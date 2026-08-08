package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// SQLiteTool provides SQLite database operations.
var SQLiteTool = &Tool{
	Name:        "sqlite",
	Description: "Execute SQLite database operations (query, execute, create tables)",
	Category:    CategoryDatabase,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"database": StringParam("Database file path (use :memory: for in-memory)"),
		"action":   EnumParam("Action to perform", []string{"query", "execute", "tables", "schema"}),
		"sql":      StringParam("SQL statement to execute"),
		"params":   StringParam("JSON array of query parameters"),
	}, []string{"database", "action"}),
	Execute: executeSQLite,
}

// SQLiteDB wraps a SQLite connection.
type SQLiteDB struct {
	db   *sql.DB
	path string
}

// OpenSQLite opens or creates a SQLite database.
func OpenSQLite(path string) (*SQLiteDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	return &SQLiteDB{db: db, path: path}, nil
}

// Close closes the database connection.
func (s *SQLiteDB) Close() error {
	return s.db.Close()
}

// Query executes a SELECT query and returns results.
func (s *SQLiteDB) Query(query string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any

	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]any)
		for i, col := range columns {
			val := values[i]
			// Convert []byte to string
			if b, ok := val.([]byte); ok {
				val = string(b)
			}
			row[col] = val
		}
		results = append(results, row)
	}

	return results, rows.Err()
}

// Execute runs an INSERT, UPDATE, DELETE, or DDL statement.
func (s *SQLiteDB) Execute(statement string, args ...any) (sql.Result, error) {
	return s.db.Exec(statement, args...)
}

// Tables returns a list of all tables.
func (s *SQLiteDB) Tables() ([]string, error) {
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// Schema returns the schema for a table.
func (s *SQLiteDB) Schema(table string) (string, error) {
	var schema string
	err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&schema)
	if err != nil {
		return "", err
	}
	return schema, nil
}

func executeSQLite(ctx context.Context, params map[string]any) (Result, error) {
	dbPath, _ := params["database"].(string)
	action, _ := params["action"].(string)
	sqlStr, _ := params["sql"].(string)
	paramsJSON, _ := params["params"].(string)

	if dbPath == "" {
		return ErrorResultFromError(fmt.Errorf("database path is required")), nil
	}

	db, err := OpenSQLite(dbPath)
	if err != nil {
		return ErrorResultFromError(fmt.Errorf("failed to open database: %w", err)), nil
	}
	defer db.Close()

	// Parse parameters if provided
	var queryParams []any
	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &queryParams); err != nil {
			return ErrorResultFromError(fmt.Errorf("invalid params JSON: %w", err)), nil
		}
	}

	switch action {
	case "query":
		if sqlStr == "" {
			return ErrorResultFromError(fmt.Errorf("sql is required for query action")), nil
		}
		results, err := db.Query(sqlStr, queryParams...)
		if err != nil {
			return ErrorResult(err), nil
		}
		return NewResultWithMeta(results, map[string]any{
			"database": dbPath,
			"rows":     len(results),
		}), nil

	case "execute":
		if sqlStr == "" {
			return ErrorResultFromError(fmt.Errorf("sql is required for execute action")), nil
		}
		result, err := db.Execute(sqlStr, queryParams...)
		if err != nil {
			return ErrorResult(err), nil
		}
		rowsAffected, _ := result.RowsAffected()
		lastID, _ := result.LastInsertId()
		return NewResultWithMeta(map[string]any{
			"rows_affected":  rowsAffected,
			"last_insert_id": lastID,
		}, map[string]any{
			"database": dbPath,
		}), nil

	case "tables":
		tables, err := db.Tables()
		if err != nil {
			return ErrorResult(err), nil
		}
		return NewResultWithMeta(tables, map[string]any{
			"database": dbPath,
			"count":    len(tables),
		}), nil

	case "schema":
		if sqlStr == "" {
			// Get all schemas
			tables, err := db.Tables()
			if err != nil {
				return ErrorResult(err), nil
			}
			schemas := make(map[string]string)
			for _, table := range tables {
				schema, _ := db.Schema(table)
				schemas[table] = schema
			}
			return NewResult(schemas), nil
		}
		schema, err := db.Schema(sqlStr)
		if err != nil {
			return ErrorResult(err), nil
		}
		return NewResult(schema), nil

	default:
		return ErrorResultFromError(fmt.Errorf("unknown action: %s", action)), nil
	}
}

// MigrateTool runs database migrations.
var MigrateTool = &Tool{
	Name:        "migrate",
	Description: "Run database schema migrations",
	Category:    CategoryDatabase,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"database":   StringParam("Database file path"),
		"migrations": StringParam("JSON array of SQL migration statements"),
		"direction":  EnumParam("Migration direction", []string{"up", "down"}),
	}, []string{"database", "migrations"}),
	Execute: executeMigrate,
}

