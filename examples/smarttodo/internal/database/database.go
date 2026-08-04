//go:build !js

package database

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

type Database struct {
	db *sql.DB
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	d := &Database{db: db}
	if err := d.createTables(); err != nil {
		return nil, err
	}

	return d, nil
}

func (d *Database) createTables() error {
	// Create todos table
	todosQuery := `
	CREATE TABLE IF NOT EXISTS todos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		description TEXT,
		priority TEXT CHECK(priority IN ('high', 'medium', 'low')) DEFAULT 'medium',
		category TEXT,
		location TEXT DEFAULT 'home',
		deadline DATETIME,
		effort TEXT,
		dependencies TEXT,
		context TEXT,
		tasks TEXT DEFAULT '[]',
		completed BOOLEAN DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		cost REAL DEFAULT 0
	);
	`

	if _, err := d.db.Exec(todosQuery); err != nil {
		return err
	}

	// Create indexes
	indexQueries := []string{
		`CREATE INDEX IF NOT EXISTS idx_completed ON todos(completed)`,
		`CREATE INDEX IF NOT EXISTS idx_priority ON todos(priority)`,
		`CREATE INDEX IF NOT EXISTS idx_deadline ON todos(deadline)`,
	}

	for _, query := range indexQueries {
		if _, err := d.db.Exec(query); err != nil {
			return err
		}
	}

	// Create user preferences table
	prefsQuery := `
	CREATE TABLE IF NOT EXISTS user_prefs (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		user_name TEXT,
		list_title TEXT,
		last_deadline_update DATE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`

	_, err := d.db.Exec(prefsQuery)
	return err
}

func (d *Database) AddTodo(todo *models.SmartTodo) (int64, error) {
	query := `
	INSERT INTO todos (title, description, priority, category, location, deadline, effort, dependencies, context, tasks, cost)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	var deadlineStr *string
	if todo.Deadline != nil {
		s := todo.Deadline.Format(time.RFC3339)
		deadlineStr = &s
	}

	deps := ""
	if len(todo.Dependencies) > 0 {
		deps = joinStrings(todo.Dependencies, ",")
	}
	tasksJSON := "[]"
	if len(todo.Tasks) > 0 {
		tasksJSON = todo.TasksToJSON()
	}

	result, err := d.db.Exec(query,
		todo.Title,
		todo.Description,
		todo.Priority,
		todo.Category,
		todo.Location,
		deadlineStr,
		todo.Effort,
		deps,
		todo.Context,
		tasksJSON,
		todo.Cost,
	)

	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

func (d *Database) GetPendingTodos() ([]*models.SmartTodo, error) {
	query := `
	SELECT id, title, description, priority, category, deadline, effort, dependencies, context, tasks, created_at
	FROM todos
	WHERE completed = 0
	ORDER BY 
		CASE priority 
			WHEN 'high' THEN 1 
			WHEN 'medium' THEN 2 
			WHEN 'low' THEN 3 
		END,
		deadline ASC NULLS LAST,
		created_at DESC
	`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var todos []*models.SmartTodo
	for rows.Next() {
		todo := &models.SmartTodo{}
		var deadlineStr, depsStr, tasksStr sql.NullString
		var createdAt string

		err := rows.Scan(
			&todo.ID,
			&todo.Title,
			&todo.Description,
			&todo.Priority,
			&todo.Category,
			&deadlineStr,
			&todo.Effort,
			&depsStr,
			&todo.Context,
			&tasksStr,
			&createdAt,
		)
		if err != nil {
			return nil, err
		}

		if deadlineStr.Valid {
			deadline, _ := time.Parse(time.RFC3339, deadlineStr.String)
			todo.Deadline = &deadline
		}

		if depsStr.Valid && depsStr.String != "" {
			todo.Dependencies = splitString(depsStr.String, ",")
		}

		if tasksStr.Valid && tasksStr.String != "" && tasksStr.String != "[]" {
			todo.TasksFromJSON(tasksStr.String)
		}

		todo.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		todos = append(todos, todo)
	}

	return todos, nil
}

func (d *Database) GetAllTodos() ([]*models.SmartTodo, error) {
	query := `
	SELECT id, title, description, priority, category, deadline, effort, dependencies, context, tasks, completed, created_at, completed_at
	FROM todos
	ORDER BY 
		completed ASC,  -- Uncompleted (0) first, then completed (1)
		CASE 
			WHEN completed = 0 THEN 
				CASE priority
					WHEN 'high' THEN 1
					WHEN 'medium' THEN 2
					WHEN 'low' THEN 3
					ELSE 4
				END
			ELSE 999  -- Completed items go to the end
		END,
		created_at DESC
	`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var todos []*models.SmartTodo
	for rows.Next() {
		todo := &models.SmartTodo{}
		var deadlineStr, depsStr, tasksStr, completedAtStr sql.NullString
		var createdAt string

		err := rows.Scan(
			&todo.ID,
			&todo.Title,
			&todo.Description,
			&todo.Priority,
			&todo.Category,
			&deadlineStr,
			&todo.Effort,
			&depsStr,
			&todo.Context,
			&tasksStr,
			&todo.Completed,
			&createdAt,
			&completedAtStr,
		)
		if err != nil {
			return nil, err
		}

		if deadlineStr.Valid {
			deadline, _ := time.Parse(time.RFC3339, deadlineStr.String)
			todo.Deadline = &deadline
		}

		if depsStr.Valid && depsStr.String != "" {
			todo.Dependencies = splitString(depsStr.String, ",")
		}

		if tasksStr.Valid && tasksStr.String != "" && tasksStr.String != "[]" {
			todo.TasksFromJSON(tasksStr.String)
		}

		todo.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		todos = append(todos, todo)
	}

	return todos, nil
}

func (d *Database) CompleteTodo(id int) error {
	query := `UPDATE todos SET completed = 1, completed_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := d.db.Exec(query, id)
	return err
}

