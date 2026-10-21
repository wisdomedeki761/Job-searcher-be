package migrations

import (
	"io/fs"
	"testing"
)

// TestEmbeddedMigrations guards against the classic embed pitfall: migration
// files added after a binary was built are missing from that binary's embedded
// filesystem. This test fails loudly if the expected migrations are absent.
func TestEmbeddedMigrations(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatalf("read embedded FS: %v", err)
	}

	found := map[string]bool{}
	for _, e := range entries {
		found[e.Name()] = true
	}

	for _, want := range []string{"0001_init.sql", "0002_jobs_search_config_id.sql"} {
		if !found[want] {
			t.Errorf("migration %s is not embedded — rebuild the binary after adding it", want)
		}
	}
	if len(found) == 0 {
		t.Fatal("no migrations embedded at all")
	}
	t.Logf("embedded migrations: %d files", len(found))
}
