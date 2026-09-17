// Command issues is the local issue ledger CLI.
package main

import (
	"fmt"
	"os"

	"github.com/osteele/agent-issues/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
