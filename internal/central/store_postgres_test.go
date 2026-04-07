package central

import (
	"context"
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
