package intelligence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

type TaskLens struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Priority       string   `json:"priority"`
	Category       string   `json:"category,omitempty"`
	Location       string   `json:"location,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	Context        string   `json:"context,omitempty"`
	Deadline       string   `json:"deadline,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
	Subtasks       []string `json:"subtasks,omitempty"`
	Completed      bool     `json:"completed"`
	CompletionRate int      `json:"completion_rate"`
}

type BoardCounts struct {
	Total     int `json:"total"`
	Open      int `json:"open"`
	Completed int `json:"completed"`
	Overdue   int `json:"overdue"`
	DueToday  int `json:"due_today"`
}

type BoardSnapshot struct {
	GeneratedAt    string      `json:"generated_at"`
	OpenTasks      []TaskLens  `json:"open_tasks"`
	CompletedTasks []TaskLens  `json:"completed_tasks"`
	Counts         BoardCounts `json:"counts"`
}

type FocusAnswer struct {
	FocusAreas     []string `json:"focus_areas"`
	QuickWins      []string `json:"quick_wins"`
	BiggestBlocker string   `json:"biggest_blocker"`
}

type BoardForecast struct {
	Outlook          string   `json:"outlook"`
	PredictedOpen    int      `json:"predicted_open"`
	PredictedDone    int      `json:"predicted_done"`
	RiskLevel        string   `json:"risk_level"`
	RecommendedMoves []string `json:"recommended_moves"`
}

type BoardReview struct {
	Summary          string        `json:"summary"`
	FocusAreas       []string      `json:"focus_areas"`
	QuickWins        []string      `json:"quick_wins"`
	BiggestBlocker   string        `json:"biggest_blocker"`
	Risks            []string      `json:"risks"`
	RecommendedMoves []string      `json:"recommended_moves"`
	Forecast         BoardForecast `json:"forecast"`
}

type BoardContextCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ContextMatch struct {
	Context   string  `json:"context"`
	TaskID    string  `json:"task_id"`
	TaskTitle string  `json:"task_title"`
	Score     float64 `json:"score"`
	Why       string  `json:"why,omitempty"`
}

type DayPlanBlock struct {
	Label   string   `json:"label"`
	Window  string   `json:"window"`
	Goal    string   `json:"goal"`
	TaskIDs []string `json:"task_ids"`
}

type DayPlan struct {
	Headline         string         `json:"headline"`
	Blocks           []DayPlanBlock `json:"blocks"`
	QuickWins        []string       `json:"quick_wins"`
	Risks            []string       `json:"risks"`
	Matches          []ContextMatch `json:"matches,omitempty"`
	RecommendedStart string         `json:"recommended_start,omitempty"`
}

func (s *Service) CaptureTodo(note string) (*models.SmartTodo, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, fmt.Errorf("note cannot be empty")
	}

	base, err := schemaflux.Extract[models.SmartTodo](
		note,
		schemaflux.NewExtractOptions().
			WithMode(schemaflux.TransformMode).
			WithIntelligence(schemaflux.Fast).
			WithSteering(`Extract a production-quality todo item.
- Title must be actionable and under 60 characters.
- Infer priority, category, effort, and location when reasonable.
- Create 2-5 subtasks when the note implies multiple steps.
- Context should be a brief execution hint, not generic filler.
- Prefer concrete deadlines over vague ones.`),
	)
	if err != nil {
		fallback := heuristicTodo(note)
		return &fallback, nil
	}

	normalized, err := schemaflux.Normalize(base, schemaflux.NewNormalizeOptions().
		WithCanonicalMappings(map[string]string{
			"asap":      "high",
			"immediate": "high",
			"quick":     "minimal",
			"easy":      "low",
			"office":    "office",
			"home":      "home",
		}).
		WithFields([]string{"priority", "category", "location", "effort"}).
		WithIntelligence(schemaflux.Fast))
	if err == nil {
		base = normalized.Normalized
	}

	enriched, err := schemaflux.EnrichInPlace(base, schemaflux.NewEnrichOptions().
		WithDerivationRules(map[string]string{
			"context":  "add one concise tactical hint for executing this task",
			"category": "fill when missing using the title and description",
			"location": "fill when missing using the task context",
		}).
		WithAddOnly(true).
		WithIntelligence(schemaflux.Fast))
	if err == nil {
		base = enriched
	}

	validation, err := schemaflux.Validate(base, schemaflux.NewValidateOptions().
		WithRules("priority must be high, medium, or low; effort must be minimal, low, medium, high, or massive; title must be specific; context should be concise").
		WithAutoCorrect(true).
		WithIntelligence(schemaflux.Fast))
	if err == nil && validation.Corrected != nil {
		base = *validation.Corrected
	}

	if len(base.Tasks) == 0 && looksComplex(note) {
		parts, err := schemaflux.DecomposeToSlice[string, models.Task](
			note,
			schemaflux.NewDecomposeOptions().
				WithStrategy("sequential").
				WithTargetParts(4).
				WithIncludeDependencies(false).
				WithIntelligence(schemaflux.Fast),
		)
		if err == nil && len(parts) > 0 {
			base.Tasks = parts
		}
	}

	final := sanitizeTodo(base)
	return &final, nil
}

