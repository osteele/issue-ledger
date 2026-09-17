package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/osteele/agent-issues/internal/store"
)

var (
	reportComponent   string
	reportTitle       string
	reportKind        string
	reportScope       string
	reportLikelihood  string
	reportSeverity    string
	reportFingerprint string
	reportRef         string
	reportReporter    string
	reportSummary     string
	reportDetail      string
	reportNote        string

	noteStdin   bool
	noteAuthor  string
	listAll     bool
	listComp    string
	closeReason string
)

var reportCmd = &cobra.Command{
	Use:   "report [title]",
	Short: "File an issue against a component and print its id",
	Args:  cobra.ArbitraryArgs,
	RunE:  runReport,
}

var noteCmd = &cobra.Command{
	Use:   "note [--stdin] <issue-id> [text...]",
	Short: "Append context to an existing issue",
	Args:  validateNoteArgs,
	RunE:  runNote,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List outstanding issues",
	Args:  cobra.NoArgs,
	RunE:  runList,
}

var showCmd = &cobra.Command{
	Use:   "show <issue-id>",
	Short: "Show an issue and its notes",
	Args:  cobra.ExactArgs(1),
	RunE:  runShow,
}

var closeCmd = &cobra.Command{
	Use:   "close <issue-id>",
	Short: "Close an issue",
	Args:  cobra.ExactArgs(1),
	RunE:  runClose,
}

var reopenCmd = &cobra.Command{
	Use:   "reopen <issue-id>",
	Short: "Reopen a closed issue",
	Args:  cobra.ExactArgs(1),
	RunE:  runReopen,
}

func init() {
	rootCmd.AddCommand(reportCmd, noteCmd, listCmd, showCmd, closeCmd, reopenCmd)

	reportCmd.Flags().StringVar(&reportComponent, "component", "", "component the issue is against; \".\" resolves the working directory")
	reportCmd.Flags().StringVar(&reportTitle, "title", "", "issue title (or pass as positional args)")
	reportCmd.Flags().StringVar(&reportKind, "kind", "bug", "kind: bug, invariant, regression, doc, ...")
	reportCmd.Flags().StringVar(&reportScope, "scope", "", "free-form scope label, e.g. infrastructure, network")
	reportCmd.Flags().StringVar(&reportLikelihood, "likelihood", "unknown", "likelihood of recurrence")
	reportCmd.Flags().StringVar(&reportSeverity, "severity", "notice", "severity: internal, notice, warning, error")
	reportCmd.Flags().StringVar(&reportFingerprint, "fingerprint", "", "stable dedupe key; defaults to the title")
	reportCmd.Flags().StringVar(&reportRef, "ref", "", "occurrence reference: a job id, host, commit, session")
	reportCmd.Flags().StringVar(&reportReporter, "reporter", "", "who is filing; defaults to $"+ReporterEnv)
	reportCmd.Flags().StringVar(&reportSummary, "summary", "", "short summary safe to show a user")
	reportCmd.Flags().StringVar(&reportDetail, "detail", "", "maintainer evidence: paths, output, invariants")
	reportCmd.Flags().StringVar(&reportNote, "note", "", "initial note")

	noteCmd.Flags().BoolVar(&noteStdin, "stdin", false, "read note text from stdin")
	noteCmd.Flags().StringVar(&noteAuthor, "author", "", "who is adding the note; defaults to $"+ReporterEnv)
	noteCmd.Flags().SetInterspersed(false)

	listCmd.Flags().BoolVar(&listAll, "all", false, "include closed issues")
	listCmd.Flags().StringVar(&listComp, "component", "", "limit to one component; \".\" resolves the working directory")

	closeCmd.Flags().StringVar(&closeReason, "reason", "", "close reason")
}

func runReport(_ *cobra.Command, args []string) error {
	title := strings.TrimSpace(reportTitle)
	if title == "" {
		title = strings.TrimSpace(strings.Join(args, " "))
	}
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	component, err := resolveComponent(database, reportComponent)
	if err != nil {
		return err
	}
	reporter := reportReporter
	if strings.TrimSpace(reporter) == "" {
		reporter = defaultReporter()
	}

	issue, created, err := store.ReportIssue(database, store.Report{
		Component:   component,
		Title:       title,
		Kind:        reportKind,
		Scope:       reportScope,
		Likelihood:  reportLikelihood,
		Severity:    reportSeverity,
		Fingerprint: reportFingerprint,
		Ref:         reportRef,
		Reporter:    reporter,
		Summary:     reportSummary,
		Detail:      reportDetail,
		Note:        reportNote,
	})
	if err != nil {
		return err
	}
	verb := "Updated"
	if created {
		verb = "Filed"
	}
	fmt.Printf("%s %s (%s): %s\n", verb, issue.CitableID(), issue.Component, issue.Title)
	if !created {
		fmt.Printf("Occurrences: %d\n", issue.Occurrences)
	}
	return nil
}

