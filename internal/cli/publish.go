package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/osteele/agent-issues/internal/store"
)

const publishTimeout = 30 * time.Second

var (
	publishRepo    string
	publishConfirm bool
	publishDetail  bool
	publishNotes   bool
	publishRef     bool
	publishLabels  []string
)

// runGH is indirected so the publish path is testable without a network or a
// GitHub account.
var runGH = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return out, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
		}
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, detail)
	}
	return out, nil
}

var publishCmd = &cobra.Command{
	Use:   "publish <issue-id>",
	Short: "Export one issue to a public tracker, after showing exactly what would be sent",
	Long: `Export one issue to a GitHub tracker.

This ledger is private; most issues here quote job identifiers, unpublished
research description, and absolute local paths. So publishing is deliberate and
minimal rather than a sync:

  - It prints the exact body and exits. Nothing is sent without --yes.
  - The body carries the title, kind, severity, likelihood and summary only.
    Detail, ref, reporter and notes are the fields that hold local specifics,
    and each is opt-in: --include-detail, --include-ref, --include-notes.
  - Review the printed body before passing --yes. The fields you opt into are
    published verbatim; nothing is redacted for you.`,
	Args: cobra.ExactArgs(1),
	RunE: runPublish,
}

func init() {
	rootCmd.AddCommand(publishCmd)
	publishCmd.Flags().StringVar(&publishRepo, "repo", "", "target owner/name; defaults to the component's registered repo")
	publishCmd.Flags().BoolVar(&publishConfirm, "yes", false, "actually create the issue (without this, prints and exits)")
	publishCmd.Flags().BoolVar(&publishDetail, "include-detail", false, "include the detail field (maintainer evidence: paths, output)")
	publishCmd.Flags().BoolVar(&publishRef, "include-ref", false, "include the ref field (job id, host, session)")
	publishCmd.Flags().BoolVar(&publishNotes, "include-notes", false, "include all notes")
	publishCmd.Flags().StringSliceVar(&publishLabels, "label", nil, "labels to apply")
}

func runPublish(_ *cobra.Command, args []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	issue, err := store.GetIssue(database, args[0])
	if err != nil {
		return err
	}
	repo := strings.TrimSpace(publishRepo)
	if repo == "" {
		component, err := store.GetComponent(database, issue.Component)
		if err != nil {
			return err
		}
		repo = component.Repo
	}
	if repo == "" {
		return fmt.Errorf("no target repo: pass --repo, or set one with `issues component set %s --repo owner/name`", issue.Component)
	}

	if prior, err := store.GetPublication(database, issue.ID, repo); err == nil {
		return fmt.Errorf("%s was already published to %s: %s", issue.CitableID(), repo, prior.URL)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var notes []store.Note
	if publishNotes {
		if notes, err = store.ListNotes(database, issue.ID); err != nil {
			return err
		}
	}
	body := renderPublicBody(issue, notes)

	fmt.Printf("Target repo:  %s\n", repo)
	fmt.Printf("Title:        %s\n", issue.Title)
	fmt.Println("Body:")
	fmt.Println("---")
	fmt.Println(body)
	fmt.Println("---")

	if !publishConfirm {
		omitted := omittedFields(issue, publishDetail, publishRef, publishNotes)
		if len(omitted) > 0 {
			fmt.Printf("\nWithheld: %s. Add with %s.\n",
				strings.Join(omitted, ", "), strings.Join(omitFlags(omitted), ", "))
		}
		fmt.Println("\nNothing was sent. Re-run with --yes to create this issue.")
		return nil
	}

	ghArgs := []string{"issue", "create", "--repo", repo, "--title", issue.Title, "--body", body}
	for _, label := range publishLabels {
		ghArgs = append(ghArgs, "--label", label)
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	out, err := runGH(ctx, ghArgs...)
	if err != nil {
		return err
	}
	url := strings.TrimSpace(lastNonEmptyLine(string(out)))
	if err := store.RecordPublication(database, issue.ID, repo, url); err != nil {
		return fmt.Errorf("issue created at %s but recording it failed: %w", url, err)
	}
	fmt.Printf("Published %s to %s\n", issue.CitableID(), url)
	return nil
}

// renderPublicBody builds the issue body. Fields that carry local specifics are
// included only when explicitly requested.
func renderPublicBody(issue *store.Issue, notes []store.Note) string {
	var b strings.Builder
	if issue.Summary != "" {
		b.WriteString(issue.Summary)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "- Kind: %s\n", issue.Kind)
	fmt.Fprintf(&b, "- Severity: %s\n", issue.Severity)
	fmt.Fprintf(&b, "- Likelihood of recurrence: %s\n", issue.Likelihood)
	fmt.Fprintf(&b, "- Observed %d time(s)\n", issue.Occurrences)
	if issue.Scope != "" {
		fmt.Fprintf(&b, "- Scope: %s\n", issue.Scope)
	}
	if publishRef && issue.Ref != "" {
		fmt.Fprintf(&b, "- Reference: %s\n", issue.Ref)
	}
	if publishDetail && issue.Detail != "" {
		b.WriteString("\n## Detail\n\n")
		b.WriteString(issue.Detail)
		b.WriteString("\n")
	}
	if len(notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, note := range notes {
			fmt.Fprintf(&b, "- %s\n", note.Body)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func omittedFields(issue *store.Issue, detail, ref, notes bool) []string {
	var out []string
	if !detail && issue.Detail != "" {
		out = append(out, "detail")
	}
	if !ref && issue.Ref != "" {
		out = append(out, "ref")
	}
	if !notes {
		out = append(out, "notes")
	}
	return out
}

func omitFlags(fields []string) []string {
	flags := make([]string, 0, len(fields))
	for _, f := range fields {
		flags = append(flags, "--include-"+f)
	}
	return flags
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
