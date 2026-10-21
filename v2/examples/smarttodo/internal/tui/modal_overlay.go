package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func renderModalOverlay(background, modal string, width, height int) string {
	dimmedBg := dimBackground(background)
	bgLines := strings.Split(dimmedBg, "\n")
	modalLines := strings.Split(modal, "\n")

	modalHeight := len(modalLines)
	modalWidth := 0
	for _, line := range modalLines {
		if w := lipgloss.Width(line); w > modalWidth {
			modalWidth = w
		}
	}

	startY := (height - modalHeight) / 2
	startX := (width - modalWidth) / 2
	if startY < 0 {
		startY = 0
	}
	if startX < 0 {
		startX = 0
	}

	for len(bgLines) < height {
		bgLines = append(bgLines, strings.Repeat(" ", width))
	}

	for i, modalLine := range modalLines {
		y := startY + i
		if y < 0 || y >= len(bgLines) {
			continue
		}
		bgLine := []rune(bgLines[y])
		for len(bgLine) < width {
			bgLine = append(bgLine, ' ')
		}
		modalRunes := []rune(modalLine)
		modalLineWidth := lipgloss.Width(modalLine)
		if startX < len(bgLine) {
			newLine := make([]rune, 0, width)
			if startX > 0 {
				newLine = append(newLine, bgLine[:startX]...)
			}
			newLine = append(newLine, modalRunes...)
			endX := startX + modalLineWidth
			if endX < len(bgLine) {
				newLine = append(newLine, bgLine[endX:]...)
			}
			bgLines[y] = string(newLine)
		}
	}

	if len(bgLines) > height {
		bgLines = bgLines[:height]
	}
	return strings.Join(bgLines, "\n")
}

func dimBackground(content string) string {
	lines := strings.Split(content, "\n")
	dimmed := make([]string, len(lines))
	style := lipgloss.NewStyle().Foreground(mutedColor)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			dimmed[i] = line
		} else {
			dimmed[i] = style.Render(stripANSI(line))
		}
	}
	return strings.Join(dimmed, "\n")
}

func stripANSI(str string) string {
	result := ""
	inEscape := false
	for _, r := range str {
		if r == '\x1b' {
			inEscape = true
		} else if inEscape {
			if r == 'm' {
				inEscape = false
			}
		} else {
			result += string(r)
		}
	}
	return result
}

func createModalBox(title, content string, width int, borderColor lipgloss.Color) string {
	header := lipgloss.NewStyle().
		Foreground(borderColor).
		Bold(true).
		Render(title)
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", content)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(surfaceColor).
		Padding(1, 2).
		Width(width).
		Render(body)
}