func (s *Service) ReviseTodo(todo *models.SmartTodo, instruction string) (*models.SmartTodo, error) {
	if todo == nil {
		return nil, fmt.Errorf("todo cannot be nil")
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		copy := sanitizeTodo(*todo)
		return &copy, nil
	}

	prompt := fmt.Sprintf(`Current todo:
%s

Instruction:
%s

Update the todo without dropping useful information.`, mustJSON(todo), instruction)
	updated, err := schemaflux.Transform[string, models.SmartTodo](
		prompt,
		schemaflux.NewTransformOptions().
			WithIntelligence(schemaflux.Fast).
			WithMode(schemaflux.TransformMode).
			WithSteering("Apply the instruction carefully. Preserve IDs, completed subtasks, and existing useful context unless the instruction overrides them."),
	)
	if err != nil {
		copy := *todo
		copy.Context = strings.TrimSpace(strings.Join([]string{copy.Context, instruction}, "; "))
		result := sanitizeTodo(copy)
		return &result, nil
	}

	updated.ID = todo.ID
	updated.CreatedAt = todo.CreatedAt
	updated.Completed = todo.Completed
	updated.CompletedAt = todo.CompletedAt
	if len(updated.Tasks) == 0 {
		updated.Tasks = todo.Tasks
	}

	normalized, err := schemaflux.Normalize(updated, schemaflux.NewNormalizeOptions().
		WithFields([]string{"priority", "category", "location", "effort"}).
		WithIntelligence(schemaflux.Fast))
	if err == nil {
		updated = normalized.Normalized
	}

	validation, err := schemaflux.Validate(updated, schemaflux.NewValidateOptions().
		WithRules("priority must be high, medium, or low; effort must be minimal, low, medium, high, or massive; title must remain actionable").
		WithAutoCorrect(true).
		WithIntelligence(schemaflux.Fast))
	if err == nil && validation.Corrected != nil {
		updated = *validation.Corrected
	}

	result := sanitizeTodo(updated)
	return &result, nil
}

func (s *Service) RecommendNext(todos []*models.SmartTodo) (*models.SmartTodo, error) {
	if len(todos) == 0 {
		return nil, fmt.Errorf("no todos available")
	}
	if len(todos) == 1 {
		copy := *todos[0]
		return &copy, nil
	}

	openTodos := filterOpenTodos(todos)
	if len(openTodos) == 0 {
		copy := *todos[0]
		return &copy, nil
	}

	ranked, err := schemaflux.Rank(
		openTodos,
		schemaflux.NewRankOptions().
			WithQuery("best next task to work on now given urgency, deadline pressure, dependencies, effort, and momentum").
			WithTopK(minInt(3, len(openTodos))).
			WithRankingFactors([]string{"urgency", "deadline", "effort", "dependency pressure", "quick wins"}).
			WithIncludeExplanation(true).
			WithIntelligence(schemaflux.Smart),
	)
	shortlist := openTodos
	if err == nil && len(ranked.Items) > 0 {
		shortlist = make([]*models.SmartTodo, 0, len(ranked.Items))
		for _, item := range ranked.Items {
			copy := *item.Item
			shortlist = append(shortlist, &copy)
		}
	}

	best, err := schemaflux.Choose(shortlist, schemaflux.NewChooseOptions().WithCriteria([]string{
		"urgency",
		"leverage",
		"available energy fit",
		"likelihood of completion",
	}).
		WithIntelligence(schemaflux.Smart))
	if err == nil {
		return best, nil
	}

	fallback := heuristicBestTodo(openTodos)
	return fallback, nil
}

