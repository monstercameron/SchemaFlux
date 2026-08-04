//go:build js && wasm

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall/js"
	"time"

	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

const browserDBStorageKey = "smarttodo:browser-db"

type browserState struct {
	NextID             int                 `json:"next_id"`
	Todos              []*models.SmartTodo `json:"todos"`
	UserName           string              `json:"user_name"`
	ListTitle          string              `json:"list_title"`
	LastDeadlineUpdate string              `json:"last_deadline_update"`
}

type Database struct {
	state browserState
}

func NewDatabase(dbPath string) (*Database, error) {
	d := &Database{
		state: browserState{
			NextID: 1,
			Todos:  []*models.SmartTodo{},
		},
	}
	if err := d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Database) AddTodo(todo *models.SmartTodo) (int64, error) {
	id := d.state.NextID
	if id <= 0 {
		id = inferNextID(d.state.Todos)
	}
	d.state.NextID = id + 1
	copy := cloneTodo(todo)
	copy.ID = strconv.Itoa(id)
	if copy.CreatedAt.IsZero() {
		copy.CreatedAt = time.Now()
	}
	d.state.Todos = append([]*models.SmartTodo{copy}, d.state.Todos...)
	return int64(id), d.save()
}

func (d *Database) GetPendingTodos() ([]*models.SmartTodo, error) {
	todos := []*models.SmartTodo{}
	for _, todo := range d.state.Todos {
		if !todo.Completed {
			todos = append(todos, cloneTodo(todo))
		}
	}
	sortTodos(todos)
	return todos, nil
}

func (d *Database) GetAllTodos() ([]*models.SmartTodo, error) {
	todos := cloneTodos(d.state.Todos)
	sortTodos(todos)
	return todos, nil
}

func (d *Database) CompleteTodo(id int) error {
	todo, err := d.todoByIntID(id)
	if err != nil {
		return err
	}
	now := time.Now()
	todo.Completed = true
	todo.CompletedAt = &now
	return d.save()
}

func (d *Database) UncompleteTodo(id int) error {
	todo, err := d.todoByIntID(id)
	if err != nil {
		return err
	}
	todo.Completed = false
	todo.CompletedAt = nil
	return d.save()
}

func (d *Database) DeleteTodo(id int) error {
	idStr := strconv.Itoa(id)
	for i, todo := range d.state.Todos {
		if todo.ID == idStr {
			d.state.Todos = append(d.state.Todos[:i], d.state.Todos[i+1:]...)
			return d.save()
		}
	}
	return fmt.Errorf("todo not found: %d", id)
}

func (d *Database) UpdateDeadline(id string, newDeadline *time.Time) error {
	todo := d.todoByStringID(id)
	if todo == nil {
		return fmt.Errorf("todo not found: %s", id)
	}
	todo.Deadline = newDeadline
	return d.save()
}

func (d *Database) UpdateTodo(todo *models.SmartTodo) error {
	if todo == nil {
		return fmt.Errorf("todo cannot be nil")
	}
	for i, existing := range d.state.Todos {
		if existing.ID == todo.ID {
			d.state.Todos[i] = cloneTodo(todo)
			return d.save()
		}
	}
	return fmt.Errorf("todo not found: %s", todo.ID)
}

func (d *Database) GetTodoByID(id int) (*models.SmartTodo, error) {
	todo, err := d.todoByIntID(id)
	if err != nil {
		return nil, err
	}
	return cloneTodo(todo), nil
}

func (d *Database) GetStats() (map[string]int, error) {
	stats := map[string]int{
		"total":     len(d.state.Todos),
		"completed": 0,
		"pending":   0,
		"high":      0,
		"medium":    0,
		"low":       0,
		"overdue":   0,
	}
	for _, todo := range d.state.Todos {
		if todo.Completed {
			stats["completed"]++
		} else {
			stats["pending"]++
			switch strings.ToLower(todo.Priority) {
			case "high":
				stats["high"]++
			case "medium":
				stats["medium"]++
			case "low":
				stats["low"]++
			}
		}
		if todo.IsOverdue() {
			stats["overdue"]++
		}
	}
	return stats, nil
}

func (d *Database) Close() error {
	return d.save()
}

func (d *Database) GetUserPrefs() (string, string, error) {
	return d.state.UserName, d.state.ListTitle, nil
}

