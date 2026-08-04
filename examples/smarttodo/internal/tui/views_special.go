package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/localization"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

func (m Model) splashViewRender() string {
	title := localization.AppName
	subtitle := "AI-assisted capture, prioritization, and review for a serious working queue."
	if m.userName != "" {
		subtitle = fmt.Sprintf("Welcome back, %s. %s", m.userName, fallbackLabel(m.listTitle, "Your board is ready."))
	}

	metrics := []string{
		renderMetricCard("Pending", fmt.Sprintf("%d", countPending(m.todos)), "Open work in your queue", primaryColor, 24),
		renderMetricCard("Due today", fmt.Sprintf("%d", countDueToday(m.todos)), "Tasks that need attention now", warningColor, 24),
		renderMetricCard("Completed", fmt.Sprintf("%d", countCompletedToday(m.todos)), "Finished today", successColor, 24),
	}

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		lipgloss.NewStyle().Foreground(textColor).Bold(true).Render(title),
		lipgloss.NewStyle().Foreground(mutedColor).Render(subtitle),
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, metrics...),
		"",
		renderStatusLine(48, "info", "Press Enter to continue."),
	)

	card := renderPanel("Overview", body, 82)
	return renderViewport(m.width, m.height, lipgloss.Center, lipgloss.Center, card)
}

func (m Model) setupViewRender() string {
	prompt := "Enter your name to personalize the board."
	if m.userName != "" {
		prompt = fmt.Sprintf("Name the board for %s.", m.userName)
	}

	input := borderStyle.Width(56).Render(m.setupInput.View())
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.NewStyle().Foreground(textColor).Bold(true).Render("Initial setup"),
		lipgloss.NewStyle().Foreground(mutedColor).Render(prompt),
		"",
		input,
		"",
		renderStatusLine(56, "info", "Press Enter to continue."),
	)

	return renderViewport(m.width, m.height, lipgloss.Center, lipgloss.Center, renderPanel("Welcome", body, 66))
}

func (m Model) closingViewRender() string {
	completed := 0
	pending := 0
	for _, todo := range m.todos {
		if todo.Completed {
			completed++
		} else {
			pending++
		}
	}

	messages := []string{"Saving state", "Closing database", "Cleaning up", "Done"}
	idx := clamp(m.closingProgress/25, 0, len(messages)-1)

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		lipgloss.NewStyle().Foreground(textColor).Bold(true).Render("Closing " + localization.AppName),
		lipgloss.NewStyle().Foreground(mutedColor).Render(messages[idx]),
		"",
		renderProgressBar(m.closingProgress),
		"",
		fmt.Sprintf("Completed: %d", completed),
		fmt.Sprintf("Still open: %d", pending),
	)

	return renderViewport(m.width, m.height, lipgloss.Center, lipgloss.Center, renderPanel("Session summary", body, 56))
}

func (m Model) statsViewRender() string {
	total := m.stats["total"]
	completed := m.stats["completed"]
	pending := m.stats["pending"]
	overdue := m.stats["overdue"]
	rate := 0
	if total > 0 {
		rate = (completed * 100) / total
	}

	left := renderPanel("Totals", strings.Join([]string{
		fmt.Sprintf("Total tasks: %d", total),
		fmt.Sprintf("Completed: %d", completed),
		fmt.Sprintf("Pending: %d", pending),
		fmt.Sprintf("Overdue: %d", overdue),
		"",
		renderProgressBar(rate),
	}, "\n"), 38)

	fastCalls := 0
	smartCalls := 0
	totalCost := 0.0
	if m.processor != nil {
		fastCalls = m.processor.FastCalls
		smartCalls = m.processor.SmartCalls
		totalCost = m.processor.TotalCost
	}

	right := renderPanel("AI usage", strings.Join([]string{
		fmt.Sprintf("Fast calls: %d", fastCalls),
		fmt.Sprintf("Smart calls: %d", smartCalls),
		fmt.Sprintf("Session cost: $%.4f", totalCost),
		"",
		fmt.Sprintf("High priority: %d", m.stats["high"]),
		fmt.Sprintf("Medium priority: %d", m.stats["medium"]),
		fmt.Sprintf("Low priority: %d", m.stats["low"]),
	}, "\n"), 38)

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		renderShellHeader(m.width, "Statistics", "Operational snapshot of your board", []string{lipgloss.NewStyle().Foreground(mutedColor).Render(time.Now().Format("15:04"))}),
		lipgloss.JoinHorizontal(lipgloss.Top, left, right),
		renderStatusLine(m.width-2, "info", "Esc returns to the board."),
	)
	return renderViewport(
		m.width,
		m.height,
		lipgloss.Left,
		lipgloss.Top,
		lipgloss.NewStyle().Padding(1, 1).Render(content),
	)
}

func countCompletedToday(todos []*models.SmartTodo) int {
	count := 0
	now := time.Now()
	for _, todo := range todos {
		if todo.Completed && sameDay(todo.CreatedAt, now) {
			count++
		}
	}
	return count
}

func countDueToday(todos []*models.SmartTodo) int {
	count := 0
	now := time.Now()
	for _, todo := range todos {
		if todo.Deadline != nil && !todo.Completed && sameDay(*todo.Deadline, now) {
			count++
		}
	}
	return count
}

func countPending(todos []*models.SmartTodo) int {
	count := 0
	for _, todo := range todos {
		if !todo.Completed {
			count++
		}
	}
	return count
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
