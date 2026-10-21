package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) addViewRenderFixed() string {
	background := m.listViewRender()
	modalWidth := clamp(m.width-10, 44, 66)

	if !m.input.Focused() {
		m.input.Focus()
	}
	m.input.Width = modalWidth - 8

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.NewStyle().Foreground(successColor).Bold(true).Render("Capture a task in plain language"),
		lipgloss.NewStyle().Foreground(mutedColor).Render("Examples: call Sam tomorrow at 9, prep board notes, buy groceries and plan dinner."),
		"",
		borderStyle.Width(modalWidth-4).Render(m.input.View()),
		"",
		renderPanel("AI extraction", strings.Join([]string{
			"The assistant will infer:",
			"- priority and urgency",
			"- deadline or timing",
			"- category and location",
			"- subtasks when the note implies a sequence",
		}, "\n"), modalWidth-8),
		"",
		lipgloss.NewStyle().Foreground(mutedColor).Render("Enter adds the task. Esc cancels."),
	)

	modal := createModalBox("New task", body, modalWidth, successColor)
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	return renderModalOverlay(background, centered, m.width, m.height)
}

func (m Model) editViewRenderFixed() string {
	if m.selectedTodo == nil {
		return m.listViewRender()
	}

	background := m.listViewRender()
	modalWidth := clamp(m.width-10, 50, 74)

	currentInfo := renderPanel("Current task", strings.Join([]string{
		truncateText(m.selectedTodo.Title, modalWidth-12),
		fmt.Sprintf("Priority: %s", m.selectedTodo.Priority),
		fmt.Sprintf("Category: %s", m.selectedTodo.Category),
		fmt.Sprintf("Deadline: %s", stripANSI(renderDeadlineSummary(m.selectedTodo.Deadline))),
	}, "\n"), modalWidth-8)

	if !m.editInput.Focused() && !m.editProcessing {
		m.editInput.Focus()
	}
	m.editInput.Width = modalWidth - 8

	var body string
	if m.editProcessing {
		steps := []string{"Analyzing changes", "Merging intent", "Updating task", "Finalizing"}
		step := steps[(m.loadingFrame/10)%len(steps)]
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			currentInfo,
			"",
			lipgloss.NewStyle().Foreground(warningColor).Bold(true).Render(step+"..."),
			"",
			lipgloss.NewStyle().Foreground(mutedColor).Render("The assistant is applying your requested change."),
		)
	} else {
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			currentInfo,
			"",
			lipgloss.NewStyle().Foreground(secondaryColor).Bold(true).Render("Describe the change"),
			lipgloss.NewStyle().Foreground(mutedColor).Render("Examples: move to Friday, make this urgent, split into 3 subtasks, change location to office."),
			borderStyle.Width(modalWidth-4).Render(m.editInput.View()),
			"",
			lipgloss.NewStyle().Foreground(mutedColor).Render("Enter saves. Esc cancels."),
		)
	}

	modal := createModalBox("Edit task", body, modalWidth, secondaryColor)
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	return renderModalOverlay(background, centered, m.width, m.height)
}

func (m Model) suggestViewRenderFixed() string {
	background := m.listViewRender()
	modalWidth := clamp(m.width-10, 46, 64)

	var content string
	switch {
	case m.loading:
		content = lipgloss.JoinVertical(
			lipgloss.Center,
			lipgloss.NewStyle().Foreground(warningColor).Bold(true).Render("Reviewing the board"),
			"",
			lipgloss.NewStyle().Foreground(mutedColor).Render("Choosing the next best move based on urgency, timing, and effort."),
		)
	case m.selectedTodo != nil && m.mode == suggestView:
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			renderPanel("Recommended next task", strings.Join([]string{
				truncateText(m.selectedTodo.Title, modalWidth-12),
				fmt.Sprintf("Priority: %s", m.selectedTodo.Priority),
				fmt.Sprintf("Category: %s", m.selectedTodo.Category),
				fmt.Sprintf("Deadline: %s", stripANSI(renderDeadlineSummary(m.selectedTodo.Deadline))),
			}, "\n"), modalWidth-8),
			"",
			lipgloss.NewStyle().Foreground(mutedColor).Render("Enter marks this as your focus. Esc dismisses the recommendation."),
		)
	default:
		content = lipgloss.NewStyle().Foreground(mutedColor).Render("No suggestion is available right now.")
	}

	modal := createModalBox("AI suggestion", content, modalWidth, primaryColor)
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	return renderModalOverlay(background, centered, m.width, m.height)
}
