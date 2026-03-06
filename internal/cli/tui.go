package cli

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jguan/aima/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd(app *App) *cobra.Command {
	var endpoint string

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			if endpoint == "" {
				endpoint = "http://localhost:6188"
			}
			m := tui.NewModel(endpoint)
			p := tea.NewProgram(m, tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "", "AIMA API endpoint (default: http://localhost:6188)")
	return cmd
}
