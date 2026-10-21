package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) detailViewRender() string {
	if m.selectedTodo == nil {
		return m.listViewRender()
	}

	todo := m.selectedTodo
	header := renderShellHeader(
		m.width,
		"Task detail",
		truncateText(todo.Title, 60),
		[]string{
			renderPriorityLabel(todo.Priority),
			lipgloss.NewStyle().Foreground(mutedColor).Render(stripANSI(renderDeadlineSummary(todo.Deadline))),
		},
	)

	mainLines := []string{
		fmt.Sprintf("Category: %s", getCategoryEmoji(strings.ToLower(todo.Category))),
		fmt.Sprintf("Effort: %s", getEffortEmoji(strings.ToLower(todo.Effort))),
		fmt.Sprintf("Location: %s", renderLocationSummary(todo.Context, todo.Location)),
		fmt.Sprintf("Created: %s", todo.CreatedAt.Format("Jan 2 2006 3:04 PM")),
	}
	if todo.Description != "" {
		mainLines = append(mainLines, "", "Description:", todo.Description)
	}
	if todo.Context != "" {
		mainLines = append(mainLines, "", "Notes:", todo.Context)
	}
	if len(todo.Dependencies) > 0 {
		mainLines = append(mainLines, "", "Dependencies:", "- "+strings.Join(todo.Dependencies, "\n- "))
	}

	left := renderPanel("Overview", strings.Join(mainLines, "\n"), clamp((m.width/2)-4, 36, 70))

	taskBody := "No subtasks yet"
	if len(todo.Tasks) > 0 {
		completed := 0
		rows := []string{}
		for _, task := range todo.Tasks {
			marker := "[ ]"
			text := task.Text
			if task.Completed {
				marker = "[x]"
				completed++
				text = stripANSI(formatCompletedText(text, true))
			}
			rows = append(rows, fmt.Sprintf("%s %s", marker, text))
		}
		taskBody = fmt.Sprintf("Progress %s\n\n%s", renderProgressBar((completed*100)/len(todo.Tasks)), strings.Join(rows, "\n"))
	}
	right := renderPanel("Subtasks", taskBody, clamp((m.width/2)-4, 36, 70))

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		lipgloss.JoinHorizontal(lipgloss.Top, left, right),
		renderStatusLine(m.width-2, "info", "Esc or Enter returns to the board."),
	)

	return renderViewport(
		m.width,
		m.height,
		lipgloss.Left,
		lipgloss.Top,
		lipgloss.NewStyle().Padding(1, 1).Render(content),
	)
}
