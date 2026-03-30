package cli

import (
	"fmt"
	goruntime "runtime"

	"github.com/jguan/aima/internal/buildinfo"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show AIMA version and build information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "aima %s\n", buildinfo.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "  build:  %s\n", buildinfo.BuildTime)
			fmt.Fprintf(cmd.OutOrStdout(), "  commit: %s\n", buildinfo.GitCommit)
			fmt.Fprintf(cmd.OutOrStdout(), "  go:     %s\n", goruntime.Version())
			return nil
		},
	}
}
