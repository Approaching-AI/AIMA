package central

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestPostgresCentralStore(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping PostgreSQL tests")
	}

	store, err := NewPostgresCentralStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresCentralStore: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	storeTestSuite(t, store)
}

func TestPostgresCentralStore_ListScenariosTimestampsRFC3339UTC(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping PostgreSQL tests")
	}

	store, err := NewPostgresCentralStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresCentralStore: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	scenario := Scenario{
		ID:              "scn-ts-rfc3339",
		Name:            fmt.Sprintf("scenario-ts-%s", t.Name()),
		Description:     "timestamp normalization regression test",
		HardwareProfile: "nvidia-gb10-arm64",
		ScenarioYAML:    "name: timestamp-test\n",
		Config:          `{}`,
		Source:          "generated",
		Version:         1,
		CreatedAt:       "2026-04-20T10:50:20Z",
		UpdatedAt:       "2026-04-20T10:50:20Z",
	}
	if err := store.InsertScenario(context.Background(), scenario); err != nil {
		t.Fatalf("InsertScenario: %v", err)
	}

	items, err := store.ListScenarios(context.Background(), ScenarioFilter{Name: scenario.Name, Limit: 1})
	if err != nil {
		t.Fatalf("ListScenarios: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("ListScenarios returned %d items, want 1", len(items))
	}
	if got := items[0].CreatedAt; got != "2026-04-20T10:50:20Z" {
		t.Fatalf("CreatedAt = %q, want RFC3339 UTC string", got)
	}
	if got := items[0].UpdatedAt; got != "2026-04-20T10:50:20Z" {
		t.Fatalf("UpdatedAt = %q, want RFC3339 UTC string", got)
	}
}
