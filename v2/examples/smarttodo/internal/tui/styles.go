package tui

import "github.com/charmbracelet/lipgloss"

var (
	canvasColor     = lipgloss.Color("#0b1020")
	surfaceColor    = lipgloss.Color("#121a2b")
	surfaceAltColor = lipgloss.Color("#18243a")
	borderColor     = lipgloss.Color("#355070")
	primaryColor    = lipgloss.Color("#7dd3fc")
	secondaryColor  = lipgloss.Color("#f59e0b")
	successColor    = lipgloss.Color("#34d399")
	warningColor    = lipgloss.Color("#fbbf24")
	errorColor      = lipgloss.Color("#f87171")
	mutedColor      = lipgloss.Color("#94a3b8")
	textColor       = lipgloss.Color("#e2e8f0")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(textColor)

	helpStyle = lipgloss.NewStyle().
			Foreground(mutedColor)

	priorityHighStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(errorColor)

	priorityMediumStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(warningColor)

	priorityLowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(successColor)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Background(surfaceColor).
			Padding(1, 2)

	focusedStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true)

	blurredStyle = lipgloss.NewStyle().
			Foreground(mutedColor)
)

func getCategoryEmoji(category string) string {
	switch category {
	case "work":
		return "WORK"
	case "personal":
		return "HOME"
	case "health":
		return "HEALTH"
	case "urgent":
		return "URGENT"
	case "learning":
		return "LEARN"
	case "shopping":
		return "SHOP"
	case "social":
		return "SOCIAL"
	case "finance":
		return "FINANCE"
	default:
		return "GENERAL"
	}
}

func getEffortEmoji(effort string) string {
	switch effort {
	case "minimal":
		return "XS"
	case "low":
		return "S"
	case "medium":
		return "M"
	case "high":
		return "L"
	case "massive":
		return "XL"
	default:
		return "M"
	}
}