func (s *Service) PrioritizeBoard(todos []*models.SmartTodo) ([]*models.SmartTodo, error) {
	if len(todos) <= 1 {
		return cloneTodos(todos), nil
	}

	openTodos := filterOpenTodos(todos)
	if len(openTodos) == 0 {
		return cloneTodos(todos), nil
	}

	sorted, err := schemaflux.Sort(
		openTodos,
		schemaflux.NewSortOptions().
			WithCriteria("priority considering overdue work, today's deadlines, blocked tasks, effort, and momentum").
			WithIntelligence(schemaflux.Smart).
			WithSteering("Return a total ordering of the open tasks from highest immediate value to lowest. Avoid ties."),
	)
	if err != nil {
		sorted = heuristicSort(openTodos)
	}

	completed := make([]*models.SmartTodo, 0, len(todos))
	for _, todo := range todos {
		if todo.Completed {
			copy := *todo
			completed = append(completed, &copy)
		}
	}
	return append(cloneTodos(sorted), completed...), nil
}

func (s *Service) FilterBoard(todos []*models.SmartTodo, query string) ([]*models.SmartTodo, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(todos) == 0 {
		return cloneTodos(todos), nil
	}

	filtered, err := schemaflux.Filter(
		todos,
		schemaflux.NewFilterOptions().
			WithCriteria(query).
			WithIntelligence(schemaflux.Fast).
			WithSteering("Filter tasks semantically. Match timing, urgency, location, energy, and title cues."),
	)
	if err == nil {
		return filtered, nil
	}

	queryLower := strings.ToLower(query)
	fallback := make([]*models.SmartTodo, 0, len(todos))
	for _, todo := range todos {
		if strings.Contains(strings.ToLower(todo.Title), queryLower) ||
			strings.Contains(strings.ToLower(todo.Description), queryLower) ||
			strings.Contains(strings.ToLower(todo.Category), queryLower) ||
			strings.Contains(strings.ToLower(todo.Location), queryLower) {
			copy := *todo
			fallback = append(fallback, &copy)
		}
	}
	return fallback, nil
}

