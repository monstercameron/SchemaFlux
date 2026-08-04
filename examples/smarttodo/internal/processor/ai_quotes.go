package processor

import (
	"fmt"
	"math/rand"

	"github.com/monstercameron/schemaflux"
)

// GenerateAIQuote generates a motivational quote using AI
func GenerateAIQuote() (string, error) {
	// Historical figures for quote attribution
	figures := []string{
		"Benjamin Franklin",
		"Marcus Aurelius",
		"Leonardo da Vinci",
		"Marie Curie",
		"Albert Einstein",
		"Maya Angelou",
		"Nelson Mandela",
		"Confucius",
		"Aristotle",
		"Socrates",
		"Steve Jobs",
		"Thomas Edison",
		"Winston Churchill",
		"Theodore Roosevelt",
		"Abraham Lincoln",
		"Martin Luther King Jr.",
		"Mahatma Gandhi",
		"Sun Tzu",
		"Lao Tzu",
		"Seneca",
	}

	// Themes for quotes
	themes := []string{
		"the importance of consistency and daily habits",
		"overcoming procrastination",
		"the value of focused work",
		"time management and priorities",
		"breaking large tasks into smaller ones",
		"the compound effect of small actions",
		"maintaining momentum in difficult times",
		"the balance between work and rest",
		"the power of starting now rather than later",
		"learning from failure and setbacks",
		"the importance of clear goals",
		"staying organized amid chaos",
		"the value of deep focus",
		"persistence through challenges",
		"the wisdom of planning ahead",
	}

	// Randomly select a figure and theme
	figure := figures[rand.Intn(len(figures))]
	theme := themes[rand.Intn(len(themes))]

	prompt := fmt.Sprintf(`Generate a single motivational quote about %s, written in the style of %s.
The quote should be:
- Concise (under 20 words)
- Profound and memorable
- Relevant to productivity and task management
- Something that person might have actually said

Format: Just the quote text, no attribution or quotation marks.`, theme, figure)

	quote, err := schemaflux.Generate[string](
		prompt,
		schemaflux.NewGenerateOptions().
			WithIntelligence(schemaflux.Fast).
			WithMode(schemaflux.Creative),
	)

	if err != nil {
		// Fallback to static quotes if AI fails
		fallbackQuotes := []string{
			"The secret of getting ahead is getting started.",
			"Focus on being productive instead of busy.",
			"Small daily improvements lead to stunning results.",
			"Action is the foundational key to all success.",
			"The way to get started is to quit talking and begin doing.",
		}
		quote = fallbackQuotes[rand.Intn(len(fallbackQuotes))]
		figure = "Unknown"
	}

	return fmt.Sprintf("\"%s\" - %s", quote, figure), nil
}
