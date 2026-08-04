package processor

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/database"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/intelligence"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

// TodoProcessor handles AI-powered todo processing
type TodoProcessor struct {
	TotalCost  float64 // Track total API costs for session
	LastCost   float64 // Last operation cost
	FastCalls  int     // Number of fast model calls
	SmartCalls int     // Number of smart model calls
	service    *intelligence.Service
}

// NewTodoProcessor creates a new TodoProcessor instance
func NewTodoProcessor() *TodoProcessor {
	return &TodoProcessor{
		service: intelligence.NewService(),
	}
}

// RefineTaskText cleans up and improves task text without full AI processing
func (tp *TodoProcessor) RefineTaskText(taskText string) string {
	// Simple refinement - capitalize, trim, add punctuation if needed
	text := strings.TrimSpace(taskText)
	if text == "" {
		return text
	}

	// Capitalize first letter
	runes := []rune(text)
	if len(runes) > 0 {
		runes[0] = unicode.ToUpper(runes[0])
	}
	text = string(runes)

	// Remove duplicate spaces
	text = strings.Join(strings.Fields(text), " ")

	// Add period if missing and doesn't end with punctuation
	lastChar := text[len(text)-1]
	if lastChar != '.' && lastChar != '!' && lastChar != '?' {
		text += "."
	}

	return text
}

// FixTaskGrammar uses AI to fix grammatical errors in task text
func (tp *TodoProcessor) FixTaskGrammar(taskText string) (string, error) {
	// Track cost
	tp.FastCalls++
	tp.LastCost = 0.0001 // Minimal cost for fast model
	tp.TotalCost += tp.LastCost

	fixed, err := schemaflux.Transform[string, string](
		taskText,
		schemaflux.NewTransformOptions().
			WithIntelligence(schemaflux.Fast).
			WithMode(schemaflux.TransformMode).
			WithSteering("Fix any grammatical errors and improve clarity. Keep it concise."),
	)

	if err != nil {
		// Fallback to simple refinement
		return tp.RefineTaskText(taskText), nil
	}

	// Ensure it's not too long
	if len(fixed) > 100 {
		fixed = fixed[:97] + "..."
	}

	return fixed, nil
}

func (tp *TodoProcessor) ProcessNote(input string) (*models.SmartTodo, error) {
	// Track cost - estimate for fast model
	tp.FastCalls += 4
	tp.LastCost = 0.0006
	tp.TotalCost += tp.LastCost
	todo, err := tp.service.CaptureTodo(input)
	if err != nil {
		return nil, err
	}
	todo.Cost = tp.LastCost
	return todo, nil
}

func (tp *TodoProcessor) SuggestNext(todos []*models.SmartTodo) (*models.SmartTodo, error) {
	if len(todos) == 0 {
		return nil, fmt.Errorf("no todos available")
	}

	// Single todo, return it
	if len(todos) == 1 {
		return todos[0], nil
	}

	// Track cost
	tp.FastCalls++
	tp.SmartCalls++
	tp.LastCost = 0.0005
	tp.TotalCost += tp.LastCost
	return tp.service.RecommendNext(todos)
}

func (tp *TodoProcessor) FilterByContext(todos []*models.SmartTodo, context string) ([]*models.SmartTodo, error) {
	if len(todos) <= 1 {
		return todos, nil
	}
	return tp.service.FilterBoard(todos, context)
}

func (tp *TodoProcessor) GroupByCategory(todos []*models.SmartTodo) map[string][]*models.SmartTodo {
	groups := make(map[string][]*models.SmartTodo)

	for _, todo := range todos {
		category := todo.Category
		if category == "" {
			category = "uncategorized"
		}
		groups[category] = append(groups[category], todo)
	}

	return groups
}