func (s *Service) BuildReview(todos []*models.SmartTodo) (BoardReview, error) {
	snapshot := buildBoardSnapshot(todos)
	snapshotJSON := mustJSON(snapshot)

	summary := "Board is quiet."
	if text, err := schemaflux.Summarize(snapshotJSON, func() schemaflux.SummarizeOptions {
		opts := schemaflux.NewSummarizeOptions()
		opts.TargetLength = 5
		opts.LengthUnit = "sentences"
		opts.Style = "executive"
		opts.FocusAreas = []string{"urgent work", "stale work", "quick wins"}
		opts.CommonOptions = opts.CommonOptions.WithIntelligence(schemaflux.Fast)
		return opts
	}()); err == nil {
		summary = text
	}

	auditResult, auditErr := schemaflux.Audit(snapshot, schemaflux.AuditOptions{
		Policies: []string{
			"High priority tasks should have either a clear deadline or a concrete next step",
			"Tasks with many subtasks should not be missing context",
			"Boards should avoid too many overdue open tasks",
		},
		Categories:   []string{"quality", "consistency", "planning"},
		Threshold:    0.45,
		Deep:         true,
		Mode:         schemaflux.TransformMode,
		Intelligence: schemaflux.Smart,
	})

	focus := FocusAnswer{}
	focusResult, focusErr := schemaflux.Question[BoardSnapshot, FocusAnswer](snapshot, schemaflux.NewQuestionOptions("What are the top three focus areas, the biggest blocker, and the best quick wins?").WithIntelligence(schemaflux.Smart))
	if focusErr == nil {
		focus = focusResult.Answer
	}

	forecast := BoardForecast{}
	forecastResult, forecastErr := schemaflux.Predict[BoardForecast](snapshot, schemaflux.NewPredictOptions().
		WithHorizon("next 3 working sessions").
		WithFactors([]string{"open count", "overdue work", "subtask load", "priority mix"}).
		WithIncludeScenarios(false).
		WithIntelligence(schemaflux.Smart))
	if forecastErr == nil {
		forecast = forecastResult.Prediction
	}

	review := BoardReview{
		Summary:          summary,
		FocusAreas:       focus.FocusAreas,
		QuickWins:        focus.QuickWins,
		BiggestBlocker:   focus.BiggestBlocker,
		Risks:            extractAuditRisks(auditResult),
		RecommendedMoves: forecast.RecommendedMoves,
		Forecast:         forecast,
	}

	synthesized, synthErr := schemaflux.Synthesize[BoardReview](
		[]any{summary, auditResult, focus, forecast},
		schemaflux.NewSynthesizeOptions().
			WithStrategy("integrate").
			WithGenerateInsights(true).
			WithCiteSources(false).
			WithIntelligence(schemaflux.Smart).
			WithSteering("Create an operator-friendly board review with concise risks, focus areas, and actionable next moves."),
	)
	if synthErr == nil {
		review = synthesized.Synthesized
		if review.Summary == "" {
			review.Summary = summary
		}
		if len(review.Risks) == 0 {
			review.Risks = extractAuditRisks(auditResult)
		}
		if len(review.RecommendedMoves) == 0 {
			review.RecommendedMoves = forecast.RecommendedMoves
		}
		if review.Forecast.Outlook == "" {
			review.Forecast = forecast
		}
	}

	if review.Summary == "" {
		review.Summary = summary
	}
	if len(review.FocusAreas) == 0 {
		review.FocusAreas = heuristicFocusAreas(todos)
	}
	if len(review.QuickWins) == 0 {
		review.QuickWins = heuristicQuickWins(todos)
	}
	if review.BiggestBlocker == "" {
		review.BiggestBlocker = heuristicBlocker(todos)
	}
	if len(review.Risks) == 0 && auditErr != nil {
		review.Risks = []string{"Audit unavailable; using heuristic risk detection"}
	}
	if len(review.RecommendedMoves) == 0 && forecastErr != nil {
		review.RecommendedMoves = []string{"Reduce overdue load", "Clear one quick win", "Protect one deep-work block"}
	}

	return review, nil
}

func (s *Service) PlanDay(todos []*models.SmartTodo, context string) (DayPlan, error) {
	snapshot := buildBoardSnapshot(todos)
	candidates := defaultContextCandidates(context)
	matches := []ContextMatch{}

	matchResult, err := schemaflux.SemanticMatch(snapshot.OpenTasks, candidates, schemaflux.NewMatchOptions().
		WithStrategy("best-fit").
		WithThreshold(0.4).
		WithMatchCriteria("Match each task to the most appropriate work mode or time block").
		WithIncludeExplanations(true).
		WithIntelligence(schemaflux.Fast))
	if err == nil {
		for _, pair := range matchResult.Matches {
			matches = append(matches, ContextMatch{
				Context:   pair.Target.Name,
				TaskID:    pair.Source.ID,
				TaskTitle: pair.Source.Title,
				Score:     pair.Score,
				Why:       pair.Explanation,
			})
		}
	}

	input := struct {
		Snapshot BoardSnapshot  `json:"snapshot"`
		Context  string         `json:"context"`
		Matches  []ContextMatch `json:"matches"`
	}{
		Snapshot: snapshot,
		Context:  context,
		Matches:  matches,
	}

	projected, projectErr := schemaflux.Project[any, DayPlan](input, schemaflux.ProjectOptions{
		InferMissing: true,
		Mode:         schemaflux.TransformMode,
		Intelligence: schemaflux.Smart,
		Steering:     "Project the board into a realistic one-day plan with 3-5 work blocks, quick wins, and a clear recommended start.",
	})
	if projectErr == nil {
		plan := projected.Projected
		plan.Matches = matches
		if plan.RecommendedStart == "" {
			plan.RecommendedStart = heuristicBestTodo(todos).Title
		}
		return plan, nil
	}

	best := heuristicBestTodo(filterOpenTodos(todos))
	plan := DayPlan{
		Headline:         "Stabilize the board and ship the most urgent work first.",
		QuickWins:        heuristicQuickWins(todos),
		Risks:            []string{"Review overdue work before opening new tasks."},
		Matches:          matches,
		RecommendedStart: best.Title,
		Blocks: []DayPlanBlock{
			{Label: "Focus block", Window: "Next 90 minutes", Goal: best.Title, TaskIDs: []string{best.ID}},
		},
	}
	return plan, nil
}

