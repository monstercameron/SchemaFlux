package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/localization"
)

func (m Model) quitConfirmViewRender() string {
	background := m.listViewRender()

	message := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.NewStyle().Foreground(textColor).Bold(true).Render(localization.HeaderQuitConfirm),
		lipgloss.NewStyle().Foreground(mutedColor).Render("The current session will close and pending UI state will be discarded."),
		"",
		renderStatusLine(44, "warning", "Enter confirms. Esc returns to the board."),
	)
	modal := createModalBox("Confirm exit", message, 52, errorColor)
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	return renderModalOverlay(background, centered, m.width, m.height)
}