func (d *Database) UncompleteTodo(id int) error {
	query := `UPDATE todos SET completed = 0, completed_at = NULL WHERE id = ?`
	_, err := d.db.Exec(query, id)
	return err
}

func (d *Database) DeleteTodo(id int) error {
	query := `DELETE FROM todos WHERE id = ?`
	_, err := d.db.Exec(query, id)
	return err
}

func (d *Database) UpdateDeadline(id string, newDeadline *time.Time) error {
	var deadlineStr *string
	if newDeadline != nil {
		s := newDeadline.Format(time.RFC3339)
		deadlineStr = &s
	}

	query := `UPDATE todos SET deadline = ? WHERE id = ?`
	idInt, _ := strconv.Atoi(id)
	_, err := d.db.Exec(query, deadlineStr, idInt)
	return err
}

func (d *Database) UpdateTodo(todo *models.SmartTodo) error {
	query := `
	UPDATE todos 
	SET title = ?, description = ?, priority = ?, category = ?, 
	    deadline = ?, effort = ?, dependencies = ?, context = ?, tasks = ?
	WHERE id = ?
	`

	var deadlineStr *string
	if todo.Deadline != nil {
		s := todo.Deadline.Format(time.RFC3339)
		deadlineStr = &s
	}

	deps := ""
	if len(todo.Dependencies) > 0 {
		deps = joinStrings(todo.Dependencies, ",")
	}

	tasksJSON := "[]"
	if len(todo.Tasks) > 0 {
		tasksJSON = todo.TasksToJSON()
	}

	_, err := d.db.Exec(query,
		todo.Title,
		todo.Description,
		todo.Priority,
		todo.Category,
		deadlineStr,
		todo.Effort,
		deps,
		todo.Context,
		tasksJSON,
		todo.ID,
	)

	return err
}

func (d *Database) GetTodoByID(id int) (*models.SmartTodo, error) {
	query := `
	SELECT id, title, description, priority, category, deadline, effort, dependencies, context, tasks, completed, created_at
	FROM todos
	WHERE id = ?
	`

	todo := &models.SmartTodo{}
	var deadlineStr, depsStr, tasksStr sql.NullString
	var createdAt string

	err := d.db.QueryRow(query, id).Scan(
		&todo.ID,
		&todo.Title,
		&todo.Description,
		&todo.Priority,
		&todo.Category,
		&deadlineStr,
		&todo.Effort,
		&depsStr,
		&todo.Context,
		&tasksStr,
		&todo.Completed,
		&createdAt,
	)

	if err != nil {
		return nil, err
	}

	if deadlineStr.Valid {
		deadline, _ := time.Parse(time.RFC3339, deadlineStr.String)
		todo.Deadline = &deadline
	}

	if depsStr.Valid && depsStr.String != "" {
		todo.Dependencies = splitString(depsStr.String, ",")
	}

	if tasksStr.Valid && tasksStr.String != "" && tasksStr.String != "[]" {
		todo.TasksFromJSON(tasksStr.String)
	}

	todo.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return todo, nil
}

