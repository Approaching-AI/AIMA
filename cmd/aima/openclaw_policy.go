package main

import (
	"context"

	"github.com/jguan/aima/internal/openclaw"
)

type openClawSyncConfigReader interface {
	GetConfig(ctx context.Context, key string) (string, error)
}

func openClawImplicitSyncAllowed(ctx context.Context, store openClawSyncConfigReader) bool {
	if !openclaw.AutoSyncEnabledFromEnv() {
		return false
	}
	if store == nil {
		return true
	}
	mode, err := store.GetConfig(ctx, openclaw.ConfigKeySyncMode)
	if err != nil {
		return true
	}
	return openclaw.AutoSyncEnabledForMode(mode)
}
