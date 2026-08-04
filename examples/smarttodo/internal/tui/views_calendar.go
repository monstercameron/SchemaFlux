package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

func (m Model) calendarViewRender() string {
	type timeSlot struct {
		time  time.Time
		todos []*models.SmartTodo
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	slots := make([]timeSlot, 0, 36)
	for hour := 6; hour <= 23; hour++ {
		for minute := 0; minute < 60; minute += 30 {
			slotTime := today.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
			slots = append(slots, timeSlot{time: slotTime, todos: []*models.SmartTodo{}})
		}
	}

	for _, todo := range m.todos {
		if todo.Completed || todo.Deadline == nil {
			continue
		}
		if !sameDay(*todo.Deadline, today) {
			continue
		}
		for i := range slots {
			if i < len(slots)-1 {
				if (todo.Deadline.Equal(slots[i].time) || todo.Deadline.After(slots[i].time)) && todo.Deadline.Before(slots[i+1].time) {
					slots[i].todos = append(slots[i].todos, todo)
					break
				}
			} else if todo.Deadline.Equal(slots[i].time) || todo.Deadline.After(slots[i].time) {
				slots[i].todos = append(slots[i].todos, todo)
			}
		}
	}

	lines := []string{}
	currentSlotIndex := -1
	for i, slot := range slots {
		if now.After(slot.time) && (i == len(slots)-1 || now.Before(slots[i+1].time)) {
			currentSlotIndex = i
		}

		timeLabel := slot.time.Format("3:04 PM")
		labelStyle := lipgloss.NewStyle().Foreground(mutedColor)
		if i == currentSlotIndex {
			labelStyle = lipgloss.NewStyle().Foreground(primaryColor).Bold(true)
		} else if slot.time.After(now) {
			labelStyle = lipgloss.NewStyle().Foreground(secondaryColor)
		}

		line := labelStyle.Render(fmt.Sprintf("%-8s", timeLabel)) + " | "
		if len(slot.todos) == 0 {
			if i == currentSlotIndex {
				line += lipgloss.NewStyle().Foreground(primaryColor).Italic(true).Render("Current slot")
			} else {
				line += lipgloss.NewStyle().Foreground(mutedColor).Render("No scheduled tasks")
			}
		} else {
			entries := make([]string, 0, len(slot.todos))
			for _, todo := range slot.todos {
				entries = append(entries, truncateText(todo.Title, 46))
			}
			line += strings.Join(entries, " | ")
		}
		lines = append(lines, line)
	}

	visibleSlots := 20
	startIndex := 0
	if currentSlotIndex >= 0 {
		startIndex = clamp(currentSlotIndex-visibleSlots/2, 0, maxInt(len(lines)-visibleSlots, 0))
	}
	endIndex := clamp(startIndex+visibleSlots, 0, len(lines))
	visible := strings.Join(lines[startIndex:endIndex], "\n")

	todayCount := 0
	overdueCount := 0
	for _, todo := range m.todos {
		if !todo.Completed && todo.Deadline != nil {
			if sameDay(*todo.Deadline, today) {
				todayCount++
			}
			if todo.Deadline.Before(now) {
				overdueCount++
			}
		}
	}

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		renderShellHeader(m.width, "Daily calendar", now.Format("Monday, January 2, 2006"), []string{lipgloss.NewStyle().Foreground(primaryColor).Render(now.Format("15:04"))}),
		renderPanel("Timeline", visible, 84),
		renderPanel("Snapshot", strings.Join([]string{
			fmt.Sprintf("Tasks due today: %d", todayCount),
			fmt.Sprintf("Overdue tasks: %d", overdueCount),
			"",
			"Esc returns to the board.",
		}, "\n"), 84),
	)

	return lipgloss.NewStyle().Background(canvasColor).Padding(1, 1).Render(content)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
