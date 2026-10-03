// todo.go - Core todo data model
package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Task represents a subtask within a todo
type Task struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}

// SmartTodo represents an AI-enhanced todo item
type SmartTodo struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Priority     string     `json:"priority"` // high, medium, low
	Category     string     `json:"category"` // work, personal, urgent, etc.
	Location     string     `json:"location"` // Where task should be done
	Deadline     *time.Time `json:"deadline"`
	Effort       string     `json:"effort"`       // minimal, low, medium, high, massive
	Dependencies []string   `json:"dependencies"` // IDs of todos this depends on
	Context      string     `json:"context"`      // AI-generated context advice
	Tasks        []Task     `json:"tasks"`        // Subtasks with completion status
	Completed    bool       `json:"completed"`
	CreatedAt    time.Time  `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	Cost         float64    `json:"cost"` // API cost for this todo
}

// TasksToJSON serializes tasks to JSON string for database storage
func (t *SmartTodo) TasksToJSON() string {
	if len(t.Tasks) == 0 {
		return "[]"
	}

	data, err := json.Marshal(t.Tasks)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// TasksFromJSON deserializes tasks from JSON string
func (t *SmartTodo) TasksFromJSON(jsonStr string) error {
	t.Tasks = []Task{}
	if jsonStr == "" || jsonStr == "[]" {
		return nil
	}

	return json.Unmarshal([]byte(jsonStr), &t.Tasks)
}

// TaskCompletionPercent returns the percentage of completed subtasks
func (t *SmartTodo) TaskCompletionPercent() int {
	if len(t.Tasks) == 0 {
		return 0
	}

	completed := 0
	for _, task := range t.Tasks {
		if task.Completed {
			completed++
		}
	}

	return (completed * 100) / len(t.Tasks)
}

// IsOverdue checks if the todo is past its deadline
func (t *SmartTodo) IsOverdue() bool {
	if t.Deadline == nil || t.Completed {
		return false
	}
	return t.Deadline.Before(time.Now())
}

// DaysUntilDeadline returns days until deadline (negative if overdue)
func (t *SmartTodo) DaysUntilDeadline() int {
	if t.Deadline == nil {
		return 999999 // No deadline
	}
	duration := time.Until(*t.Deadline)
	return int(duration.Hours() / 24)
}

// GetPriorityWeight returns a numeric weight for sorting
func (t *SmartTodo) GetPriorityWeight() int {
	switch t.Priority {
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

// GetEffortMinutes returns estimated minutes for the effort level
func (t *SmartTodo) GetEffortMinutes() int {
	switch strings.ToLower(t.Effort) {
	case "minimal":
		return 5
	case "low":
		return 30
	case "medium":
		return 90
	case "high":
		return 180
	case "massive":
		return 300
	default:
		return 60
	}
}

// String returns a string representation of the todo
func (t *SmartTodo) String() string {
	status := "[ ]"
	if t.Completed {
		status = "[✓]"
	}

	deadline := ""
	if t.Deadline != nil {
		deadline = fmt.Sprintf(" (Due: %s)", t.Deadline.Format("Jan 2"))
	}

	return fmt.Sprintf("%s %s - %s%s", status, t.Title, t.Priority, deadline)
}
