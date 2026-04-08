package cli

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jguan/aima/internal/engine"
	"github.com/spf13/cobra"
)

func newEngineCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "engine",
		Short: "Manage inference engines",
	}

	cmd.AddCommand(
		newEngineScanCmd(app),
		newEngineListCmd(app),
		newEngineInfoCmd(app),
		newEnginePullCmd(app),
		newEngineImportCmd(app),
		newEngineRemoveCmd(app),
	)

	return cmd
}

func newEngineScanCmd(app *App) *cobra.Command {
	var (
		runtime    string
		autoImport bool
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for locally available engine images",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if runtime == "" {
				runtime = "auto"
			}
			if runtime != "auto" && runtime != "container" && runtime != "native" {
				return fmt.Errorf("invalid runtime: %s (must be auto, container, or native)", runtime)
			}

			data, err := app.ToolDeps.ScanEngines(ctx, runtime, autoImport)
			if err != nil {
				return fmt.Errorf("scan engines: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "auto", "Runtime filter: auto, container, or native")
	cmd.Flags().BoolVar(&autoImport, "import", false, "Auto-import Docker images to K3S containerd (requires root)")
	return cmd
}

func newEngineInfoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info <name>",
		Short: "Get full information about an engine (catalog knowledge + live availability)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			data, err := app.ToolDeps.GetEngineInfo(ctx, name)
			if err != nil {
				return fmt.Errorf("engine info %s: %w", name, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newEngineListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List known engines from the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			data, err := app.ToolDeps.ListEngines(ctx)
			if err != nil {
				return fmt.Errorf("list engines: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), formatJSON(data))
			return nil
		},
	}
}

func newEnginePullCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "pull [name]",
		Short: "Pull an inference engine (default: catalog default engine)",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var name string
			if len(args) > 0 {
				name = args[0]
			}

			if name == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Pulling default engine...")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Pulling engine %s...\n", name)
			}

			w := cmd.OutOrStdout()
			isTTY := isTerminal(w)

			pr := &pullProgressRenderer{
				w:          w,
				isTTY:      isTTY,
				lastReport: -1,
			}

			if err := app.ToolDeps.PullEngine(ctx, name, pr.onProgress); err != nil {
				pr.finish()
				return fmt.Errorf("pull engine: %w", err)
			}
			pr.finish()

			fmt.Fprintln(w, "Engine pulled and registered successfully")
			return nil
		},
	}
}

// pullProgressRenderer renders download progress to a terminal or log output.
type pullProgressRenderer struct {
	mu         sync.Mutex
	w          interface{ Write([]byte) (int, error) }
	isTTY      bool
	lastReport int   // last reported percentage (for non-TTY deduplication)
	started    bool  // whether we've printed any progress line
	lastUpdate time.Time
}

func (r *pullProgressRenderer) onProgress(ev engine.ProgressEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch ev.Phase {
	case "already_available":
		fmt.Fprintf(r.w, "  %s\n", ev.Message)
		return
	case "complete":
		return // handled by finish()
	case "extracting":
		if r.started && r.isTTY {
			fmt.Fprintf(r.w, "\r\033[K")
		}
		fmt.Fprintf(r.w, "  Extracting...\n")
		r.started = false
		return
	}

	// Rate-limit updates: at most every 200ms for TTY, every 5% for non-TTY
	now := time.Now()
	if r.isTTY && now.Sub(r.lastUpdate) < 200*time.Millisecond {
		return
	}
	r.lastUpdate = now

	if ev.Total > 0 && ev.Downloaded > 0 {
		pct := int(ev.Downloaded * 100 / ev.Total)
		downMB := float64(ev.Downloaded) / (1024 * 1024)
		totalMB := float64(ev.Total) / (1024 * 1024)

		if r.isTTY {
			// Single-line overwrite progress bar
			barWidth := 30
			filled := barWidth * pct / 100
			bar := make([]byte, barWidth)
			for i := range bar {
				if i < filled {
					bar[i] = '='
				} else if i == filled {
					bar[i] = '>'
				} else {
					bar[i] = ' '
				}
			}

			speedStr := ""
			if ev.Speed > 0 {
				speedMBs := float64(ev.Speed) / (1024 * 1024)
				remaining := float64(ev.Total-ev.Downloaded) / float64(ev.Speed)
				speedStr = fmt.Sprintf("  %.1f MB/s  ~%s", speedMBs, formatDuration(remaining))
			}
			fmt.Fprintf(r.w, "\r  [%s] %.0f MB / %.0f MB  %d%%%s", string(bar), downMB, totalMB, pct, speedStr)
			r.started = true
		} else {
			// Non-TTY: print every 10%
			bucket := (pct / 10) * 10
			if bucket > r.lastReport {
				r.lastReport = bucket
				fmt.Fprintf(r.w, "  %.0f MB / %.0f MB  %d%%\n", downMB, totalMB, pct)
			}
		}
	} else if ev.Downloaded > 0 {
		// Unknown total size
		downMB := float64(ev.Downloaded) / (1024 * 1024)
		if r.isTTY {
			fmt.Fprintf(r.w, "\r  %.0f MB downloaded...", downMB)
			r.started = true
		}
	}
}

func (r *pullProgressRenderer) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started && r.isTTY {
		fmt.Fprintf(r.w, "\n")
		r.started = false
	}
}

// isTerminal checks if w is a terminal (character device).
func isTerminal(w interface{ Write([]byte) (int, error) }) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// formatDuration formats seconds into a human-readable duration string.
func formatDuration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", int(seconds))
	}
	m := int(seconds) / 60
	s := int(seconds) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

func newEngineImportCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "import <path>",
		Short: "Import an engine image from a tar file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			tarPath := args[0]

			if err := app.ToolDeps.ImportEngine(ctx, tarPath); err != nil {
				return fmt.Errorf("import engine from %s: %w", tarPath, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Engine image imported from %s\n", tarPath)
			return nil
		},
	}
}

func newEngineRemoveCmd(app *App) *cobra.Command {
	var deleteFiles bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an engine from the database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			if app.ToolDeps.RemoveEngine == nil {
				return fmt.Errorf("engine.remove not implemented")
			}
			if err := app.ToolDeps.RemoveEngine(ctx, name, deleteFiles); err != nil {
				return fmt.Errorf("remove engine %s: %w", name, err)
			}

			if deleteFiles {
				fmt.Fprintf(cmd.OutOrStdout(), "Engine %s removed (files cleaned up)\n", name)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Engine %s removed\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&deleteFiles, "delete-files", "f", false, "Also delete the actual container image or native binary")
	return cmd
}
