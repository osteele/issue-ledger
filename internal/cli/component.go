package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/osteele/agent-issues/internal/store"
)

var (
	componentPrefix string
	componentRepo   string
	componentPath   string
)

var componentCmd = &cobra.Command{
	Use:     "component",
	Aliases: []string{"components"},
	Short:   "Manage the components issues are filed against",
}

var componentAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register a component",
	Args:  cobra.ExactArgs(1),
	RunE:  runComponentAdd,
}

var componentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered components",
	Args:  cobra.NoArgs,
	RunE:  runComponentList,
}

var componentSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Set a component's repo or local path",
	Args:  cobra.ExactArgs(1),
	RunE:  runComponentSet,
}

func init() {
	rootCmd.AddCommand(componentCmd)
	componentCmd.AddCommand(componentAddCmd, componentListCmd, componentSetCmd)

	componentAddCmd.Flags().StringVar(&componentPrefix, "prefix", "", "id prefix, 1-5 lowercase letters; derived from the name when omitted")
	componentAddCmd.Flags().StringVar(&componentRepo, "repo", "", "owner/name, used only by an explicit `issues publish`")
	componentAddCmd.Flags().StringVar(&componentPath, "path", "", "local checkout, so \".\" resolves to this component")

	componentSetCmd.Flags().StringVar(&componentRepo, "repo", "", "owner/name")
	componentSetCmd.Flags().StringVar(&componentPath, "path", "", "local checkout; \".\" uses the working directory")
}

func runComponentAdd(_ *cobra.Command, args []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	path, err := absPathOrEmpty(componentPath)
	if err != nil {
		return err
	}
	component, err := store.RegisterComponent(database, store.Component{
		Name:   args[0],
		Prefix: componentPrefix,
		Repo:   componentRepo,
		Path:   path,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Registered %s with prefix %s (ids look like %s1)\n",
		component.Name, component.Prefix, component.Prefix)
	return nil
}

func runComponentList(_ *cobra.Command, _ []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	components, err := store.ListComponents(database)
	if err != nil {
		return err
	}
	if len(components) == 0 {
		fmt.Println("No components registered. Add one with `issues component add <name>`.")
		return nil
	}
	fmt.Printf("%-18s %-7s %5s %6s  %s\n", "COMPONENT", "PREFIX", "OPEN", "TOTAL", "PATH")
	for _, c := range components {
		fmt.Printf("%-18s %-7s %5d %6d  %s\n", c.Name, c.Prefix, c.Open, c.Total, c.Path)
	}
	return nil
}

func runComponentSet(_ *cobra.Command, args []string) error {
	database, err := openLedger()
	if err != nil {
		return err
	}
	defer database.Close()

	if strings.TrimSpace(componentRepo) == "" && strings.TrimSpace(componentPath) == "" {
		return fmt.Errorf("pass --repo and/or --path")
	}
	path, err := absPathOrEmpty(componentPath)
	if err != nil {
		return err
	}
	component, err := store.UpdateComponent(database, args[0], componentRepo, path)
	if err != nil {
		return err
	}
	fmt.Printf("%s: repo=%q path=%q\n", component.Name, component.Repo, component.Path)
	return nil
}

// absPathOrEmpty resolves a path flag, treating "." as the working directory
// so a component can be registered from inside its own checkout.
func absPathOrEmpty(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if value == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine working directory: %w", err)
		}
		return cwd, nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", value, err)
	}
	return abs, nil
}
