package central

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteCentralStore(t *testing.T) {
	store, err := NewSQLiteCentralStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteCentralStore: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	storeTestSuite(t, store)
}
