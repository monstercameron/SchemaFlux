package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Layout constants to prevent rendering issues
const (
	// Minimum dimensions to prevent crashes
	MinWidth  = 80
	MinHeight = 24

	// Fixed component heights for consistent layout
	HeaderHeight      = 4 // Header with border
	StatsBarHeight    = 2 // Stats bar with margin
	BottomPanelHeight = 9 // Keyboard shortcuts and activity log
	StatusLineHeight  = 1 // Status message line
	SpacingHeight     = 3 // Total spacing between components

	// Margins and padding
	ListHorizontalPadding = 4
	ModalMinMargin        = 10

	// Maximum widths to prevent overflow
	MaxTodoTitleWidth   = 80
	MaxTaskTextWidth    = 70
	MaxProgressBarWidth = 40
)

// truncateText safely truncates text to fit within width
func truncateText(text string, maxWidth int) string {
	if lipgloss.Width(text) <= maxWidth {
		return text
	}

	// Account for ellipsis
	if maxWidth < 4 {
		return "..."
	}

	// Binary search for the right truncation point
	runes := []rune(text)
	left, right := 0, len(runes)
	result := ""

	for left < right {
		mid := (left + right + 1) / 2
		candidate := string(runes[:mid]) + "..."
		if lipgloss.Width(candidate) <= maxWidth {
			result = candidate
			left = mid
		} else {
			right = mid - 1
		}
	}

	if result == "" && maxWidth >= 3 {
		return "..."
	}

	return result
}
