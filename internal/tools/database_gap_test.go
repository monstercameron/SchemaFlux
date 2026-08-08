package tools

import (
	"context"
	"path/filepath"
	"testing"
)

// A directory cannot be opened as a SQLite file; the failure has to surface
// as an error from the tool rather than a panic the first time a query runs.
func TestSQLiteToolRefusesADirectoryAsADatabase(t *testing.T) {
	dir := t.TempDir()

	result, _ := SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dir,
		"action":   "execute",
		"sql":      "CREATE TABLE x (id INTEGER)",
	})
	if result.Success {
		t.Error("a directory is not a valid SQLite file; the tool must refuse it")
	}
}

func TestSQLiteToolQueryErrors(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"execute without sql", map[string]any{"database": ":memory:", "action": "execute"}},
		{"invalid params JSON", map[string]any{"database": ":memory:", "action": "query", "sql": "SELECT 1", "params": "not json"}},
		{"query against a nonexistent table", map[string]any{"database": ":memory:", "action": "query", "sql": "SELECT * FROM nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := SQLiteTool.Execute(context.Background(), tc.params)
			if result.Success {
				t.Errorf("%s: expected failure, got success", tc.name)
			}
		})
	}
}

// Blob columns are converted from []byte to string, so a caller gets a JSON-
// friendly value back instead of a base64 byte-slice wrapper.
func TestSQLiteToolBlobColumnBecomesAString(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "blob.db")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	if _, err := db.Execute("CREATE TABLE b (data BLOB)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Execute("INSERT INTO b (data) VALUES (?)", []byte("raw bytes")); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := db.Query("SELECT data FROM b")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if _, ok := rows[0]["data"].(string); !ok {
		t.Errorf("expected the blob column to convert to a string, got %T", rows[0]["data"])
	}
}

// Asking for the schema of a table that does not exist is an error, not an
// empty string -- an empty string reads as "the table has no columns."
func TestSQLiteSchemaOfAMissingTableIsAnError(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	if _, err := db.Schema("does_not_exist"); err == nil {
		t.Error("expected an error for a table that was never created")
	}
}

// Asking for the schema with no table named returns every table's schema,
// not just the first one.
func TestSQLiteToolSchemaWithoutATableReturnsEveryTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "schemas.db")
	db, _ := OpenSQLite(dbPath)
	db.Execute("CREATE TABLE a (id INTEGER)")
	db.Execute("CREATE TABLE b (id INTEGER)")
	db.Close()

	result, _ := SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"action":   "schema",
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	schemas, ok := result.Data.(map[string]string)
	if !ok || len(schemas) != 2 {
		t.Errorf("expected schemas for both tables, got %#v", result.Data)
	}
}

func TestMigrateToolRefusesInvalidMigrationsJSON(t *testing.T) {
	result, _ := MigrateTool.Execute(context.Background(), map[string]any{
		"database":   filepath.Join(t.TempDir(), "bad.db"),
		"migrations": "not a json array",
	})
	if result.Success {
		t.Error("invalid migrations JSON must fail the tool")
	}
}

// The down direction removes the migration record without re-running any
// SQL -- there is no reverse statement to run, so applying "down" against an
// already-applied migration is a bookkeeping change only.
func TestMigrateToolDownRemovesTheAppliedRecord(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate_down.db")
	migration := `["CREATE TABLE widgets (id INTEGER)"]`

	up, err := MigrateTool.Execute(context.Background(), map[string]any{
		"database":   dbPath,
		"migrations": migration,
		"direction":  "up",
	})
	if err != nil || !up.Success {
		t.Fatalf("up migration failed: %v %s", err, up.Error)
	}

	down, err := MigrateTool.Execute(context.Background(), map[string]any{
		"database":   dbPath,
		"migrations": migration,
		"direction":  "down",
	})
	if err != nil || !down.Success {
		t.Fatalf("down migration failed: %v %s", err, down.Error)
	}

	db, _ := OpenSQLite(dbPath)
	defer db.Close()
	rows, _ := db.Query("SELECT * FROM _migrations")
	if len(rows) != 0 {
		t.Errorf("expected the migration record to be removed, got %d rows", len(rows))
	}
}

func TestSeedToolRefusesInvalidDataJSON(t *testing.T) {
	result, _ := SeedTool.Execute(context.Background(), map[string]any{
		"database": filepath.Join(t.TempDir(), "bad_seed.db"),
		"table":    "users",
		"data":     "not a json array",
	})
	if result.Success {
		t.Error("invalid data JSON must fail the tool")
	}
}

// Seeding a table that was never created fails on the first insert, rather
// than silently reporting zero rows inserted.
func TestSeedToolRefusesAMissingTable(t *testing.T) {
	result, _ := SeedTool.Execute(context.Background(), map[string]any{
		"database": filepath.Join(t.TempDir(), "missing_table.db"),
		"table":    "ghost",
		"data":     `[{"name": "Alice"}]`,
	})
	if result.Success {
		t.Error("seeding a table that does not exist must fail")
	}
}

// Restore swaps source and destination, so a backup taken earlier can be
// written back over the live database.
func TestBackupToolRestoreSwapsSourceAndDest(t *testing.T) {
	tmpDir := t.TempDir()
	backupPath := filepath.Join(tmpDir, "backup.db")
	livePath := filepath.Join(tmpDir, "live.db")

	db, _ := OpenSQLite(backupPath)
	db.Execute("CREATE TABLE data (value TEXT)")
	db.Execute("INSERT INTO data (value) VALUES ('from-backup')")
	db.Close()

	// Restore swaps source and dest internally, so to restore FROM backupPath
	// INTO livePath, "source" names the target and "dest" names the backup.
	result, err := BackupTool.Execute(context.Background(), map[string]any{
		"source": livePath,
		"dest":   backupPath,
		"action": "restore",
	})
	if err != nil || !result.Success {
		t.Fatalf("restore failed: %v %s", err, result.Error)
	}

	restored, _ := OpenSQLite(livePath)
	defer restored.Close()
	rows, _ := restored.Query("SELECT * FROM data")
	if len(rows) != 1 || rows[0]["value"] != "from-backup" {
		t.Error("restore did not copy the backup's data to the live path")
	}
}

// A backup of a source file that does not exist must fail, not create an
// empty destination that looks like a successful, if empty, backup.
func TestBackupToolRefusesAMissingSource(t *testing.T) {
	tmpDir := t.TempDir()
	result, _ := BackupTool.Execute(context.Background(), map[string]any{
		"source": filepath.Join(tmpDir, "does-not-exist.db"),
		"dest":   filepath.Join(tmpDir, "out.db"),
		"action": "backup",
	})
	if result.Success {
		t.Error("backing up a nonexistent source must fail")
	}
}