func (d *Database) NeedsDeadlineUpdate() (bool, error) {
	if d.state.LastDeadlineUpdate == "" {
		return true, nil
	}
	lastUpdate, err := time.Parse("2006-01-02", d.state.LastDeadlineUpdate)
	if err != nil {
		return true, nil
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	lastDay := time.Date(lastUpdate.Year(), lastUpdate.Month(), lastUpdate.Day(), 0, 0, 0, 0, lastUpdate.Location())
	return !today.Equal(lastDay), nil
}

func (d *Database) MarkDeadlinesUpdated() error {
	d.state.LastDeadlineUpdate = time.Now().Format("2006-01-02")
	return d.save()
}

func (d *Database) UpdateTaskStatus(todoID string, taskIndex int, completed bool) error {
	todo := d.todoByStringID(todoID)
	if todo == nil {
		return fmt.Errorf("todo not found: %s", todoID)
	}
	if taskIndex < 0 || taskIndex >= len(todo.Tasks) {
		return fmt.Errorf("task index out of range")
	}
	todo.Tasks[taskIndex].Completed = completed
	return d.save()
}

func (d *Database) GetTodosForDeadlineUpdate() ([]*models.SmartTodo, error) {
	todos := []*models.SmartTodo{}
	for _, todo := range d.state.Todos {
		if !todo.Completed && todo.Deadline != nil {
			todos = append(todos, cloneTodo(todo))
		}
	}
	sortTodos(todos)
	return todos, nil
}

func (d *Database) SaveUserPrefs(userName, listTitle string) error {
	d.state.UserName = userName
	d.state.ListTitle = listTitle
	return d.save()
}

func (d *Database) load() error {
	storage := js.Global().Get("localStorage")
	if storage.IsUndefined() || storage.IsNull() {
		return nil
	}
	raw := storage.Call("getItem", browserDBStorageKey)
	if raw.IsNull() || raw.IsUndefined() {
		return nil
	}
	value := raw.String()
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(value), &d.state); err != nil {
		return err
	}
	if d.state.NextID <= 0 {
		d.state.NextID = inferNextID(d.state.Todos)
	}
	if d.state.Todos == nil {
		d.state.Todos = []*models.SmartTodo{}
	}
	return nil
}

func (d *Database) save() error {
	storage := js.Global().Get("localStorage")
	if storage.IsUndefined() || storage.IsNull() {
		return nil
	}
	data, err := json.Marshal(d.state)
	if err != nil {
		return err
	}
	storage.Call("setItem", browserDBStorageKey, string(data))
	return nil
}

func (d *Database) todoByIntID(id int) (*models.SmartTodo, error) {
	idStr := strconv.Itoa(id)
	todo := d.todoByStringID(idStr)
	if todo == nil {
		return nil, fmt.Errorf("todo not found: %d", id)
	}
	return todo, nil
}

func (d *Database) todoByStringID(id string) *models.SmartTodo {
	for _, todo := range d.state.Todos {
		if todo.ID == id {
			return todo
		}
	}
	return nil
}

func cloneTodo(todo *models.SmartTodo) *models.SmartTodo {
	if todo == nil {
		return nil
	}
	copy := *todo
	if todo.Dependencies != nil {
		copy.Dependencies = append([]string(nil), todo.Dependencies...)
	}
	if todo.Tasks != nil {
		copy.Tasks = append([]models.Task(nil), todo.Tasks...)
	}
	return &copy
}

func cloneTodos(todos []*models.SmartTodo) []*models.SmartTodo {
	cloned := make([]*models.SmartTodo, 0, len(todos))
	for _, todo := range todos {
		cloned = append(cloned, cloneTodo(todo))
	}
	return cloned
}

func inferNextID(todos []*models.SmartTodo) int {
	maxID := 0
	for _, todo := range todos {
		id, err := strconv.Atoi(todo.ID)
		if err == nil && id > maxID {
			maxID = id
		}
	}
	return maxID + 1
}

func sortTodos(todos []*models.SmartTodo) {
	sort.SliceStable(todos, func(i, j int) bool {
		if todos[i].Completed != todos[j].Completed {
			return !todos[i].Completed
		}
		if !todos[i].Completed && !todos[j].Completed {
			pi := priorityWeight(todos[i].Priority)
			pj := priorityWeight(todos[j].Priority)
			if pi != pj {
				return pi > pj
			}
			if todos[i].Deadline != nil && todos[j].Deadline != nil {
				return todos[i].Deadline.Before(*todos[j].Deadline)
			}
			if todos[i].Deadline != nil {
				return true
			}
			if todos[j].Deadline != nil {
				return false
			}
		}
		return todos[i].CreatedAt.After(todos[j].CreatedAt)
	})
}

func priorityWeight(priority string) int {
	switch strings.ToLower(priority) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}