func validateNoteArgs(_ *cobra.Command, args []string) error {
	if noteStdin {
		if len(args) != 1 {
			return fmt.Errorf("--stdin expects exactly one issue id and no note text")
		}
		return nil
	}
	switch len(args) {
	case 0:
		return fmt.Errorf("issue id is required")
	case 1:
		return fmt.Errorf("note text is required: pass text after the issue id, or pipe it with --stdin")
	}
	return nil
}

func runNote(_ *cobra.Command, args []string) error {
	note := strings.TrimSpace(strings.Join(args[1:], " "))
	if noteStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		note = strings.TrimRight(string(data), "\r\n")
	}
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	author := noteAuthor
	if strings.TrimSpace(author) == "" {
		author = defaultReporter()
	}
	issue, err := store.AddNote(database, args[0], note, author)
	if err != nil {
		return err
	}
	fmt.Printf("Added note to %s\n", issue.CitableID())
	return nil
}

func runList(_ *cobra.Command, _ []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	component := ""
	if strings.TrimSpace(listComp) != "" {
		if component, err = resolveComponent(database, listComp); err != nil {
			return err
		}
	}
	issues, err := store.ListIssues(database, store.ListOptions{Component: component, IncludeClosed: listAll})
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		fmt.Println("No issues.")
		return nil
	}
	for _, issue := range issues {
		line := fmt.Sprintf("%-8s %-7s %-14s %-6s %s",
			issue.CitableID(), issue.Status, issue.Component, age(issue.UpdatedAt), issue.Title)
		if issue.Status == "closed" && issue.Recurrences > 0 {
			line += fmt.Sprintf("  [%d recurrences since close]", issue.Recurrences)
		}
		fmt.Println(line)
	}
	return nil
}

func runShow(_ *cobra.Command, args []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	issue, err := store.GetIssue(database, args[0])
	if err != nil {
		return err
	}
	printIssue(issue)
	notes, err := store.ListNotes(database, issue.ID)
	if err != nil {
		return err
	}
	if len(notes) > 0 {
		fmt.Println()
		fmt.Println("Notes:")
		for _, note := range notes {
			author := ""
			if note.Author != "" {
				author = " " + note.Author + ":"
			}
			fmt.Printf("- %s%s %s\n", formatTime(note.CreatedAt), author, note.Body)
		}
	}
	return nil
}

func runClose(_ *cobra.Command, args []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()
	issue, err := store.CloseIssue(database, args[0], closeReason)
	if err != nil {
		return err
	}
	fmt.Printf("Closed %s\n", issue.CitableID())
	return nil
}

func runReopen(_ *cobra.Command, args []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()
	issue, err := store.ReopenIssue(database, args[0])
	if err != nil {
		return err
	}
	fmt.Printf("Reopened %s\n", issue.CitableID())
	return nil
}

func printIssue(issue *store.Issue) {
	fmt.Printf("Issue:       %s\n", issue.CitableID())
	fmt.Printf("Component:   %s\n", issue.Component)
	fmt.Printf("Status:      %s\n", issue.Status)
	fmt.Printf("Title:       %s\n", issue.Title)
	fmt.Printf("Kind:        %s\n", issue.Kind)
	if issue.Scope != "" {
		fmt.Printf("Scope:       %s\n", issue.Scope)
	}
	fmt.Printf("Likelihood:  %s\n", issue.Likelihood)
	fmt.Printf("Severity:    %s\n", issue.Severity)
	fmt.Printf("Fingerprint: %s\n", issue.Fingerprint)
	if issue.Ref != "" {
		fmt.Printf("Ref:         %s\n", issue.Ref)
	}
	if issue.Reporter != "" {
		fmt.Printf("Reporter:    %s\n", issue.Reporter)
	}
	fmt.Printf("Occurrences: %d\n", issue.Occurrences)
	if issue.Recurrences > 0 {
		line := fmt.Sprintf("Recurrences after close: %d", issue.Recurrences)
		if issue.LastRecurrenceAt > 0 {
			line += fmt.Sprintf(" (most recent %s)", formatTime(issue.LastRecurrenceAt))
		}
		fmt.Println(line)
	}
	fmt.Printf("Created:     %s\n", formatTime(issue.CreatedAt))
	fmt.Printf("Updated:     %s\n", formatTime(issue.UpdatedAt))
	if issue.ClosedAt != nil {
		fmt.Printf("Closed:      %s\n", formatTime(*issue.ClosedAt))
	}
	if issue.CloseReason != "" {
		fmt.Printf("Close reason: %s\n", issue.CloseReason)
	}
	if issue.Summary != "" {
		fmt.Printf("Summary:     %s\n", issue.Summary)
	}
	if issue.Detail != "" {
		fmt.Println("Detail:")
		fmt.Println(issue.Detail)
	}
}
