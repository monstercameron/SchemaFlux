package browserapp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/intelligence"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

type Shell struct {
	service *intelligence.Service
	state   ShellState
}

type ShellState struct {
	Todos    []*models.SmartTodo       `json:"todos"`
	Context  string                    `json:"context"`
	Review   *intelligence.BoardReview `json:"review,omitempty"`
	Focus    *models.SmartTodo         `json:"focus,omitempty"`
	Plan     *intelligence.DayPlan     `json:"plan,omitempty"`
	NextID   int                       `json:"next_id"`
	BootedAt time.Time                 `json:"booted_at"`
}

func NewShell() *Shell {
	return &Shell{
		service: intelligence.NewService(),
		state: ShellState{
			Todos:    []*models.SmartTodo{},
			NextID:   1,
			BootedAt: time.Now(),
		},
	}
}

func (s *Shell) BootMessage() string {
	return strings.TrimSpace(strings.Join([]string{
		"SchemaFlux CommandDeck",
		"Type /help for commands.",
		"",
		"Connect your API key, then capture work in plain language.",
	}, "\n"))
}

func (s *Shell) Connect(apiKey, context string) (string, error) {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" {
		return "", fmt.Errorf("api key is required")
	}
	schemaflux.Init(trimmed)
	s.state.Context = strings.TrimSpace(context)

	var out []string
	out = append(out, "Connected to OpenAI.")
	if s.state.Context != "" {
		out = append(out, "Planning context: "+s.state.Context)
	}
	out = append(out, "")
	out = append(out, s.helpText())
	if len(s.state.Todos) > 0 {
		out = append(out, "")
		out = append(out, s.renderBoard("Current board", s.state.Todos)...)
	}
	return strings.Join(out, "\n"), nil
}

func (s *Shell) SetContext(context string) string {
	s.state.Context = strings.TrimSpace(context)
	if s.state.Context == "" {
		return "Planning context cleared."
	}
	return "Planning context updated."
}

func (s *Shell) ExportState() (string, error) {
	data, err := json.Marshal(s.state)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Shell) ImportState(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	var imported ShellState
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		return "", err
	}
	if imported.Todos == nil {
		imported.Todos = []*models.SmartTodo{}
	}
	if imported.NextID <= 0 {
		imported.NextID = inferNextID(imported.Todos)
	}
	if imported.BootedAt.IsZero() {
		imported.BootedAt = time.Now()
	}
	s.state = imported
	return fmt.Sprintf("Restored %d tasks from browser storage.", len(s.state.Todos)), nil
}

func (s *Shell) Submit(line string) (string, error) {
	command := strings.TrimSpace(line)
	if command == "" {
		return "", nil
	}

	if command == "/help" {
		return s.helpText(), nil
	}
	if command == "/clear" {
		return "__CLEAR__", nil
	}
	if strings.HasPrefix(command, "/context ") {
		value := strings.TrimSpace(strings.TrimPrefix(command, "/context"))
		return s.SetContext(value), nil
	}

	if strings.HasPrefix(command, "/") {
		return s.runSlashCommand(command)
	}
	return s.captureTodo(command)
}

