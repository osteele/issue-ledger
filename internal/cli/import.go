package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/osteele/agent-issues/internal/store"
)

var (
	importSource    string
	importComponent string
	importReconcile bool
)

var importWeftCmd = &cobra.Command{
	Use:   "import-weft",
	Short: "Import weft's bug ledger, preserving issue numbers",
	Long: `Import weft's standalone bug ledger into this one.

Each bug keeps its number, so an id already cited in a lab notebook, review
ledger, or commit message still resolves. The source is opened read-only and
is not modified. The import is idempotent: re-running it picks up only bugs
that were not imported before. Pass --reconcile to also refresh issues that
were already imported, which is what a cutover needs when the old ledger kept
receiving writes after the first import.`,
	Args: cobra.NoArgs,
	RunE: runImportWeft,
}

func init() {
	rootCmd.AddCommand(importWeftCmd)
	importWeftCmd.Flags().StringVar(&importSource, "source", "", "path to weft's bugs.db; defaults to ~/.local/share/weft/bugs.db")
	importWeftCmd.Flags().StringVar(&importComponent, "component", "weft", "component to import into")
	importWeftCmd.Flags().BoolVar(&importReconcile, "reconcile", false, "also refresh already-imported issues from the source (status, close verdict, counters, new notes)")
}

func runImportWeft(_ *cobra.Command, _ []string) error {
	source := strings.TrimSpace(importSource)
	if source == "" {
		defaultSource, err := store.DefaultWeftBugDBPath()
		if err != nil {
			return err
		}
		source = defaultSource
	}
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	result, err := store.ImportWeftBugs(database, source, importComponent, importReconcile)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %d issues (%d notes) from %s into %s\n",
		result.Imported, result.Notes, source, importComponent)
	if result.Reconciled > 0 {
		fmt.Printf("Reconciled %d already-imported issues\n", result.Reconciled)
	}
	if result.Skipped > 0 {
		fmt.Printf("Skipped %d already imported\n", result.Skipped)
	}
	return nil
}
