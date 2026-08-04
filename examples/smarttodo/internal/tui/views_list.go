package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/localization"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

func (m Model) listViewRender() string {
	header := renderShellHeader(
		m.width,
		localization.AppName,
		fmt.Sprintf("%s | %s", fallbackLabel(m.listTitle, "Focused task board"), fallbackLabel(m.userName, "anonymous")),
		[]string{
			lipgloss.NewStyle().Foreground(primaryColor).Render(time.Now().Format("Mon Jan 2")),
			lipgloss.NewStyle().Foreground(mutedColor).Render(fmt.Sprintf("%d items", len(m.todos))),
		},
	)

	statsBar := m.renderEnhancedStatsBar()

	bodyWidth := clamp(m.width-4, 72, 148)
	stackSideColumn := m.width < 132
	compactHeight := m.height < 34
	showFooter := m.height >= 40
	leftWidth := bodyWidth
	rightWidth := bodyWidth
	if !stackSideColumn && !compactHeight {
		leftWidth = clamp((bodyWidth*3)/5, 46, 92)
		rightWidth = clamp(bodyWidth-leftWidth-2, 24, 50)
	}
	listHeight := clamp(m.height-18, 6, 24)
	if compactHeight {
		listHeight = clamp(m.height-14, 6, 16)
	}
	m.list.SetSize(leftWidth-6, listHeight)

	listPanel := renderPanel("Task queue", m.list.View(), leftWidth)

	var selectedTodo *models.SmartTodo
	if item, ok := m.list.SelectedItem().(todoItem); ok {
		selectedTodo = item.todo
	}

	rightTop := renderPanel("Focus", renderTodoSnapshot(selectedTodo, rightWidth-4), rightWidth)

	shortcutLines := []string{
		"ctrl+a  add task",
		"enter   toggle complete",
		"ctrl+e  edit task",
		"ctrl+d  delete task",
		"v       details",
		"right   subtasks",
		"ctrl+s  suggest next",
		"ctrl+r  smart prioritize",
		"ctrl+i  board stats",
		"ctrl+k  update API key",
		"esc     quit",
	}
	shortcuts := renderPanel("Controls", renderShortcutList(shortcutLines), rightWidth)
	rightColumn := lipgloss.JoinVertical(lipgloss.Left, rightTop, shortcuts)

	mainSection := listPanel
	if !compactHeight {
		mainSection = lipgloss.JoinHorizontal(lipgloss.Top, listPanel, rightColumn)
	}
	if stackSideColumn && !compactHeight {
		mainSection = lipgloss.JoinVertical(lipgloss.Left, listPanel, rightColumn)
	}

	status := renderStatusLine(m.width-2, m.statusType, m.statusMsg)
	footerLeft := renderPanel("Activity", renderRecentLogs(m.consoleLogs, clamp((m.width-10)/2, 28, 64), 8), clamp((m.width-6)/2, 34, 72))
	footerRight := renderPanel("Hints", strings.Join([]string{
		"Use natural language when adding tasks.",
		"The AI can extract subtasks, urgency, timing, and context.",
		fmt.Sprintf("Locale: %s", fallbackLabel(localization.GetLocale(), "en")),
	}, "\n"), clamp((m.width-6)/2, 34, 72))
	footerSection := lipgloss.JoinHorizontal(lipgloss.Top, footerLeft, footerRight)
	if stackSideColumn {
		footerSection = lipgloss.JoinVertical(lipgloss.Left, footerLeft, footerRight)
	}
	compactControls := lipgloss.NewStyle().
		Foreground(mutedColor).
		Render("Keys: ctrl+a add | enter toggle | ctrl+e edit | ctrl+d delete | v details | right subtasks | esc quit")

	contentParts := []string{header, statsBar, mainSection}
	if compactHeight {
		contentParts = append(contentParts, compactControls)
	}
	contentParts = append(contentParts, status)
	if showFooter && !compactHeight {
		contentParts = append(contentParts, footerSection)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, contentParts...)
	return renderViewport(
		m.width,
		m.height,
		lipgloss.Left,
		lipgloss.Top,
		lipgloss.NewStyle().Padding(1, 1).Render(content),
	)
}

func fallbackLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func renderPriorityLabel(priority string) string {
	switch strings.ToLower(priority) {
	case "high":
		return priorityHighStyle.Render("HIGH")
	case "medium":
		return priorityMediumStyle.Render("MED")
	case "low":
		return priorityLowStyle.Render("LOW")
	default:
		return lipgloss.NewStyle().Foreground(mutedColor).Render("OPEN")
	}
}

func renderDeadlineSummary(deadline *time.Time) string {
	if deadline == nil {
		return "No deadline"
	}
	daysUntil := int(time.Until(*deadline).Hours() / 24)
	switch {
	case daysUntil < 0:
		return lipgloss.NewStyle().Foreground(errorColor).Render(fmt.Sprintf("Overdue by %d days", -daysUntil))
	case daysUntil == 0:
		return lipgloss.NewStyle().Foreground(warningColor).Render("Due today")
	case daysUntil == 1:
		return lipgloss.NewStyle().Foreground(warningColor).Render("Due tomorrow")
	default:
		return deadline.Format("Mon Jan 2")
	}
}

func renderLocationSummary(todoContext, location string) string {
	value := strings.TrimSpace(location)
	if value == "" {
		value = strings.TrimSpace(todoContext)
	}
	if value == "" {
		value = localization.T(localization.LocationHome)
	}
	return value
}
