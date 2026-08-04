package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

type todoItem struct {
	todo *models.SmartTodo
}

func (t todoItem) Title() string {
	check := "[ ]"
	if t.todo.Completed {
		check = "[x]"
	}
	priority := strings.ToUpper(strings.TrimSpace(t.todo.Priority))
	if priority == "" {
		priority = "OPEN"
	}
	return fmt.Sprintf("%s %s %s", check, priority, truncateText(t.todo.Title, 64))
}

func (t todoItem) Description() string {
	meta := []string{}
	if t.todo.Category != "" && t.todo.Category != "pending" {
		meta = append(meta, getCategoryEmoji(strings.ToLower(t.todo.Category)))
	}
	if t.todo.Effort != "" {
		meta = append(meta, "effort "+getEffortEmoji(strings.ToLower(t.todo.Effort)))
	}
	if t.todo.Deadline != nil {
		meta = append(meta, renderDeadlineLabel(t.todo.Deadline))
	}
	if len(t.todo.Dependencies) > 0 {
		meta = append(meta, fmt.Sprintf("deps %d", len(t.todo.Dependencies)))
	}

	lines := []string{}
	if len(meta) > 0 {
		lines = append(lines, strings.Join(meta, " | "))
	}
	if len(t.todo.Tasks) > 0 {
		completed := 0
		preview := []string{}
		for _, task := range t.todo.Tasks {
			if task.Completed {
				completed++
			}
			if len(preview) < 2 {
				prefix := "-"
				if task.Completed {
					prefix = "x"
				}
				preview = append(preview, fmt.Sprintf("%s %s", prefix, truncateText(task.Text, 56)))
			}
		}
		lines = append(lines, fmt.Sprintf("subtasks %d/%d", completed, len(t.todo.Tasks)))
		lines = append(lines, preview...)
	}
	return strings.Join(lines, "\n")
}

func (t todoItem) FilterValue() string { return t.todo.Title }

func renderDeadlineLabel(deadline *time.Time) string {
	if deadline == nil {
		return ""
	}
	daysUntil := int(time.Until(*deadline).Hours() / 24)
	switch {
	case daysUntil < 0:
		return fmt.Sprintf("overdue %dd", -daysUntil)
	case daysUntil == 0:
		return "due today"
	case daysUntil == 1:
		return "due tomorrow"
	case daysUntil <= 7:
		return fmt.Sprintf("due in %dd", daysUntil)
	default:
		return deadline.Format("Jan 2")
	}
}
