package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteOpenAndQuery(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite error: %v", err)
	}
	defer db.Close()

	// Create table
	_, err = db.Execute(`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		age INTEGER
	)`)
	if err != nil {
		t.Fatalf("Create table error: %v", err)
	}

	// Insert data
	_, err = db.Execute("INSERT INTO users (name, age) VALUES (?, ?)", "Alice", 30)
	if err != nil {
		t.Fatalf("Insert error: %v", err)
	}
	_, err = db.Execute("INSERT INTO users (name, age) VALUES (?, ?)", "Bob", 25)
	if err != nil {
		t.Fatalf("Insert error: %v", err)
	}

	// Query data
	results, err := db.Query("SELECT * FROM users ORDER BY name")
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 rows, got %d", len(results))
	}
	if results[0]["name"] != "Alice" {
		t.Errorf("Expected 'Alice', got %v", results[0]["name"])
	}
}

func TestSQLiteTables(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite error: %v", err)
	}
	defer db.Close()

	db.Execute("CREATE TABLE table1 (id INTEGER)")
	db.Execute("CREATE TABLE table2 (id INTEGER)")

	tables, err := db.Tables()
	if err != nil {
		t.Fatalf("Tables error: %v", err)
	}

	if len(tables) != 2 {
		t.Errorf("Expected 2 tables, got %d", len(tables))
	}
}

func TestSQLiteSchema(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite error: %v", err)
	}
	defer db.Close()

	db.Execute("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")

	schema, err := db.Schema("users")
	if err != nil {
		t.Fatalf("Schema error: %v", err)
	}

	if schema == "" {
		t.Error("Expected schema, got empty string")
	}
}

func TestSQLiteTool(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "tool_test.db")

	// Create table
	result, err := SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Insert data
	SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO items (name) VALUES (?)",
		"params":   `["Item 1"]`,
	})

	// Query data
	result, _ = SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"action":   "query",
		"sql":      "SELECT * FROM items",
	})
	if !result.Success {
		t.Errorf("Query failed: %s", result.Error)
	}

	rows := result.Data.([]map[string]any)
	if len(rows) != 1 {
		t.Errorf("Expected 1 row, got %d", len(rows))
	}

	// List tables
	result, _ = SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"action":   "tables",
	})
	tables := result.Data.([]string)
	if len(tables) != 1 || tables[0] != "items" {
		t.Errorf("Expected [items], got %v", tables)
	}

	// Get schema
	result, _ = SQLiteTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"action":   "schema",
		"sql":      "items",
	})
	if result.Data.(string) == "" {
		t.Error("Expected schema")
	}
}

func TestSQLiteToolInMemory(t *testing.T) {
	result, err := SQLiteTool.Execute(context.Background(), map[string]any{
		"database": ":memory:",
		"action":   "execute",
		"sql":      "CREATE TABLE test (id INTEGER)",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}
}

func TestMigrateTool(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migrate.db")

	result, err := MigrateTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"migrations": `[
			"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)",
			"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT)"
		]`,
		"direction": "up",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Verify tables were created
	db, _ := OpenSQLite(dbPath)
	defer db.Close()

	tables, _ := db.Tables()
	// Should have users, posts, and _migrations
	if len(tables) < 2 {
		t.Errorf("Expected at least 2 tables, got %d", len(tables))
	}
}

func TestSeedTool(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "seed.db")

	// Create table first
	db, _ := OpenSQLite(dbPath)
	db.Execute("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT)")
	db.Close()

	result, err := SeedTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"table":    "users",
		"data": `[
			{"name": "Alice", "email": "alice@example.com"},
			{"name": "Bob", "email": "bob@example.com"}
		]`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Verify data
	db, _ = OpenSQLite(dbPath)
	defer db.Close()

	rows, _ := db.Query("SELECT * FROM users")
	if len(rows) != 2 {
		t.Errorf("Expected 2 rows, got %d", len(rows))
	}
}

func TestSeedToolTruncate(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "seed_truncate.db")

	// Create and seed initial data
	db, _ := OpenSQLite(dbPath)
	db.Execute("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)")
	db.Execute("INSERT INTO items (name) VALUES ('existing')")
	db.Close()

	// Seed with truncate
	SeedTool.Execute(context.Background(), map[string]any{
		"database": dbPath,
		"table":    "items",
		"data":     `[{"name": "new"}]`,
		"truncate": true,
	})

	// Verify only new data exists
	db, _ = OpenSQLite(dbPath)
	defer db.Close()

	rows, _ := db.Query("SELECT * FROM items")
	if len(rows) != 1 {
		t.Errorf("Expected 1 row, got %d", len(rows))
	}
	if rows[0]["name"] != "new" {
		t.Errorf("Expected 'new', got %v", rows[0]["name"])
	}
}

func TestBackupTool(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.db")
	dstPath := filepath.Join(tmpDir, "backup.db")

	// Create source database with data
	db, _ := OpenSQLite(srcPath)
	db.Execute("CREATE TABLE data (value TEXT)")
	db.Execute("INSERT INTO data (value) VALUES ('important')")
	db.Close()

	// Backup
	result, err := BackupTool.Execute(context.Background(), map[string]any{
		"source": srcPath,
		"dest":   dstPath,
		"action": "backup",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Verify backup
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		t.Error("Backup file not created")
	}

	db, _ = OpenSQLite(dstPath)
	defer db.Close()

	rows, _ := db.Query("SELECT * FROM data")
	if len(rows) != 1 || rows[0]["value"] != "important" {
		t.Error("Backup data mismatch")
	}
}

func TestSQLiteToolErrors(t *testing.T) {
	// Missing database
	result, _ := SQLiteTool.Execute(context.Background(), map[string]any{
		"action": "query",
	})
	if result.Success {
		t.Error("Expected failure for missing database")
	}

	// Missing SQL for query
	result, _ = SQLiteTool.Execute(context.Background(), map[string]any{
		"database": ":memory:",
		"action":   "query",
	})
	if result.Success {
		t.Error("Expected failure for missing SQL")
	}

	// Invalid action
	result, _ = SQLiteTool.Execute(context.Background(), map[string]any{
		"database": ":memory:",
		"action":   "invalid",
	})
	if result.Success {
		t.Error("Expected failure for invalid action")
	}

	// Invalid SQL
	result, _ = SQLiteTool.Execute(context.Background(), map[string]any{
		"database": ":memory:",
		"action":   "execute",
		"sql":      "INVALID SQL STATEMENT",
	})
	if result.Success {
		t.Error("Expected failure for invalid SQL")
	}
}
