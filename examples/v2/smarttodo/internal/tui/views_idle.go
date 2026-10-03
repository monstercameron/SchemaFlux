package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) idleViewRender() string {
	pending := countPending(m.todos)
	overdue := 0
	urgent := 0
	now := time.Now()
	for _, todo := range m.todos {
		if todo.Completed || todo.Deadline == nil {
			continue
		}
		if todo.Deadline.Before(now) {
			overdue++
		} else if todo.Deadline.Sub(now) <= time.Hour {
			urgent++
		}
	}

	body := []string{
		fmt.Sprintf("Idle for %d minutes", int(time.Since(m.lastActivity).Minutes())),
		fmt.Sprintf("Open tasks: %d", pending),
		fmt.Sprintf("Overdue: %d", overdue),
		fmt.Sprintf("Due within the hour: %d", urgent),
	}
	if m.aiQuote != "" {
		body = append(body, "", truncateText(m.aiQuote, 64))
	}

	content := lipgloss.JoinVertical(
		lipgloss.Center,
		lipgloss.NewStyle().Foreground(textColor).Bold(true).Render("Idle summary"),
		lipgloss.NewStyle().Foreground(mutedColor).Render("Any key returns to the board."),
		"",
		renderPanel("Current state", strings.Join(body, "\n"), 56),
	)

	return renderViewport(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}
