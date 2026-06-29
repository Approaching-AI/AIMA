package openclaw

import (
	"context"
	"fmt"
	"strings"
)

// Exclude revokes a model from OpenClaw sync: it is removed from openclaw.json
// and future Syncs (including the auto-sync loop) skip it, until Include clears
// the mark. The revocation is persistent (survives restarts) and reversible.
func Exclude(ctx context.Context, deps *Deps, model string) error {
	if err := setExcluded(deps.ConfigPath, model, true); err != nil {
		return err
	}
	_, err := Sync(ctx, deps, false)
	return err
}

// Include clears a model's revocation so the next Sync re-adds it to openclaw.json.
func Include(ctx context.Context, deps *Deps, model string) error {
	if err := setExcluded(deps.ConfigPath, model, false); err != nil {
		return err
	}
	_, err := Sync(ctx, deps, false)
	return err
}

// setExcluded adds or removes a model from the persisted ExcludedModels set.
func setExcluded(configPath, model string, excluded bool) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model name required")
	}
	st, err := ReadManagedState(configPath)
	if err != nil {
		return err
	}
	if excluded {
		st.ExcludedModels = append(st.ExcludedModels, model) // normalize dedups
	} else {
		kept := make([]string, 0, len(st.ExcludedModels))
		for _, m := range st.ExcludedModels {
			if m != model {
				kept = append(kept, m)
			}
		}
		st.ExcludedModels = kept
	}
	return WriteManagedState(configPath, st)
}
