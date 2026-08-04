package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

const panelChromeWidth = 6

func renderViewport(width, height int, horizontal, vertical lipgloss.Position, content string) string {
	if width <= 0 || height <= 0 {
		return content
	}

	placed := lipgloss.Place(width, height, horizontal, vertical, content)
	return lipgloss.NewStyle().
		Background(canvasColor).
		Foreground(textColor).
		Width(width).
		Height(height).
		Render(placed)
}

func formatCompletedText(text string, completed bool) string {
	if !completed {
		return text
	}
	return lipgloss.NewStyle().Strikethrough(true).Foreground(mutedColor).Render(text)
}

func (m *Model) addLog(msg string) {
	timestamp := time.Now().Format("15:04:05")
	logEntry := fmt.Sprintf("[%s] %s", timestamp, msg)
	m.consoleLogs = append(m.consoleLogs, logEntry)
	if len(m.consoleLogs) > m.maxLogs {
		m.consoleLogs = m.consoleLogs[len(m.consoleLogs)-m.maxLogs:]
	}
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func renderTag(value string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Background(surfaceAltColor).
		Padding(0, 1).
		Bold(true).
		Render(strings.ToUpper(value))
}

func renderPanel(title, body string, width int) string {
	if width <= 0 {
		width = lipgloss.Width(body) + panelChromeWidth
	}
	innerWidth := max(1, width-panelChromeWidth)
	head := lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		MarginBottom(1).
		Render(title)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(surfaceColor).
		Padding(1, 2).
		Width(innerWidth).
		Render(lipgloss.JoinVertical(lipgloss.Left, head, body))
}

func renderMetricCard(label, value, note string, tone lipgloss.Color, width int) string {
	if width <= 0 {
		width = 24
	}
	innerWidth := max(1, width-panelChromeWidth)
	content := []string{
		lipgloss.NewStyle().Foreground(mutedColor).Render(strings.ToUpper(label)),
		lipgloss.NewStyle().Foreground(tone).Bold(true).Render(value),
	}
	if note != "" {
		content = append(content, lipgloss.NewStyle().Foreground(mutedColor).Render(note))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(surfaceColor).
		Padding(1, 2).
		Width(innerWidth).
		Render(strings.Join(content, "\n"))
}

func renderShellHeader(width int, title, subtitle string, right []string) string {
	if width < MinWidth {
		width = MinWidth
	}
	titleText := lipgloss.NewStyle().Foreground(textColor).Bold(true).Render(title)
	metadata := []string{strings.TrimSpace(subtitle)}
	for _, item := range right {
		if strings.TrimSpace(item) != "" {
			metadata = append(metadata, item)
		}
	}
	metadataText := lipgloss.NewStyle().
		Foreground(mutedColor).
		Render(truncateText(strings.Join(metadata, " | "), max(12, width-6)))
	body := lipgloss.JoinVertical(lipgloss.Left, titleText, metadataText)

	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(borderColor).
		Background(surfaceColor).
		Padding(0, 1).
		Width(max(1, width-2)).
		Render(body)
}

func renderStatusLine(width int, statusType, msg string) string {
	if strings.TrimSpace(msg) == "" {
		msg = "Ready. Add a task, filter the board, or ask the assistant for a next move."
		statusType = "info"
	}

	tone := primaryColor
	label := "INFO"
	switch statusType {
	case "success":
		tone = successColor
		label = "OK"
	case "error":
		tone = errorColor
		label = "ERROR"
	case "warning":
		tone = warningColor
		label = "WARN"
	}

	prefix := lipgloss.NewStyle().Foreground(tone).Bold(true).Render("[" + label + "] ")
	return lipgloss.NewStyle().
		Foreground(textColor).
		Background(surfaceColor).
		Padding(0, 2).
		Width(width).
		Render(prefix + truncateText(msg, clamp(width-10, 16, width)))
}

func renderProgressBar(percentage int) string {
	percentage = clamp(percentage, 0, 100)
	width := 24
	filled := (percentage * width) / 100
	bar := strings.Repeat("=", filled) + strings.Repeat(".", width-filled)
	return fmt.Sprintf("[%s] %d%%", bar, percentage)
}

func (m Model) renderEnhancedStatsBar() string {
	if m.stats == nil {
		return ""
	}

	total := m.stats["total"]
	completed := m.stats["completed"]
	pending := m.stats["pending"]
	overdue := m.stats["overdue"]
	completionRate := 0
	if total > 0 {
		completionRate = (completed * 100) / total
	}

	cost := "AI idle"
	if m.processor != nil && m.processor.TotalCost > 0 {
		cost = fmt.Sprintf("Session cost $%.4f", m.processor.TotalCost)
	}

	parts := []string{
		renderTag(fmt.Sprintf("open %d", pending), primaryColor),
		renderTag(fmt.Sprintf("done %d", completed), successColor),
		renderTag(fmt.Sprintf("overdue %d", overdue), warningColor),
		lipgloss.NewStyle().Foreground(mutedColor).Render(renderProgressBar(completionRate)),
		lipgloss.NewStyle().Foreground(mutedColor).Render(cost),
	}

	return lipgloss.NewStyle().
		Width(m.width-2).
		Padding(0, 1).
		Render(strings.Join(parts, "  "))
}

func renderRecentLogs(logs []string, width int, height int) string {
	if len(logs) == 0 {
		return lipgloss.NewStyle().Foreground(mutedColor).Render("No recent activity")
	}
	if len(logs) > height {
		logs = logs[len(logs)-height:]
	}
	trimmed := make([]string, 0, len(logs))
	for _, line := range logs {
		trimmed = append(trimmed, truncateText(line, width))
	}
	return strings.Join(trimmed, "\n")
}

func renderShortcutList(lines []string) string {
	return strings.Join(lines, "\n")
}

func renderTodoSnapshot(todo *models.SmartTodo, width int) string {
	if todo == nil {
		return lipgloss.NewStyle().Foreground(mutedColor).Render("No task selected")
	}

	rows := []string{
		truncateText(todo.Title, width-4),
		fmt.Sprintf("Priority: %s", strings.ToLower(todo.Priority)),
		fmt.Sprintf("Category: %s", strings.ToLower(todo.Category)),
		fmt.Sprintf("Deadline: %s", stripANSI(renderDeadlineSummary(todo.Deadline))),
	}
	if todo.Location != "" {
		rows = append(rows, fmt.Sprintf("Location: %s", todo.Location))
	}
	if len(todo.Tasks) > 0 {
		rows = append(rows, fmt.Sprintf("Subtasks: %d", len(todo.Tasks)))
	}
	if todo.Context != "" {
		rows = append(rows, "", truncateText(todo.Context, width-4))
	}
	return strings.Join(rows, "\n")
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return models.TickMsg(t)
	})
}

func closingTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		return models.ClosingTickMsg{}
	})
}

func splashTimerCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return models.SplashDismissMsg{}
	})
}

func idleCheckCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return models.IdleCheckMsg{}
	})
}