func buildBoardSnapshot(todos []*models.SmartTodo) BoardSnapshot {
	snapshot := BoardSnapshot{GeneratedAt: time.Now().Format(time.RFC3339)}
	for _, todo := range todos {
		lens := TaskLens{
			ID:             todo.ID,
			Title:          todo.Title,
			Description:    todo.Description,
			Priority:       strings.ToLower(todo.Priority),
			Category:       strings.ToLower(todo.Category),
			Location:       strings.ToLower(todo.Location),
			Effort:         strings.ToLower(todo.Effort),
			Context:        todo.Context,
			Dependencies:   append([]string(nil), todo.Dependencies...),
			Completed:      todo.Completed,
			CompletionRate: todo.TaskCompletionPercent(),
		}
		if todo.Deadline != nil {
			lens.Deadline = todo.Deadline.Format(time.RFC3339)
		}
		if len(todo.Tasks) > 0 {
			for _, task := range todo.Tasks {
				lens.Subtasks = append(lens.Subtasks, task.Text)
			}
		}
		snapshot.Counts.Total++
		if todo.Completed {
			snapshot.Counts.Completed++
			snapshot.CompletedTasks = append(snapshot.CompletedTasks, lens)
			continue
		}
		snapshot.Counts.Open++
		if todo.Deadline != nil {
			if todo.Deadline.Before(time.Now()) {
				snapshot.Counts.Overdue++
			}
			if sameDay(*todo.Deadline, time.Now()) {
				snapshot.Counts.DueToday++
			}
		}
		snapshot.OpenTasks = append(snapshot.OpenTasks, lens)
	}
	return snapshot
}

func sanitizeTodo(todo models.SmartTodo) models.SmartTodo {
	if strings.TrimSpace(todo.Title) == "" {
		todo.Title = "Untitled task"
	}
	todo.Title = truncate(todo.Title, 60)
	if todo.Priority != "high" && todo.Priority != "medium" && todo.Priority != "low" {
		todo.Priority = "medium"
	}
	validEfforts := map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "massive": true}
	todo.Effort = strings.ToLower(strings.TrimSpace(todo.Effort))
	if !validEfforts[todo.Effort] {
		todo.Effort = "medium"
	}
	if todo.Category == "" {
		todo.Category = "personal"
	}
	if todo.Location == "" {
		todo.Location = "home"
	}
	if todo.CreatedAt.IsZero() {
		todo.CreatedAt = time.Now()
	}
	for i := range todo.Tasks {
		todo.Tasks[i].Text = strings.TrimSpace(todo.Tasks[i].Text)
	}
	return todo
}

func heuristicTodo(note string) models.SmartTodo {
	title := truncate(strings.TrimSpace(note), 60)
	if title == "" {
		title = "Untitled task"
	}
	priority := "medium"
	if strings.Contains(strings.ToLower(note), "urgent") || strings.Contains(strings.ToLower(note), "asap") {
		priority = "high"
	}
	return sanitizeTodo(models.SmartTodo{
		Title:       title,
		Description: note,
		Priority:    priority,
		Category:    "personal",
		Location:    "home",
		Effort:      "medium",
		CreatedAt:   time.Now(),
	})
}

