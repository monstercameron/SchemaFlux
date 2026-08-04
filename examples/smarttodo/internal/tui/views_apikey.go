package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/monstercameron/schemaflux"
)

func (m Model) apiKeyViewRender() string {
	maskedInput := m.setupInput.Value()
	if len(maskedInput) > 8 {
		maskedInput = maskedInput[:4] + strings.Repeat("*", len(maskedInput)-8) + maskedInput[len(maskedInput)-4:]
	}
	if maskedInput == "" {
		maskedInput = m.setupInput.Placeholder
	}

	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Padding(0, 1).
		Width(58).
		Render(maskedInput)

	body := []string{
		lipgloss.NewStyle().Foreground(textColor).Bold(true).Render("Connect an API key"),
		lipgloss.NewStyle().Foreground(mutedColor).Render("Enter an OpenAI key to unlock AI capture, prioritization, and board analysis."),
		"",
		inputBox,
		"",
		lipgloss.NewStyle().Foreground(mutedColor).Render("Enter validates and saves. Esc quits."),
	}
	if m.statusMsg != "" {
		body = append(body, "", renderStatusLine(58, m.statusType, m.statusMsg))
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, renderPanel("API key setup", strings.Join(body, "\n"), 70))
}

func validateAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("empty API key")
	}

	previousOpenAI := os.Getenv("OPENAI_API_KEY")
	previousSchemaFlux := os.Getenv("SCHEMAFLUX_API_KEY")

	_ = os.Setenv("OPENAI_API_KEY", apiKey)
	_ = os.Setenv("SCHEMAFLUX_API_KEY", apiKey)
	schemaflux.Init(apiKey)

	_, err := schemaflux.Generating[string]("Reply with ok.").Fast().Run()
	if err != nil {
		_ = os.Setenv("OPENAI_API_KEY", previousOpenAI)
		_ = os.Setenv("SCHEMAFLUX_API_KEY", previousSchemaFlux)
		if previousSchemaFlux != "" {
			schemaflux.Init(previousSchemaFlux)
		}
		return fmt.Errorf("invalid API key: %w", err)
	}
	return nil
}

func saveAPIKey(apiKey string) error {
	const envPath = ".env"
	content, err := os.ReadFile(envPath)
	existingLines := []string{}
	if err == nil {
		existingLines = strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	}

	updatedOpenAI := false
	updatedSchemaFlux := false
	for i, line := range existingLines {
		switch {
		case strings.HasPrefix(line, "OPENAI_API_KEY="):
			existingLines[i] = fmt.Sprintf("OPENAI_API_KEY=%s", apiKey)
			updatedOpenAI = true
		case strings.HasPrefix(line, "SCHEMAFLUX_API_KEY="):
			existingLines[i] = fmt.Sprintf("SCHEMAFLUX_API_KEY=%s", apiKey)
			updatedSchemaFlux = true
		}
	}
	if !updatedOpenAI {
		existingLines = append(existingLines, fmt.Sprintf("OPENAI_API_KEY=%s", apiKey))
	}
	if !updatedSchemaFlux {
		existingLines = append(existingLines, fmt.Sprintf("SCHEMAFLUX_API_KEY=%s", apiKey))
	}

	newContent := strings.TrimSpace(strings.Join(existingLines, "\n")) + "\n"
	if err := os.WriteFile(envPath, []byte(newContent), 0600); err != nil {
		return err
	}

	_ = os.Setenv("OPENAI_API_KEY", apiKey)
	_ = os.Setenv("SCHEMAFLUX_API_KEY", apiKey)
	return nil
}
