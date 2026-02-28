package cli

import (
	"fmt"
	goruntime "runtime"

	"github.com/spf13/cobra"
)

// Version information set at build time via -ldflags.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "none"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show AIMA version and build information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "aima %s\n", Version)
			fmt.Fprintf(cmd.OutOrStdout(), "  build:  %s\n", BuildTime)
			fmt.Fprintf(cmd.OutOrStdout(), "  commit: %s\n", GitCommit)
			fmt.Fprintf(cmd.OutOrStdout(), "  go:     %s\n", goruntime.Version())
			return nil
		},
	}
}
