// Package cli is the `issues` command line.
package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/osteele/agent-issues/internal/store"
)

// ReporterEnv lets a session stamp its own name on everything it files without
// passing --reporter to every call.
const ReporterEnv = "AGENT_ISSUES_REPORTER"

var rootCmd = &cobra.Command{
	Use:   "issues",
	Short: "Local issue ledger for locally developed tools",
	Long: `Local issue ledger shared across projects.

Issues are filed against a component -- a tool, service, or repository -- by
whoever hit the defect, which is usually an agent working in a different
project. Repeat sightings of the same fingerprint collapse onto one issue and
raise its occurrence count; a sighting after a close records a recurrence
instead of silently reopening.

The ledger is local and is never synchronized. Publishing an issue to a public
tracker is an explicit per-issue decision (see ` + "`issues publish`" + `).`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the command line.
func Execute() error { return rootCmd.Execute() }

func openLedger() (*sql.DB, error) { return store.Open() }

// resolveComponent turns a --component value into a registered component name.
// "." and "" resolve through the current working directory, so a session
// working in a registered checkout does not need to name it.
func resolveComponent(database *sql.DB, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" && value != "." {
		return strings.ToLower(value), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	components, err := store.ListComponents(database)
	if err != nil {
		return "", err
	}
	// Longest registered path wins, so a component inside another component's
	// tree resolves to the inner one.
	best := ""
	bestLen := 0
	for _, c := range components {
		if c.Path == "" {
			continue
		}
		path, err := filepath.Abs(c.Path)
		if err != nil {
			continue
		}
		if cwd == path || strings.HasPrefix(cwd, path+string(filepath.Separator)) {
			if len(path) > bestLen {
				best, bestLen = c.Name, len(path)
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("no component registered for %s: pass --component, or set a path with `issues component set <name> --path <dir>`", cwd)
	}
	return best, nil
}

func defaultReporter() string { return strings.TrimSpace(os.Getenv(ReporterEnv)) }

func formatTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func age(ts int64) string {
	if ts <= 0 {
		return ""
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