func heuristicBestTodo(todos []*models.SmartTodo) *models.SmartTodo {
	if len(todos) == 0 {
		return &models.SmartTodo{Title: "No tasks available"}
	}
	sorted := heuristicSort(todos)
	copy := *sorted[0]
	return &copy
}

func heuristicSort(todos []*models.SmartTodo) []*models.SmartTodo {
	cloned := cloneTodos(todos)
	sort.SliceStable(cloned, func(i, j int) bool {
		left := cloned[i]
		right := cloned[j]
		if priorityWeight(left.Priority) != priorityWeight(right.Priority) {
			return priorityWeight(left.Priority) > priorityWeight(right.Priority)
		}
		if left.Deadline != nil && right.Deadline != nil {
			return left.Deadline.Before(*right.Deadline)
		}
		if left.Deadline != nil {
			return true
		}
		if right.Deadline != nil {
			return false
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})
	return cloned
}

func priorityWeight(priority string) int {
	switch strings.ToLower(priority) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func filterOpenTodos(todos []*models.SmartTodo) []*models.SmartTodo {
	filtered := make([]*models.SmartTodo, 0, len(todos))
	for _, todo := range todos {
		if !todo.Completed {
			copy := *todo
			filtered = append(filtered, &copy)
		}
	}
	return filtered
}

func cloneTodos(todos []*models.SmartTodo) []*models.SmartTodo {
	cloned := make([]*models.SmartTodo, 0, len(todos))
	for _, todo := range todos {
		copy := *todo
		cloned = append(cloned, &copy)
	}
	return cloned
}

func extractAuditRisks(result schemaflux.AuditResult[BoardSnapshot]) []string {
	risks := make([]string, 0, len(result.Findings))
	for _, finding := range result.Findings {
		risks = append(risks, truncate(strings.TrimSpace(finding.Issue), 120))
	}
	if len(risks) == 0 {
		risks = append(risks, "No critical structural issues detected.")
	}
	return risks
}

func heuristicFocusAreas(todos []*models.SmartTodo) []string {
	areas := []string{}
	for _, todo := range heuristicSort(filterOpenTodos(todos)) {
		areas = append(areas, todo.Title)
		if len(areas) == 3 {
			break
		}
	}
	if len(areas) == 0 {
		areas = []string{"Clear one quick task to create momentum."}
	}
	return areas
}

func heuristicQuickWins(todos []*models.SmartTodo) []string {
	wins := []string{}
	for _, todo := range todos {
		if todo.Completed {
			continue
		}
		effort := strings.ToLower(todo.Effort)
		if effort == "minimal" || effort == "low" {
			wins = append(wins, todo.Title)
		}
		if len(wins) == 3 {
			break
		}
	}
	if len(wins) == 0 {
		wins = []string{"Clear the smallest open task to make room for focus work."}
	}
	return wins
}

func heuristicBlocker(todos []*models.SmartTodo) string {
	for _, todo := range todos {
		if !todo.Completed && len(todo.Dependencies) > 0 {
			return todo.Title
		}
	}
	for _, todo := range todos {
		if !todo.Completed && todo.Deadline != nil && todo.Deadline.Before(time.Now()) {
			return todo.Title
		}
	}
	return "No obvious blocker detected."
}

func defaultContextCandidates(context string) []BoardContextCandidate {
	base := []BoardContextCandidate{
		{Name: "Deep work", Description: "High-focus work block for cognitively demanding tasks"},
		{Name: "Admin sweep", Description: "Quick replies, follow-ups, cleanup, and low-friction tasks"},
		{Name: "Errands and outside", Description: "Tasks that require leaving home or being in a store or office"},
		{Name: "Collaboration", Description: "Tasks that involve meetings, calls, reviews, or handoffs"},
	}
	if strings.TrimSpace(context) != "" {
		base = append(base, BoardContextCandidate{Name: "Current context", Description: context})
	}
	return base
}

func looksComplex(note string) bool {
	lower := strings.ToLower(note)
	return strings.Contains(lower, " and ") || strings.Contains(lower, ",") || strings.Contains(lower, " then ") || strings.Contains(lower, " after ")
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