func (s *Shell) runSlashCommand(command string) (string, error) {
	parts := strings.Fields(command)
	verb := parts[0]
	arg := strings.TrimSpace(strings.TrimPrefix(command, verb))

	switch verb {
	case "/board":
		filter := strings.TrimSpace(arg)
		switch strings.ToLower(filter) {
		case "", "all":
			return strings.Join(s.renderBoard("Current board", s.state.Todos), "\n"), nil
		case "hot", "ready", "done":
			return strings.Join(s.renderBoard(strings.ToUpper(filter)+" lane", s.selectBoard(filter)), "\n"), nil
		default:
			return strings.Join(s.renderBoard("Filtered board", s.filterLocal(filter)), "\n"), nil
		}
	case "/prioritize":
		todos, err := s.service.PrioritizeBoard(s.state.Todos)
		if err != nil {
			return "", err
		}
		s.state.Todos = s.mergeCompletionState(todos)
		return strings.Join(append([]string{"Board reprioritized."}, s.renderBoard("Prioritized board", s.state.Todos)...), "\n"), nil
	case "/focus":
		todo, err := s.service.RecommendNext(s.state.Todos)
		if err != nil {
			return "", err
		}
		s.state.Focus = todo
		return strings.Join(s.renderFocus(todo), "\n"), nil
	case "/review":
		review, err := s.service.BuildReview(s.state.Todos)
		if err != nil {
			return "", err
		}
		s.state.Review = &review
		return strings.Join(s.renderReview(review), "\n"), nil
	case "/plan":
		context := strings.TrimSpace(arg)
		if context == "" {
			context = s.state.Context
		}
		plan, err := s.service.PlanDay(s.state.Todos, context)
		if err != nil {
			return "", err
		}
		s.state.Plan = &plan
		if context != "" {
			s.state.Context = context
		}
		return strings.Join(s.renderPlan(plan), "\n"), nil
	case "/filter":
		query := strings.TrimSpace(arg)
		if query == "" {
			return "Usage: /filter <query>", nil
		}
		todos, err := s.service.FilterBoard(s.state.Todos, query)
		if err != nil {
			return "", err
		}
		return strings.Join(s.renderBoard("Semantic filter: "+query, todos), "\n"), nil
	case "/complete":
		return s.toggleCompletion(strings.TrimSpace(arg), true)
	case "/reopen":
		return s.toggleCompletion(strings.TrimSpace(arg), false)
	case "/drop":
		return s.dropTodo(strings.TrimSpace(arg))
	default:
		return "", fmt.Errorf("unknown command: %s", verb)
	}
}

func (s *Shell) captureTodo(note string) (string, error) {
	todo, err := s.service.CaptureTodo(note)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(todo.ID) == "" {
		todo.ID = fmt.Sprintf("%d", s.state.NextID)
		s.state.NextID++
	}
	s.state.Todos = append([]*models.SmartTodo{todo}, s.state.Todos...)
	return strings.Join(append([]string{"Captured task."}, s.renderTodo(todo)...), "\n"), nil
}

func (s *Shell) toggleCompletion(id string, completed bool) (string, error) {
	if id == "" {
		return "", fmt.Errorf("task id is required")
	}
	todo := s.findTodo(id)
	if todo == nil {
		return "", fmt.Errorf("task not found: %s", id)
	}
	todo.Completed = completed
	if completed {
		now := time.Now()
		todo.CompletedAt = &now
	} else {
		todo.CompletedAt = nil
	}
	status := "completed"
	if !completed {
		status = "reopened"
	}
	return fmt.Sprintf("%s %s", strings.Title(status), todo.Title), nil
}

func (s *Shell) dropTodo(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("task id is required")
	}
	for i, todo := range s.state.Todos {
		if todo.ID == id {
			s.state.Todos = append(s.state.Todos[:i], s.state.Todos[i+1:]...)
			return "Deleted " + todo.Title, nil
		}
	}
	return "", fmt.Errorf("task not found: %s", id)
}

func (s *Shell) helpText() string {
	return strings.Join([]string{
		"Commands",
		"  /help               show this help",
		"  /board [lane]       render board, optionally hot|ready|done",
		"  /prioritize         reprioritize the board",
		"  /focus              recommend the next task",
		"  /review             synthesize a board review",
		"  /plan [context]     produce a day plan",
		"  /filter <query>     semantic task filter",
		"  /complete <id>      mark a task done",
		"  /reopen <id>        reopen a task",
		"  /drop <id>          delete a task",
		"  /context <value>    update planning context",
		"  /clear              clear terminal output",
		"  <free text>         capture a new todo",
	}, "\n")
}

func (s *Shell) findTodo(id string) *models.SmartTodo {
	for _, todo := range s.state.Todos {
		if todo.ID == id {
			return todo
		}
	}
	return nil
}

