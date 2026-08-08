package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runDatabaseExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🗄️ DATABASE TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Create a temp database file
	tempDir := os.TempDir()
	dbPath := filepath.Join(tempDir, "schemaflux_example.db")
	defer os.Remove(dbPath)

	// Example 1: Create a table
	result, err := tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql": `CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT UNIQUE,
			age INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	})
	printResult("SQLite: Create table", result, err)

	// Example 2: Insert data
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO users (name, email, age) VALUES (?, ?, ?)",
		"params":   `["Alice", "alice@example.com", 30]`,
	})
	printResult("SQLite: Insert Alice", result, err)

	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO users (name, email, age) VALUES (?, ?, ?)",
		"params":   `["Bob", "bob@example.com", 25]`,
	})
	printResult("SQLite: Insert Bob", result, err)

	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO users (name, email, age) VALUES (?, ?, ?)",
		"params":   `["Charlie", "charlie@example.com", 35]`,
	})
	printResult("SQLite: Insert Charlie", result, err)

	// Example 3: Query all users
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "query",
		"sql":      "SELECT * FROM users",
	})
	printResult("SQLite: Query all users", result, err)

	// Example 4: Query with parameters
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "query",
		"sql":      "SELECT * FROM users WHERE age > ?",
		"params":   `[28]`,
	})
	printResult("SQLite: Query users age > 28", result, err)

	// Example 5: Update data
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "UPDATE users SET age = ? WHERE name = ?",
		"params":   `[31, "Alice"]`,
	})
	printResult("SQLite: Update Alice's age", result, err)

	// Example 6: List tables
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "tables",
	})
	printResult("SQLite: List tables", result, err)

	// Example 7: Get table schema
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "schema",
		"sql":      "users",
	})
	printResult("SQLite: Get users schema", result, err)

	// Example 8: Create another table for relations
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql": `CREATE TABLE IF NOT EXISTS posts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER,
			title TEXT NOT NULL,
			content TEXT,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`,
	})
	printResult("SQLite: Create posts table", result, err)

	// Example 9: Insert posts
	tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO posts (user_id, title, content) VALUES (?, ?, ?)",
		"params":   `[1, "Hello World", "My first post!"]`,
	})
	tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO posts (user_id, title, content) VALUES (?, ?, ?)",
		"params":   `[1, "Second Post", "More content here"]`,
	})
	tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "INSERT INTO posts (user_id, title, content) VALUES (?, ?, ?)",
		"params":   `[2, "Bob's Post", "Bob writes too"]`,
	})

	// Example 10: JOIN query
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "query",
		"sql": `SELECT users.name, posts.title, posts.content 
		        FROM posts 
		        JOIN users ON posts.user_id = users.id`,
	})
	printResult("SQLite: JOIN users and posts", result, err)

	// Example 11: Aggregate query
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "query",
		"sql":      "SELECT name, (SELECT COUNT(*) FROM posts WHERE posts.user_id = users.id) as post_count FROM users",
	})
	printResult("SQLite: Count posts per user", result, err)

	// Example 12: Seed data using seed tool
	result, err = tools.Execute(ctx, "seed", map[string]any{
		"database": dbPath,
		"table":    "users",
		"data": `[
			{"name": "David", "email": "david@example.com", "age": 28},
			{"name": "Eve", "email": "eve@example.com", "age": 32}
		]`,
	})
	printResult("Seed: Add more users", result, err)

	// Verify seed
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "query",
		"sql":      "SELECT name, email FROM users ORDER BY id",
	})
	printResult("SQLite: All users after seed", result, err)

	// Example 13: Migrations
	migrationsDB := filepath.Join(tempDir, "migrations_example.db")
	defer os.Remove(migrationsDB)

	result, err = tools.Execute(ctx, "migrate", map[string]any{
		"database": migrationsDB,
		"migrations": `[
			"CREATE TABLE IF NOT EXISTS products (id INTEGER PRIMARY KEY, name TEXT)",
			"CREATE TABLE IF NOT EXISTS orders (id INTEGER PRIMARY KEY, product_id INTEGER)"
		]`,
		"direction": "up",
	})
	printResult("Migrate: Run migrations", result, err)

	// Check tables created
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": migrationsDB,
		"action":   "tables",
	})
	printResult("SQLite: Tables after migration", result, err)

	// Example 14: Backup database
	backupPath := filepath.Join(tempDir, "backup.db")
	defer os.Remove(backupPath)

	result, err = tools.Execute(ctx, "backup", map[string]any{
		"source": dbPath,
		"dest":   backupPath,
		"action": "backup",
	})
	printResult("Backup: Create backup", result, err)

	// Example 15: In-memory database
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": ":memory:",
		"action":   "execute",
		"sql":      "CREATE TABLE temp (id INTEGER, value TEXT)",
	})
	printResult("SQLite: In-memory database", result, err)

	// Example 16: Delete data
	result, err = tools.Execute(ctx, "sqlite", map[string]any{
		"database": dbPath,
		"action":   "execute",
		"sql":      "DELETE FROM users WHERE age < ?",
		"params":   `[30]`,
	})
	printResult("SQLite: Delete users age < 30", result, err)

	// The vector_db example is gone with the tool. It answered every query
	// with a stub result that reported success, which is the one thing an
	// example must never demonstrate. See PS-001.
}