func (tp *TodoProcessor) SortByPriority(todos []*models.SmartTodo) []*models.SmartTodo {
	// Manual sort since we need specific priority ordering
	high := []*models.SmartTodo{}
	medium := []*models.SmartTodo{}
	low := []*models.SmartTodo{}

	for _, todo := range todos {
		switch todo.Priority {
		case "high":
			high = append(high, todo)
		case "medium":
			medium = append(medium, todo)
		default:
			low = append(low, todo)
		}
	}

	// Within each priority, sort by deadline
	sortByDeadline := func(todos []*models.SmartTodo) {
		for i := 0; i < len(todos)-1; i++ {
			for j := i + 1; j < len(todos); j++ {
				// Both have deadlines
				if todos[i].Deadline != nil && todos[j].Deadline != nil {
					if todos[i].Deadline.After(*todos[j].Deadline) {
						todos[i], todos[j] = todos[j], todos[i]
					}
				} else if todos[i].Deadline == nil && todos[j].Deadline != nil {
					// Task with deadline comes first
					todos[i], todos[j] = todos[j], todos[i]
				}
			}
		}
	}

	sortByDeadline(high)
	sortByDeadline(medium)
	sortByDeadline(low)

	// Combine
	result := make([]*models.SmartTodo, 0, len(todos))
	result = append(result, high...)
	result = append(result, medium...)
	result = append(result, low...)

	return result
}

// ProcessEditContext merges edit context with existing todo using AI
func (tp *TodoProcessor) ProcessEditContext(todo *models.SmartTodo, context string) (*models.SmartTodo, error) {
	// Track cost
	tp.FastCalls++
	tp.LastCost = 0.0003 // Approximate cost
	tp.TotalCost += tp.LastCost

	updatedTodo, err := tp.service.ReviseTodo(todo, context)
	if err != nil {
		return todo, fmt.Errorf("failed to process edit: %w", err)
	}
	updatedTodo.Cost = todo.Cost + tp.LastCost
	return updatedTodo, nil
}

func (tp *TodoProcessor) EstimateTimeToComplete(todos []*models.SmartTodo) time.Duration {
	var total time.Duration

	for _, todo := range todos {
		switch strings.ToLower(todo.Effort) {
		case "minimal":
			total += 5 * time.Minute
		case "low":
			total += 30 * time.Minute
		case "medium":
			total += 90 * time.Minute
		case "high":
			total += 3 * time.Hour
		case "massive":
			total += 5 * time.Hour
		default:
			total += 1 * time.Hour
		}
	}

	return total
}

// SmartPrioritize reorders todos using AI based on multiple factors
func (tp *TodoProcessor) SmartPrioritize(todos []*models.SmartTodo) ([]*models.SmartTodo, error) {
	if len(todos) <= 1 {
		return todos, nil
	}

	// Track cost for smart model
	tp.SmartCalls += 2
	tp.LastCost = 0.0012
	tp.TotalCost += tp.LastCost
	return tp.service.PrioritizeBoard(todos)
}

// SemanticFilter filters todos using natural language queries
func (tp *TodoProcessor) SemanticFilter(todos []*models.SmartTodo, query string) ([]*models.SmartTodo, error) {
	if len(todos) == 0 || query == "" {
		return todos, nil
	}

	// Track cost
	tp.FastCalls++
	tp.LastCost = 0.0002
	tp.TotalCost += tp.LastCost
	return tp.service.FilterBoard(todos, query)
}

func (tp *TodoProcessor) BuildReview(todos []*models.SmartTodo) (intelligence.BoardReview, error) {
	tp.FastCalls += 2
	tp.SmartCalls += 3
	tp.LastCost = 0.0018
	tp.TotalCost += tp.LastCost
	return tp.service.BuildReview(todos)
}

func (tp *TodoProcessor) PlanDay(todos []*models.SmartTodo, context string) (intelligence.DayPlan, error) {
	tp.FastCalls++
	tp.SmartCalls++
	tp.LastCost = 0.001
	tp.TotalCost += tp.LastCost
	return tp.service.PlanDay(todos, context)
}

// UpdateDeadlines checks and updates deadline-based priorities
func (tp *TodoProcessor) UpdateDeadlines(db *database.Database) error {
	todos, err := db.GetAllTodos()
	if err != nil {
		return err
	}

	now := time.Now()
	updated := false

	for _, todo := range todos {
		if todo.Completed || todo.Deadline == nil {
			continue
		}

		// Check if deadline is approaching
		daysUntil := int(todo.Deadline.Sub(now).Hours() / 24)

		// Auto-escalate priority if deadline is near
		if daysUntil <= 1 && todo.Priority != "high" {
			todo.Priority = "high"
			if err := db.UpdateTodo(todo); err == nil {
				updated = true
			}
		} else if daysUntil <= 3 && todo.Priority == "low" {
			todo.Priority = "medium"
			if err := db.UpdateTodo(todo); err == nil {
				updated = true
			}
		}
	}

	if updated {
		return nil
	}

	return nil
}