func (s *Shell) filterLocal(query string) []*models.SmartTodo {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return cloneTodos(s.state.Todos)
	}
	filtered := []*models.SmartTodo{}
	for _, todo := range s.state.Todos {
		hay := strings.ToLower(strings.Join([]string{todo.Title, todo.Description, todo.Context, todo.Category, todo.Location}, " "))
		if strings.Contains(hay, query) {
			copy := *todo
			filtered = append(filtered, &copy)
		}
	}
	return filtered
}

func (s *Shell) selectBoard(mode string) []*models.SmartTodo {
	filtered := []*models.SmartTodo{}
	for _, todo := range s.state.Todos {
		switch mode {
		case "hot":
			if !todo.Completed && (todo.IsOverdue() || strings.EqualFold(todo.Priority, "high")) {
				copy := *todo
				filtered = append(filtered, &copy)
			}
		case "ready":
			if !todo.Completed && !todo.IsOverdue() && !strings.EqualFold(todo.Priority, "high") {
				copy := *todo
				filtered = append(filtered, &copy)
			}
		case "done":
			if todo.Completed {
				copy := *todo
				filtered = append(filtered, &copy)
			}
		}
	}
	return filtered
}

func (s *Shell) mergeCompletionState(prioritized []*models.SmartTodo) []*models.SmartTodo {
	byID := map[string]*models.SmartTodo{}
	for _, todo := range s.state.Todos {
		byID[todo.ID] = todo
	}
	merged := []*models.SmartTodo{}
	for _, todo := range prioritized {
		if existing := byID[todo.ID]; existing != nil {
			copy := *todo
			copy.Completed = existing.Completed
			copy.CompletedAt = existing.CompletedAt
			merged = append(merged, &copy)
			delete(byID, todo.ID)
		} else {
			copy := *todo
			merged = append(merged, &copy)
		}
	}
	for _, todo := range byID {
		copy := *todo
		merged = append(merged, &copy)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Completed != merged[j].Completed {
			return !merged[i].Completed
		}
		return merged[i].CreatedAt.Before(merged[j].CreatedAt)
	})
	return merged
}

func (s *Shell) renderBoard(title string, todos []*models.SmartTodo) []string {
	lines := []string{
		drawRule("="),
		title,
		s.summaryLine(),
		drawRule("-"),
	}
	lines = append(lines, s.renderLane("HOT", filterLane(todos, "hot"))...)
	lines = append(lines, s.renderLane("READY", filterLane(todos, "ready"))...)
	lines = append(lines, s.renderLane("DONE", filterLane(todos, "done"))...)
	return lines
}

func (s *Shell) renderLane(name string, todos []*models.SmartTodo) []string {
	lines := []string{fmt.Sprintf("%s %d", name, len(todos))}
	if len(todos) == 0 {
		return append(lines, "  (empty)", "")
	}
	for _, todo := range todos {
		deadline := ""
		if todo.Deadline != nil {
			deadline = " | due " + todo.Deadline.Format("Jan 2")
		}
		lines = append(lines, fmt.Sprintf("  [%s] %s | %s | %s%s", todo.ID, strings.ToUpper(todo.Priority), todo.Title, todo.Category, deadline))
		lines = append(lines, fmt.Sprintf("       %s | %s", todo.Location, todo.Effort))
		if strings.TrimSpace(todo.Context) != "" {
			lines = append(lines, "       "+truncate(todo.Context, 100))
		}
		if len(todo.Tasks) > 0 {
			lines = append(lines, fmt.Sprintf("       subtasks %d/%d", completedTasks(todo.Tasks), len(todo.Tasks)))
		}
	}
	return append(lines, "")
}

func (s *Shell) renderTodo(todo *models.SmartTodo) []string {
	lines := []string{
		fmt.Sprintf("ID: %s", todo.ID),
		fmt.Sprintf("Title: %s", todo.Title),
		fmt.Sprintf("Priority: %s | Category: %s | Effort: %s | Location: %s", todo.Priority, todo.Category, todo.Effort, todo.Location),
	}
	if strings.TrimSpace(todo.Context) != "" {
		lines = append(lines, "Context: "+truncate(todo.Context, 100))
	}
	if len(todo.Tasks) > 0 {
		lines = append(lines, "Subtasks:")
		for _, task := range todo.Tasks {
			marker := "[ ]"
			if task.Completed {
				marker = "[x]"
			}
			lines = append(lines, fmt.Sprintf("  %s %s", marker, task.Text))
		}
	}
	return lines
}

