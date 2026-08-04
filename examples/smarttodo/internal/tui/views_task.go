package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

func (m Model) taskViewRender() string {
	if m.selectedTodo == nil {
		return m.listViewRender()
	}

	background := m.listViewRender()
	todo := m.selectedTodo
	modalWidth := clamp(m.width-8, 54, 82)

	progressSection := renderPanel("Progress", renderTaskProgress(todo), modalWidth-8)
	tasksSection := renderPanel("Subtasks", renderTaskList(todo, m.selectedTask, modalWidth-12), modalWidth-8)

	parts := []string{
		progressSection,
		"",
		tasksSection,
	}

	if m.taskInputMode {
		m.taskInput.Width = modalWidth - 10
		parts = append(parts,
			"",
			renderPanel("Add subtask", strings.Join([]string{
				m.taskInput.View(),
				"Enter adds the subtask. Esc cancels input mode.",
			}, "\n"), modalWidth-8),
		)
	}

	parts = append(parts,
		"",
		lipgloss.NewStyle().Foreground(mutedColor).Render("Up/down navigate. Space toggles. Ctrl+A adds. Ctrl+D deletes. Esc returns."),
	)

	modal := createModalBox("Subtasks | "+truncateText(todo.Title, modalWidth-20), strings.Join(parts, "\n"), modalWidth, primaryColor)
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	return renderModalOverlay(background, centered, m.width, m.height)
}

func renderTaskProgress(todo *models.SmartTodo) string {
	if len(todo.Tasks) == 0 {
		return "No subtasks yet. Use Ctrl+A to add the first one."
	}
	completed := 0
	for _, task := range todo.Tasks {
		if task.Completed {
			completed++
		}
	}
	return strings.Join([]string{
		fmt.Sprintf("Completed %d of %d", completed, len(todo.Tasks)),
		renderProgressBar((completed * 100) / len(todo.Tasks)),
	}, "\n")
}

func renderTaskList(todo *models.SmartTodo, selected int, width int) string {
	if len(todo.Tasks) == 0 {
		return lipgloss.NewStyle().Foreground(mutedColor).Render("No subtasks to display")
	}

	type indexedTask struct {
		task  models.Task
		index int
	}

	ordered := make([]indexedTask, 0, len(todo.Tasks))
	for i, task := range todo.Tasks {
		if !task.Completed {
			ordered = append(ordered, indexedTask{task: task, index: i})
		}
	}
	for i, task := range todo.Tasks {
		if task.Completed {
			ordered = append(ordered, indexedTask{task: task, index: i})
		}
	}

	rows := make([]string, 0, len(ordered))
	for _, entry := range ordered {
		prefix := "  "
		marker := "[ ]"
		style := lipgloss.NewStyle().Foreground(textColor)
		if entry.task.Completed {
			marker = "[x]"
			style = lipgloss.NewStyle().Foreground(mutedColor).Strikethrough(true)
		}
		if entry.index == selected {
			prefix = "> "
			style = style.Bold(true).Foreground(primaryColor)
		}
		rows = append(rows, fmt.Sprintf("%s%s %s", prefix, marker, style.Render(truncateText(entry.task.Text, width))))
	}
	return strings.Join(rows, "\n")
}