func (d *Database) GetStats() (map[string]int, error) {
	stats := make(map[string]int)

	// Total todos
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM todos").Scan(&total)
	if err != nil {
		return nil, err
	}
	stats["total"] = total

	// Completed todos
	var completed int
	err = d.db.QueryRow("SELECT COUNT(*) FROM todos WHERE completed = 1").Scan(&completed)
	if err != nil {
		return nil, err
	}
	stats["completed"] = completed

	// Pending todos
	stats["pending"] = total - completed

	// By priority
	rows, err := d.db.Query(`
		SELECT priority, COUNT(*) 
		FROM todos 
		WHERE completed = 0 
		GROUP BY priority
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var priority string
		var count int
		if err := rows.Scan(&priority, &count); err != nil {
			continue
		}
		stats[priority] = count
	}

	// Overdue
	var overdue int
	err = d.db.QueryRow(`
		SELECT COUNT(*) 
		FROM todos 
		WHERE completed = 0 AND deadline < datetime('now')
	`).Scan(&overdue)
	if err != nil {
		return nil, err
	}
	stats["overdue"] = overdue

	return stats, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

// User preferences methods
func (d *Database) GetUserPrefs() (string, string, error) {
	query := `SELECT user_name, list_title FROM user_prefs WHERE id = 1`

	var userName, listTitle sql.NullString
	err := d.db.QueryRow(query).Scan(&userName, &listTitle)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}

	return userName.String, listTitle.String, nil
}

// Check if deadlines need updating (once per day)
func (d *Database) NeedsDeadlineUpdate() (bool, error) {
	query := `SELECT last_deadline_update FROM user_prefs WHERE id = 1`

	var lastUpdate sql.NullString
	err := d.db.QueryRow(query).Scan(&lastUpdate)
	if err == sql.ErrNoRows {
		// No prefs yet, needs update
		return true, nil
	}
	if err != nil {
		return false, err
	}

	if !lastUpdate.Valid {
		// Never updated
		return true, nil
	}

	// Parse the date and check if it's today
	lastUpdateTime, err := time.Parse("2006-01-02", lastUpdate.String)
	if err != nil {
		return true, nil // If we can't parse, update to be safe
	}

	// Check if it's a new day
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	lastUpdateDay := time.Date(lastUpdateTime.Year(), lastUpdateTime.Month(), lastUpdateTime.Day(), 0, 0, 0, 0, lastUpdateTime.Location())

	return !today.Equal(lastUpdateDay), nil
}

// Mark deadlines as updated
func (d *Database) MarkDeadlinesUpdated() error {
	query := `
	INSERT INTO user_prefs (id, last_deadline_update) 
	VALUES (1, DATE('now'))
	ON CONFLICT(id) DO UPDATE SET
		last_deadline_update = DATE('now'),
		updated_at = CURRENT_TIMESTAMP
	`

	_, err := d.db.Exec(query)
	return err
}

// Update a specific task's completion status
func (d *Database) UpdateTaskStatus(todoID string, taskIndex int, completed bool) error {
	idInt, err := strconv.Atoi(todoID)
	if err != nil {
		return err
	}

	// Get the todo first
	todo, err := d.GetTodoByID(idInt)
	if err != nil {
		return err
	}

	// Update the task status
	if taskIndex >= 0 && taskIndex < len(todo.Tasks) {
		todo.Tasks[taskIndex].Completed = completed
		// Update the todo with new tasks
		return d.UpdateTodo(todo)
	}

	return fmt.Errorf("task index out of range")
}

// Get todos with relative deadlines that need updating
func (d *Database) GetTodosForDeadlineUpdate() ([]*models.SmartTodo, error) {
	// Get all incomplete todos with deadlines
	query := `
	SELECT id, title, description, priority, category, deadline, effort, dependencies, context, tasks, created_at
	FROM todos
	WHERE completed = 0 AND deadline IS NOT NULL
	ORDER BY created_at DESC
	`

	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var todos []*models.SmartTodo
	for rows.Next() {
		todo := &models.SmartTodo{}
		var deadlineStr, depsStr, tasksStr sql.NullString
		var createdAt string

		err := rows.Scan(
			&todo.ID,
			&todo.Title,
			&todo.Description,
			&todo.Priority,
			&todo.Category,
			&deadlineStr,
			&todo.Effort,
			&depsStr,
			&todo.Context,
			&tasksStr,
			&createdAt,
		)
		if err != nil {
			return nil, err
		}

		if deadlineStr.Valid {
			deadline, _ := time.Parse(time.RFC3339, deadlineStr.String)
			todo.Deadline = &deadline
		}

		if depsStr.Valid && depsStr.String != "" {
			todo.Dependencies = splitString(depsStr.String, ",")
		}

		todo.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		todos = append(todos, todo)
	}

	return todos, nil
}

func (d *Database) SaveUserPrefs(userName, listTitle string) error {
	query := `
	INSERT INTO user_prefs (id, user_name, list_title) 
	VALUES (1, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		user_name = excluded.user_name,
		list_title = excluded.list_title,
		updated_at = CURRENT_TIMESTAMP
	`

	_, err := d.db.Exec(query, userName, listTitle)
	return err
}

// Helper functions
func joinStrings(strs []string, sep string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

func splitString(str, sep string) []string {
	if str == "" {
		return []string{}
	}
	result := []string{}
	start := 0
	for i := 0; i < len(str); i++ {
		if i+len(sep) <= len(str) && str[i:i+len(sep)] == sep {
			result = append(result, str[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	if start < len(str) {
		result = append(result, str[start:])
	}
	return result
}
