package tui

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type itemDelegate struct{}

func (d itemDelegate) Height() int                             { return 5 }
func (d itemDelegate) Spacing() int                            { return 1 }
func (d itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(todoItem)
	if !ok {
		return
	}

	title := i.Title()
	if i.todo.Completed {
		title = formatCompletedText(title, true)
	}
	desc := i.Description()
	body := title
	if desc != "" {
		body += "\n" + desc
	}

	contentWidth := max(1, m.Width()-4)
	cardWidth := max(1, contentWidth-4)
	style := lipgloss.NewStyle().
		Width(cardWidth).
		Padding(0, 1).
		Foreground(textColor)
	if index == m.Index() {
		style = style.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Background(surfaceAltColor)
	} else {
		style = style.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Background(surfaceColor)
	}

	fmt.Fprint(w, style.Render(body))
}