func (s *Shell) renderFocus(todo *models.SmartTodo) []string {
	if todo == nil {
		return []string{"No focus recommendation returned."}
	}
	return append([]string{"Recommended start"}, s.renderTodo(todo)...)
}

func (s *Shell) renderReview(review intelligence.BoardReview) []string {
	lines := []string{
		drawRule("-"),
		"Board review",
		review.Summary,
		"",
		"Focus areas:",
	}
	lines = append(lines, prefixList(review.FocusAreas)...)
	lines = append(lines, "", "Quick wins:")
	lines = append(lines, prefixList(review.QuickWins)...)
	lines = append(lines, "", "Risks:")
	lines = append(lines, prefixList(review.Risks)...)
	lines = append(lines, "", "Recommended moves:")
	lines = append(lines, prefixList(review.RecommendedMoves)...)
	return lines
}

func (s *Shell) renderPlan(plan intelligence.DayPlan) []string {
	lines := []string{
		drawRule("-"),
		"Day plan",
		plan.Headline,
	}
	if plan.RecommendedStart != "" {
		lines = append(lines, "Recommended start: "+plan.RecommendedStart)
	}
	lines = append(lines, "", "Blocks:")
	if len(plan.Blocks) == 0 {
		lines = append(lines, "  - no plan blocks returned")
	} else {
		for idx, block := range plan.Blocks {
			lines = append(lines, fmt.Sprintf("  %d. %s | %s", idx+1, block.Label, block.Window))
			lines = append(lines, "     "+truncate(block.Goal, 100))
		}
	}
	lines = append(lines, "", "Quick wins:")
	lines = append(lines, prefixList(plan.QuickWins)...)
	lines = append(lines, "", "Risks:")
	lines = append(lines, prefixList(plan.Risks)...)
	return lines
}

func (s *Shell) summaryLine() string {
	total := len(s.state.Todos)
	open := 0
	done := 0
	overdue := 0
	for _, todo := range s.state.Todos {
		if todo.Completed {
			done++
		} else {
			open++
		}
		if todo.IsOverdue() {
			overdue++
		}
	}
	return fmt.Sprintf("total %d | open %d | done %d | overdue %d", total, open, done, overdue)
}

func filterLane(todos []*models.SmartTodo, lane string) []*models.SmartTodo {
	out := []*models.SmartTodo{}
	for _, todo := range todos {
		switch lane {
		case "hot":
			if !todo.Completed && (todo.IsOverdue() || strings.EqualFold(todo.Priority, "high")) {
				copy := *todo
				out = append(out, &copy)
			}
		case "ready":
			if !todo.Completed && !todo.IsOverdue() && !strings.EqualFold(todo.Priority, "high") {
				copy := *todo
				out = append(out, &copy)
			}
		case "done":
			if todo.Completed {
				copy := *todo
				out = append(out, &copy)
			}
		}
	}
	return out
}

func cloneTodos(todos []*models.SmartTodo) []*models.SmartTodo {
	out := make([]*models.SmartTodo, 0, len(todos))
	for _, todo := range todos {
		copy := *todo
		out = append(out, &copy)
	}
	return out
}

func prefixList(items []string) []string {
	if len(items) == 0 {
		return []string{"  - none"}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, "  - "+item)
	}
	return out
}

func completedTasks(tasks []models.Task) int {
	count := 0
	for _, task := range tasks {
		if task.Completed {
			count++
		}
	}
	return count
}

func inferNextID(todos []*models.SmartTodo) int {
	maxID := 0
	for _, todo := range todos {
		var id int
		fmt.Sscanf(todo.ID, "%d", &id)
		if id > maxID {
			maxID = id
		}
	}
	return maxID + 1
}

func drawRule(char string) string {
	if char == "" {
		char = "-"
	}
	return strings.Repeat(char, 72)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