func executeMigrate(ctx context.Context, params map[string]any) (Result, error) {
	dbPath, _ := params["database"].(string)
	migrationsJSON, _ := params["migrations"].(string)
	direction, _ := params["direction"].(string)

	if direction == "" {
		direction = "up"
	}

	var migrations []string
	if err := json.Unmarshal([]byte(migrationsJSON), &migrations); err != nil {
		return ErrorResultFromError(fmt.Errorf("invalid migrations JSON: %w", err)), nil
	}

	db, err := OpenSQLite(dbPath)
	if err != nil {
		return ErrorResult(err), nil
	}
	defer db.Close()

	// Create migrations table
	_, err = db.Execute(`CREATE TABLE IF NOT EXISTS _migrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		migration TEXT NOT NULL,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return ErrorResult(err), nil
	}

	var applied int
	for _, migration := range migrations {
		// Check if already applied
		results, _ := db.Query("SELECT id FROM _migrations WHERE migration = ?", migration)
		alreadyApplied := len(results) > 0

		if direction == "up" && !alreadyApplied {
			if _, err := db.Execute(migration); err != nil {
				return ErrorResultFromError(fmt.Errorf("migration failed: %w", err)), nil
			}
			db.Execute("INSERT INTO _migrations (migration) VALUES (?)", migration)
			applied++
		} else if direction == "down" && alreadyApplied {
			// For down migrations, we'd need the reverse SQL
			// This is a simplified version
			db.Execute("DELETE FROM _migrations WHERE migration = ?", migration)
			applied++
		}
	}

	return NewResultWithMeta(fmt.Sprintf("Applied %d migrations", applied), map[string]any{
		"database":  dbPath,
		"direction": direction,
		"applied":   applied,
	}), nil
}

// SeedTool populates database with initial data.
var SeedTool = &Tool{
	Name:        "seed",
	Description: "Seed database with initial/test data",
	Category:    CategoryDatabase,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"database": StringParam("Database file path"),
		"table":    StringParam("Table to seed"),
		"data":     StringParam("JSON array of row objects to insert"),
		"truncate": BoolParam("Truncate table before seeding"),
	}, []string{"database", "table", "data"}),
	Execute: executeSeed,
}

func executeSeed(ctx context.Context, params map[string]any) (Result, error) {
	dbPath, _ := params["database"].(string)
	table, _ := params["table"].(string)
	dataJSON, _ := params["data"].(string)
	truncate, _ := params["truncate"].(bool)

	var data []map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return ErrorResultFromError(fmt.Errorf("invalid data JSON: %w", err)), nil
	}

	db, err := OpenSQLite(dbPath)
	if err != nil {
		return ErrorResult(err), nil
	}
	defer db.Close()

	if truncate {
		db.Execute(fmt.Sprintf("DELETE FROM %s", table))
	}

	var inserted int
	for _, row := range data {
		var columns []string
		var placeholders []string
		var values []any

		for col, val := range row {
			columns = append(columns, col)
			placeholders = append(placeholders, "?")
			values = append(values, val)
		}

		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			table,
			strings.Join(columns, ", "),
			strings.Join(placeholders, ", "))

		if _, err := db.Execute(sql, values...); err != nil {
			return ErrorResultFromError(fmt.Errorf("insert failed: %w", err)), nil
		}
		inserted++
	}

	return NewResultWithMeta(fmt.Sprintf("Inserted %d rows", inserted), map[string]any{
		"database": dbPath,
		"table":    table,
		"inserted": inserted,
	}), nil
}

// BackupTool creates database backups.
var BackupTool = &Tool{
	Name:        "backup",
	Description: "Backup or restore a SQLite database",
	Category:    CategoryDatabase,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"source": StringParam("Source database path"),
		"dest":   StringParam("Destination path"),
		"action": EnumParam("Action", []string{"backup", "restore"}),
	}, []string{"source", "dest", "action"}),
	Execute: executeBackup,
}

func executeBackup(ctx context.Context, params map[string]any) (Result, error) {
	source, _ := params["source"].(string)
	dest, _ := params["dest"].(string)
	action, _ := params["action"].(string)

	if action == "restore" {
		// Swap source and dest for restore
		source, dest = dest, source
	}

	// Copy database file using file copy
	err := CopyFile(source, dest)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta(fmt.Sprintf("Database %s successful", action), map[string]any{
		"source": source,
		"dest":   dest,
		"action": action,
	}), nil
}

func init() {
	mustRegister(SQLiteTool)
	mustRegister(MigrateTool)
	mustRegister(SeedTool)
	mustRegister(BackupTool)
}
