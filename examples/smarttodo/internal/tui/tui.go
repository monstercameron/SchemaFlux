package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/database"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/processor"
	"github.com/monstercameron/schemaflux/examples/smarttodo/pkg/notifier"
)

// View modes
type viewMode int

const (
	setupView  viewMode = iota
	splashView          // Welcome splash screen
	listView
	idleView // Idle mode summary view
	taskView // Inline task view for managing subtasks
	addView
	editView // Edit todo with context modal
	detailView
	suggestView
	statsView
	quitConfirmView
	closingView     // Closing animation view
	calendarView    // Daily calendar view with time slots
	apiKeySetupView // API key setup for first-time users
)

// Model represents the main TUI application state
type Model struct {
	db               *database.Database
	processor        *processor.TodoProcessor
	notifier         *notifier.Notifier
	todos            []*models.SmartTodo
	list             list.Model
	input            textinput.Model
	textarea         textarea.Model
	help             help.Model
	keys             keyMap
	mode             viewMode
	previousMode     viewMode // Store previous mode for returning from quit confirm
	selectedTodo     *models.SmartTodo
	selectedTask     int             // Index of selected task in task edit view
	newTasks         []string        // Temporary storage for new tasks being added
	taskInput        textinput.Model // Input for adding tasks
	taskInputMode    bool            // Whether we're in input mode in task view
	pendingTasks     []string        // Queue of tasks being processed for grammar fixing
	processingTask   bool            // Whether we're processing a task
	editInput        textinput.Model // Input for edit modal
	closingFrame     int             // Animation frame for closing
	closingProgress  int             // Progress for closing animation
	idleMode         bool            // Idle mode to save CPU
	lastActivity     time.Time       // Track last user activity
	statusMsg        string
	statusType       string // "success", "error", "info"
	width            int
	height           int
	loading          bool
	stats            map[string]int
	userName         string
	listTitle        string
	pendingTodos     []string        // Queue of todos being processed
	loadingFrame     int             // For animation
	setupInput       textinput.Model // For initial setup
	consoleLogs      []string        // Store console messages
	maxLogs          int             // Maximum number of logs to keep
	needsAPIKey      bool            // Whether API key setup is needed
	aiQuote          string          // AI-generated motivational quote for idle mode
	editProcessing   bool            // Whether edit is being processed with AI
	lastFilterString string          // Store last filter string to restore
}

// InitialModel creates the initial TUI model
func InitialModel(db *database.Database) Model {
	// Check if user preferences exist in database
	userName, listTitle, _ := db.GetUserPrefs()

	// Create list with custom delegate
	items := []list.Item{}
	delegate := itemDelegate{}
	l := list.New(items, delegate, 0, 0)
	l.Title = "" // Remove the title from the list
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = titleStyle
	l.Styles.FilterPrompt = focusedStyle
	l.Styles.FilterCursor = focusedStyle
	l.KeyMap.CursorUp.SetKeys("up", "k")
	l.KeyMap.CursorDown.SetKeys("down", "j")

	// Create input
	ti := textinput.New()
	ti.Placeholder = "What do you need to do?"
	ti.CharLimit = 200
	ti.Width = 60

	// Create textarea for longer input
	ta := textarea.New()
	ta.Placeholder = "Describe your task in natural language..."
	ta.SetWidth(60)
	ta.SetHeight(4)

	// Create task input for subtasks
	taskInput := textinput.New()
	taskInput.Placeholder = "Enter a subtask..."
	taskInput.CharLimit = 100
	taskInput.Width = 40
	taskInput.TextStyle = focusedStyle
	taskInput.PromptStyle = focusedStyle

	// Create setup input
	setupInput := textinput.New()
	setupInput.Placeholder = "Enter your name..."
	setupInput.CharLimit = 50
	setupInput.Width = 40

	// Create edit input
	editInput := textinput.New()
	editInput.Placeholder = "Add context or updates to this todo..."
	editInput.CharLimit = 200
	editInput.Width = 60

	initialMode := splashView // Always start with splash
	if userName == "" {
		// Will transition to setup after splash
		setupInput.Focus()
	}

	m := Model{
		db:           db,
		processor:    processor.NewTodoProcessor(),
		notifier:     notifier.NewNotifier(),
		list:         l,
		input:        ti,
		textarea:     ta,
		taskInput:    taskInput,
		newTasks:     []string{},
		help:         help.New(),
		keys:         keys,
		mode:         initialMode,
		statusType:   "info",
		stats:        make(map[string]int),
		userName:     userName,
		listTitle:    listTitle,
		pendingTodos: []string{},
		setupInput:   setupInput,
		editInput:    editInput,
		consoleLogs:  []string{},
		maxLogs:      5,
		lastActivity: time.Now(),
		idleMode:     false,
	}

	return m
}

// SetNeedsAPIKey sets whether API key setup is needed
func (m *Model) SetNeedsAPIKey(needs bool) {
	m.needsAPIKey = needs
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.updateDeadlines(),
		m.loadTodos(),
		m.loadStats(),
		splashTimerCmd(), // Start splash timer
		idleCheckCmd(),   // Start idle checking
	)
}

// Commands
func (m *Model) loadTodos() tea.Cmd {
	return func() tea.Msg {
		// Load ALL todos, not just pending ones
		// The query already sorts them with uncompleted first
		todos, err := m.db.GetAllTodos()
		if err != nil {
			return models.ErrMsg{Err: err}
		}
		return models.TodosLoadedMsg{Todos: todos}
	}
}

func (m *Model) loadStats() tea.Cmd {
	return func() tea.Msg {
		stats, err := m.db.GetStats()
		if err != nil {
			return models.ErrMsg{Err: err}
		}
		return models.StatsLoadedMsg{Stats: stats}
	}
}

func (m *Model) updateDeadlines() tea.Cmd {
	return func() tea.Msg {
		// Run deadline updates in the background
		err := m.processor.UpdateDeadlines(m.db)
		if err != nil {
			// Log but don't fail - deadlines update is not critical
			return models.DeadlineUpdateMsg{Success: false, Message: fmt.Sprintf("Failed to update deadlines: %v", err)}
		}
		return models.DeadlineUpdateMsg{Success: true, Message: "Deadlines checked and updated"}
	}
}

func (m *Model) processTodo(input string) tea.Cmd {
	return func() tea.Msg {
		todo, err := m.processor.ProcessNote(input)
		if err != nil {
			return models.ErrMsg{Err: err}
		}

		id, err := m.db.AddTodo(todo)
		if err != nil {
			return models.ErrMsg{Err: err}
		}

		todo.ID = strconv.FormatInt(id, 10)
		return models.TodoAddedMsg{Todo: todo}
	}
}

func (m *Model) suggestNextTodo() tea.Cmd {
	return func() tea.Msg {
		if len(m.todos) == 0 {
			return models.ErrMsg{Err: fmt.Errorf("no pending tasks")}
		}

		best, err := m.processor.SuggestNext(m.todos)
		if err != nil {
			return models.ErrMsg{Err: err}
		}

		return models.TodoSuggestedMsg{Todo: best}
	}
}

func (m *Model) processEditContext(todo *models.SmartTodo, context string) tea.Cmd {
	return func() tea.Msg {
		updatedTodo, err := m.processor.ProcessEditContext(todo, context)
		if err != nil {
			return models.EditProcessedMsg{Todo: todo, Err: err}
		}

		// Save to database
		err = m.db.UpdateTodo(updatedTodo)
		if err != nil {
			return models.EditProcessedMsg{Todo: todo, Err: err}
		}

		return models.EditProcessedMsg{Todo: updatedTodo, Err: nil}
	}
}

func (m *Model) smartPrioritize() tea.Cmd {
	return func() tea.Msg {
		prioritized, err := m.processor.SmartPrioritize(m.todos)
		if err != nil {
			return models.SmartPrioritizeMsg{Todos: m.todos, Err: err}
		}
		return models.SmartPrioritizeMsg{Todos: prioritized, Err: nil}
	}
}

func (m *Model) processTaskGrammar(taskText string, todoID string) tea.Cmd {
	return func() tea.Msg {
		fixed, err := m.processor.FixTaskGrammar(taskText)
		if err != nil {
			// Use simple refinement as fallback
			fixed = m.processor.RefineTaskText(taskText)
		}

		return models.TaskProcessedMsg{
			OriginalText: taskText,
			FixedText:    fixed,
			TodoID:       todoID,
		}
	}
}

// generateQuoteCmd generates a new AI quote asynchronously
func (m *Model) generateQuoteCmd() tea.Cmd {
	return func() tea.Msg {
		quote, err := processor.GenerateAIQuote()
		if err != nil {
			// Use fallback quote on error
			return models.AiQuoteMsg{Quote: "\"The secret of getting ahead is getting started.\" - Mark Twain"}
		}
		return models.AiQuoteMsg{Quote: quote}
	}
}

// Update
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Ensure minimum dimensions to prevent rendering issues
		m.width = msg.Width
		m.height = msg.Height
		if m.width < 80 {
			m.width = 80
		}
		if m.height < 24 {
			m.height = 24
		}
		// Update list size with safe dimensions
		listWidth := m.width - 4
		listHeight := m.height - 20 // Account for all UI elements
		if listHeight < 5 {
			listHeight = 5
		}
		m.list.SetSize(listWidth, listHeight)
		return m, nil

	case models.TodosLoadedMsg:
		m.todos = msg.Todos
		items := make([]list.Item, len(msg.Todos))
		for i, todo := range msg.Todos {
			items[i] = todoItem{todo: todo}
		}
		m.list.SetItems(items)
		m.loading = false
		// Force a refresh to ensure proper rendering
		return m, tea.ClearScreen

	case models.DeadlineUpdateMsg:
		if msg.Success {
			m.addLog("📅 Deadlines checked for updates")
		} else {
			m.addLog("⚠️ " + msg.Message)
		}
		return m, nil

	case models.StatsLoadedMsg:
		m.stats = msg.Stats
		return m, nil

	case models.TodoAddedMsg:
		// Remove the placeholder todo from the list
		newTodos := []*models.SmartTodo{}
		for _, todo := range m.todos {
			if !strings.HasPrefix(todo.ID, "pending-") {
				newTodos = append(newTodos, todo)
			}
		}
		m.todos = newTodos

		// Remove from pending queue
		if len(m.pendingTodos) > 0 {
			m.pendingTodos = m.pendingTodos[1:]
		}

		m.statusMsg = fmt.Sprintf("✅ Added: %s", msg.Todo.Title)
		m.statusType = "success"
		m.addLog(fmt.Sprintf("✅ Added: %s", msg.Todo.Title))

		// Log cost if tracked
		if m.processor != nil && m.processor.LastCost > 0 {
			m.addLog(fmt.Sprintf("💰 Cost: $%.4f (Total: $%.4f)", m.processor.LastCost, m.processor.TotalCost))
		}

		// Process next in queue if any
		var nextCmd tea.Cmd
		if len(m.pendingTodos) > 0 {
			// Add placeholder for next pending item
			placeholderTodo := &models.SmartTodo{
				ID:          fmt.Sprintf("pending-%d", time.Now().Unix()),
				Title:       fmt.Sprintf("⏳ Processing: %.30s...", m.pendingTodos[0]),
				Description: "🤖 AI is analyzing your task...",
				Priority:    "medium",
				Category:    "pending",
				CreatedAt:   time.Now(),
			}
			m.todos = append([]*models.SmartTodo{placeholderTodo}, m.todos...)

			nextCmd = m.processTodo(m.pendingTodos[0])
			m.statusMsg = fmt.Sprintf("⏳ Processing %d more tasks...", len(m.pendingTodos))
			m.addLog(fmt.Sprintf("⏳ Processing %d more tasks...", len(m.pendingTodos)))
		} else {
			m.loading = false
		}
		// Always refresh and clear screen to ensure proper redraw
		return m, tea.Batch(m.loadTodos(), m.loadStats(), nextCmd, tea.ClearScreen)

	case models.TodoProcessingMsg:
		// Add to queue and start processing if not already
		m.pendingTodos = append(m.pendingTodos, msg.Input)
		m.addLog(fmt.Sprintf("📋 Queued task: %.50s...", msg.Input))

		// Add a placeholder todo to the list immediately
		placeholderTodo := &models.SmartTodo{
			ID:          fmt.Sprintf("pending-%d", time.Now().Unix()),
			Title:       fmt.Sprintf("⏳ Processing: %.30s...", msg.Input),
			Description: "🤖 AI is analyzing your task...",
			Priority:    "medium",
			Category:    "pending",
			CreatedAt:   time.Now(),
		}

		// Add placeholder to the beginning of the todos list
		m.todos = append([]*models.SmartTodo{placeholderTodo}, m.todos...)

		// Update the list view
		items := make([]list.Item, len(m.todos))
		for i, todo := range m.todos {
			items[i] = todoItem{todo: todo}
		}
		m.list.SetItems(items)

		if len(m.pendingTodos) == 1 {
			m.loading = true
			m.addLog("🤖 Starting AI processing...")
			return m, tea.Batch(m.processTodo(msg.Input), tickCmd())
		}
		m.statusMsg = fmt.Sprintf("📋 Queued (%d tasks pending)", len(m.pendingTodos))
		m.statusType = "info"
		return m, tickCmd()

	case models.IdleCheckMsg:
		// Check if we should enter idle mode (no activity for 1 minute)
		if time.Since(m.lastActivity) > 1*time.Minute && !m.idleMode && m.mode == listView {
			m.idleMode = true
			m.previousMode = m.mode
			m.mode = idleView
			m.addLog("💤 Entering idle mode (power saving)")
			// Generate AI quote and continue with slow tick for idle animation
			return m, tea.Batch(
				m.generateQuoteCmd(),
				tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
					return models.TickMsg(t)
				}),
			)
		}
		// Check every 5 seconds when active, every 30 seconds when idle
		checkInterval := 5 * time.Second
		if m.idleMode {
			checkInterval = 30 * time.Second
		}
		return m, tea.Tick(checkInterval, func(t time.Time) tea.Msg {
			return models.IdleCheckMsg{}
		})

	case models.WakeupMsg:
		// Wake up from idle mode
		if m.idleMode {
			m.idleMode = false
			m.lastActivity = time.Now()
			m.aiQuote = "" // Clear quote for next idle session
			m.addLog("⚡ Waking up from idle mode")
			// Resume normal tick rate for animations
			return m, tickCmd()
		}
		return m, nil

	case models.AiQuoteMsg:
		// Store the AI-generated quote
		m.aiQuote = msg.Quote
		m.addLog("💭 Generated motivational quote")
		return m, nil

	case models.SmartPrioritizeMsg:
		// Handle smart prioritization result
		m.loading = false
		if msg.Err != nil {
			m.statusMsg = fmt.Sprintf("❌ Prioritization failed: %v", msg.Err)
			m.statusType = "error"
			m.addLog(fmt.Sprintf("❌ Failed to prioritize: %v", msg.Err))
		} else {
			m.todos = msg.Todos
			m.statusMsg = "✨ Tasks reordered by AI priority"
			m.statusType = "success"
			m.addLog(fmt.Sprintf("✨ Reordered %d tasks with AI", len(msg.Todos)))

			// Log cost for smart prioritization
			if m.processor != nil && m.processor.LastCost > 0 {
				m.addLog(fmt.Sprintf("💰 Cost: $%.4f (Total: $%.4f)", m.processor.LastCost, m.processor.TotalCost))
			}

			// Update list items with new order
			items := make([]list.Item, len(msg.Todos))
			for i, todo := range msg.Todos {
				items[i] = todoItem{todo: todo}
			}
			m.list.SetItems(items)
		}
		return m, tea.ClearScreen

	case models.EditProcessedMsg:
		// Handle edit processing result
		m.editProcessing = false
		if msg.Err != nil {
			m.statusMsg = fmt.Sprintf("❌ Edit failed: %v", msg.Err)
			m.statusType = "error"
			m.addLog(fmt.Sprintf("❌ Edit failed: %v", msg.Err))
		} else {
			m.statusMsg = fmt.Sprintf("✅ Updated: %s", msg.Todo.Title)
			m.statusType = "success"
			m.addLog(fmt.Sprintf("✅ Updated todo with AI: %s", msg.Todo.Title))

			// Log cost for edit processing
			if m.processor != nil && m.processor.LastCost > 0 {
				m.addLog(fmt.Sprintf("💰 Cost: $%.4f (Total: $%.4f)", m.processor.LastCost, m.processor.TotalCost))
			}
			m.selectedTodo = nil
			m.mode = listView
			m.editInput.SetValue("")
		}
		return m, m.loadTodos()

	case models.TickMsg:
		// Don't animate if in idle mode
		if m.idleMode {
			return m, nil
		}

		// Update loading animation
		m.loadingFrame++

		// Update placeholder todos with animation
		if len(m.pendingTodos) > 0 {
			spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			frame := spinner[m.loadingFrame%len(spinner)]

			// Update any pending todos in the list
			for _, todo := range m.todos {
				if strings.HasPrefix(todo.ID, "pending-") {
					// Update the title with spinning animation
					originalInput := m.pendingTodos[0] // Get the first pending input
					if len(originalInput) > 30 {
						originalInput = originalInput[:30] + "..."
					}
					todo.Title = fmt.Sprintf("%s Processing: %s", frame, originalInput)

					// Cycle through different status messages
					statusMessages := []string{
						"🤖 AI is analyzing your task...",
						"🔍 Extracting task details...",
						"📝 Setting priorities...",
						"⚡ Almost ready...",
					}
					todo.Description = statusMessages[(m.loadingFrame/10)%len(statusMessages)]
				}
			}

			// Update the list view
			items := make([]list.Item, len(m.todos))
			for i, todo := range m.todos {
				items[i] = todoItem{todo: todo}
			}
			m.list.SetItems(items)

			return m, tickCmd()
		}

		if m.loading {
			return m, tickCmd()
		}
		return m, nil

	case models.SetupCompleteMsg:
		m.userName = msg.UserName
		m.listTitle = msg.ListTitle
		// Save to database instead of file
		m.db.SaveUserPrefs(msg.UserName, msg.ListTitle)
		m.mode = listView
		return m, tea.Batch(m.loadTodos(), m.loadStats())

	case models.TodoSuggestedMsg:
		m.selectedTodo = msg.Todo
		m.mode = suggestView
		m.statusMsg = "💡 AI Recommendation"
		m.statusType = "info"
		m.loading = false
		m.addLog(fmt.Sprintf("💡 AI suggests: %s", msg.Todo.Title))

		// Log cost for suggestion
		if m.processor != nil && m.processor.LastCost > 0 {
			m.addLog(fmt.Sprintf("💰 Cost: $%.4f (Total: $%.4f)", m.processor.LastCost, m.processor.TotalCost))
		}
		return m, tea.ClearScreen

	case models.TaskProcessedMsg:
		// Find and update the placeholder task
		if m.selectedTodo != nil && m.selectedTodo.ID == msg.TodoID {
			// Find the placeholder task (should be the last one or recently added)
			for i := len(m.selectedTodo.Tasks) - 1; i >= 0; i-- {
				if strings.HasPrefix(m.selectedTodo.Tasks[i].Text, "⏳ Processing:") {
					// Replace with fixed text
					m.selectedTodo.Tasks[i].Text = msg.FixedText
					break
				}
			}

			// Save to database
			err := m.db.UpdateTodo(m.selectedTodo)
			if err != nil {
				m.statusMsg = fmt.Sprintf("❌ Failed to save task: %v", err)
				m.statusType = "error"
			} else {
				m.statusMsg = fmt.Sprintf("✅ Added: %s", msg.FixedText)
				m.statusType = "success"
				m.addLog(fmt.Sprintf("✅ Added task: %s", msg.FixedText))
			}

			// Remove from pending queue
			if len(m.pendingTasks) > 0 {
				m.pendingTasks = m.pendingTasks[1:]
			}

			// Process next task if any
			if len(m.pendingTasks) > 0 {
				return m, m.processTaskGrammar(m.pendingTasks[0], m.selectedTodo.ID)
			} else {
				m.processingTask = false
			}

			return m, m.loadTodos()
		}
		return m, nil

	case models.StartClosingMsg:
		// External signal to start closing
		if m.mode != closingView {
			m.mode = closingView
			m.closingFrame = 0
			m.closingProgress = 0
			if m.db != nil {
				m.db.Close()
			}
			return m, closingTickCmd()
		}
		return m, nil

	case models.ClosingTickMsg:
		// Update closing animation
		m.closingFrame++
		m.closingProgress += 5

		if m.closingProgress >= 100 {
			// Animation complete, quit
			return m, tea.Quit
		}

		return m, closingTickCmd()

	case models.SplashDismissMsg:
		// Transition from splash to appropriate view
		if m.needsAPIKey {
			m.mode = apiKeySetupView
			m.setupInput.Placeholder = "sk-..."
			m.setupInput.CharLimit = 100
			m.setupInput.Focus()
		} else if m.userName == "" {
			m.mode = setupView
			m.setupInput.Focus()
		} else {
			m.mode = listView
		}
		return m, tea.ClearScreen

	case models.ErrMsg:
		m.statusMsg = fmt.Sprintf("❌ Error: %v", msg.Err)
		m.statusType = "error"
		m.loading = false
		m.processingTask = false
		m.addLog(fmt.Sprintf("❌ Error: %v", msg.Err))
		return m, nil

	case tea.KeyMsg:
		// Track user activity and wake from idle
		m.lastActivity = time.Now()
		if m.idleMode {
			m.idleMode = false
			m.mode = m.previousMode
			m.aiQuote = "" // Clear quote for next idle session
			m.addLog("⚡ Waking up from idle mode")
			cmds = append(cmds, tickCmd(), idleCheckCmd())
			return m, tea.Batch(cmds...)
		}

		// Global keys
		if msg.Type == tea.KeyCtrlC {
			// Start closing animation for Ctrl+C
			if m.mode != closingView {
				m.mode = closingView
				m.closingFrame = 0
				m.closingProgress = 0
				if m.db != nil {
					m.db.Close()
				}
				return m, closingTickCmd()
			}
			return m, nil
		}

		// Force refresh on Ctrl+L (standard terminal clear)
		if msg.Type == tea.KeyCtrlL {
			return m, tea.ClearScreen
		}

		switch m.mode {
		case splashView:
			// Allow Enter or any key to skip splash
			if msg.Type == tea.KeyEnter || msg.Type == tea.KeySpace {
				return m, func() tea.Msg { return models.SplashDismissMsg{} }
			}
			return m, nil

		case apiKeySetupView:
			switch {
			case msg.Type == tea.KeyEsc:
				// Exit on Esc
				return m, tea.Quit
			case msg.Type == tea.KeyEnter:
				apiKey := m.setupInput.Value()
				if apiKey != "" {
					// Validate the API key
					m.statusMsg = "Validating API key..."
					m.statusType = "info"

					// Test the API key
					if err := validateAPIKey(apiKey); err != nil {
						m.statusMsg = "Invalid API key. Please check and try again."
						m.statusType = "error"
						return m, nil
					}

					// Save the API key
					if err := saveAPIKey(apiKey); err != nil {
						m.statusMsg = fmt.Sprintf("Failed to save API key: %v", err)
						m.statusType = "error"
						return m, nil
					}

					// API key is valid, proceed to setup or list view
					m.needsAPIKey = false
					if m.userName == "" {
						m.mode = setupView
						m.setupInput.SetValue("")
						m.setupInput.Placeholder = "Enter your name..."
						m.setupInput.CharLimit = 50
					} else {
						m.mode = listView
						return m, tea.Batch(m.loadTodos(), m.loadStats())
					}
				}
				return m, nil
			default:
				var cmd tea.Cmd
				m.setupInput, cmd = m.setupInput.Update(msg)
				return m, cmd
			}

		case setupView:
			switch {
			case msg.Type == tea.KeyEnter:
				if m.setupInput.Value() != "" {
					if m.userName == "" {
						// First step - get name
						m.userName = m.setupInput.Value()
						m.setupInput.SetValue("")
						m.setupInput.Placeholder = "Name your todo list (e.g., 'Work Tasks', 'Personal Goals')..."
						return m, nil
					} else {
						// Second step - get list title
						listTitle := m.setupInput.Value()
						return m, func() tea.Msg {
							return models.SetupCompleteMsg{UserName: m.userName, ListTitle: listTitle}
						}
					}
				}
			default:
				var cmd tea.Cmd
				m.setupInput, cmd = m.setupInput.Update(msg)
				return m, cmd
			}

		case listView:
			// Check if we're in filter mode and handle Left arrow to exit
			if m.list.SettingFilter() && msg.Type == tea.KeyLeft {
				// Save the current filter string before resetting
				m.lastFilterString = m.list.FilterInput.Value()
				m.list.ResetFilter()
				m.addLog("🔍 Exited filter mode (filter saved)")
				return m, nil
			}

			switch {
			case msg.Type == tea.KeyEsc || key.Matches(msg, m.keys.Quit):
				m.previousMode = m.mode
				m.mode = quitConfirmView
				return m, nil
			case key.Matches(msg, m.keys.Add):
				m.mode = addView
				m.input.Focus()
				m.addLog("📝 Opening add todo modal...")
				return m, textinput.Blink
			case key.Matches(msg, m.keys.Suggest):
				m.loading = true
				m.addLog("🤖 Getting AI suggestion...")
				return m, tea.Batch(m.suggestNextTodo(), tickCmd())
			case key.Matches(msg, m.keys.Stats):
				m.mode = statsView
				m.addLog("📊 Viewing statistics...")
				return m, tea.ClearScreen
			case key.Matches(msg, m.keys.Prioritize):
				// Ctrl+R for smart prioritization
				m.loading = true
				m.statusMsg = "🤖 Analyzing tasks with AI..."
				m.statusType = "info"
				m.addLog("🤖 Smart prioritizing tasks...")
				return m, tea.Batch(m.smartPrioritize(), tickCmd())
			case key.Matches(msg, m.keys.Calendar):
				// Ctrl+P for calendar view
				m.mode = calendarView
				m.addLog("📅 Viewing calendar...")
				return m, tea.ClearScreen
			case key.Matches(msg, m.keys.UpdateKey):
				// Ctrl+K to update API key
				m.mode = apiKeySetupView
				m.setupInput.SetValue("")
				m.setupInput.Placeholder = "sk-..."
				m.setupInput.CharLimit = 100
				m.setupInput.Focus()
				m.addLog("🔑 Updating API key...")
				return m, tea.ClearScreen
			case msg.String() == "f" && !m.list.SettingFilter() && m.lastFilterString != "":
				// Restore last filter with 'f' key
				m.list.FilterInput.SetValue(m.lastFilterString)
				m.addLog(fmt.Sprintf("🔍 Restored filter: %s", m.lastFilterString))
				// Trigger filter mode
				filterCmd := func() tea.Msg {
					return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
				}
				cmds = append(cmds, filterCmd)
				return m, tea.Batch(cmds...)
			case msg.Type == tea.KeySpace || key.Matches(msg, m.keys.Complete):
				// Space bar or Ctrl+X marks as complete (toggle)
				if i, ok := m.list.SelectedItem().(todoItem); ok {
					id, _ := strconv.Atoi(i.todo.ID)
					if i.todo.Completed {
						// Uncomplete the task
						m.db.UncompleteTodo(id)
						m.statusMsg = fmt.Sprintf("⬜ Reopened: %s", i.todo.Title)
						m.statusType = "info"
						m.addLog(fmt.Sprintf("⬜ Reopened: %s", i.todo.Title))
					} else {
						// Complete the task
						m.db.CompleteTodo(id)
						m.statusMsg = fmt.Sprintf("✅ Completed: %s", i.todo.Title)
						m.statusType = "success"
						m.addLog(fmt.Sprintf("✅ Completed: %s", i.todo.Title))
					}
					// Force refresh with clear screen
					return m, tea.Batch(m.loadTodos(), m.loadStats(), tea.ClearScreen)
				}
			case key.Matches(msg, m.keys.Edit):
				// Ctrl+E opens edit modal for selected todo
				if i, ok := m.list.SelectedItem().(todoItem); ok {
					// Don't edit pending items
					if strings.HasPrefix(i.todo.ID, "pending-") {
						m.statusMsg = "⚠️ Cannot edit a task that's being processed"
						m.statusType = "warning"
						return m, nil
					}

					m.selectedTodo = i.todo
					m.mode = editView
					m.editInput.SetValue("")
					m.editInput.Focus()
					m.addLog(fmt.Sprintf("✏️ Editing: %s", i.todo.Title))
					return m, textinput.Blink
				}
				return m, nil
			case key.Matches(msg, m.keys.Delete):
				// Get the currently selected item from the list
				selectedIndex := m.list.Index()
				items := m.list.Items()

				if selectedIndex >= 0 && selectedIndex < len(items) {
					if todoItem, ok := items[selectedIndex].(todoItem); ok {
						// Don't delete pending items that are being processed
						if strings.HasPrefix(todoItem.todo.ID, "pending-") {
							m.statusMsg = "⚠️ Cannot delete a task that's being processed"
							m.statusType = "warning"
							return m, nil
						}

						// Actually delete the todo from database
						id, err := strconv.Atoi(todoItem.todo.ID)
						if err != nil {
							m.statusMsg = fmt.Sprintf("❌ Invalid todo ID: %s", todoItem.todo.ID)
							m.statusType = "error"
							return m, nil
						}

						err = m.db.DeleteTodo(id)
						if err != nil {
							m.statusMsg = fmt.Sprintf("❌ Failed to delete: %v", err)
							m.statusType = "error"
							m.addLog(fmt.Sprintf("❌ Delete failed: %v", err))
						} else {
							m.statusMsg = fmt.Sprintf("🗑️ Deleted: %s", todoItem.todo.Title)
							m.statusType = "success"
							m.addLog(fmt.Sprintf("🗑️ Deleted: %s", todoItem.todo.Title))
						}
						// Force refresh with clear screen
						return m, tea.Batch(m.loadTodos(), m.loadStats(), tea.ClearScreen)
					}
				}
			case key.Matches(msg, m.keys.Enter):
				// Enter now marks as complete (toggle)
				if i, ok := m.list.SelectedItem().(todoItem); ok {
					id, _ := strconv.Atoi(i.todo.ID)
					if i.todo.Completed {
						// Uncomplete the task
						m.db.UncompleteTodo(id)
						m.statusMsg = fmt.Sprintf("⬜ Uncompleted: %s", i.todo.Title)
						m.statusType = "info"
						m.addLog(fmt.Sprintf("⬜ Reopened: %s", i.todo.Title))
					} else {
						// Complete the task
						m.db.CompleteTodo(id)
						m.statusMsg = fmt.Sprintf("✅ Completed: %s", i.todo.Title)
						m.statusType = "success"
						m.addLog(fmt.Sprintf("✅ Completed: %s", i.todo.Title))
					}
					// Force refresh with clear screen
					return m, tea.Batch(m.loadTodos(), m.loadStats(), tea.ClearScreen)
				}
			case key.Matches(msg, m.keys.Detail):
				if i, ok := m.list.SelectedItem().(todoItem); ok {
					m.selectedTodo = i.todo
					m.mode = detailView
					m.addLog(fmt.Sprintf("🔍 Viewing: %s", i.todo.Title))
					return m, tea.ClearScreen
				}
				return m, nil
			case msg.Type == tea.KeyRight:
				// Right arrow enters subtask mode - always allow to add/manage tasks
				if i, ok := m.list.SelectedItem().(todoItem); ok {
					m.selectedTodo = i.todo
					m.selectedTask = 0
					m.mode = taskView
					if len(i.todo.Tasks) > 0 {
						m.addLog(fmt.Sprintf("📋 Managing subtasks for: %s", i.todo.Title))
					} else {
						m.addLog(fmt.Sprintf("📋 Entering task mode for: %s (Press Ctrl+A to add tasks)", i.todo.Title))
					}
					return m, nil
				}
				return m, nil
			}

		case addView:
			switch {
			case msg.Type == tea.KeyEsc:
				m.mode = listView
				m.input.SetValue("")
				m.input.Blur()
				return m, nil
			case msg.Type == tea.KeyEnter:
				// Enter to save
				input := m.input.Value()
				if strings.TrimSpace(input) != "" {
					m.mode = listView
					m.input.SetValue("")
					m.input.Blur()
					m.addLog(fmt.Sprintf("📥 Adding: %.50s...", input))
					return m, tea.Batch(
						func() tea.Msg { return models.TodoProcessingMsg{Input: input} },
						tickCmd(),
					)
				}
				return m, nil
			}

			// Update input field
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd

		case editView:
			switch {
			case msg.Type == tea.KeyEsc:
				// Cancel edit
				m.mode = listView
				m.selectedTodo = nil
				m.editInput.SetValue("")
				m.editInput.Blur()
				return m, nil
			case msg.Type == tea.KeyEnter:
				// Process edit with AI
				context := m.editInput.Value()
				if strings.TrimSpace(context) != "" && m.selectedTodo != nil {
					// Start processing animation
					m.editProcessing = true
					m.statusMsg = "🤖 Processing edit with AI..."
					m.statusType = "info"
					m.addLog(fmt.Sprintf("🤖 Processing edit for: %s", m.selectedTodo.Title))

					// Keep the edit modal open but disable input
					m.editInput.Blur()

					// Process the edit asynchronously
					return m, tea.Batch(
						m.processEditContext(m.selectedTodo, context),
						tickCmd(),
					)
				}
				return m, nil
			}
			// Update input field
			var cmd tea.Cmd
			m.editInput, cmd = m.editInput.Update(msg)
			return m, cmd

		case detailView, suggestView, statsView, calendarView:
			if msg.Type == tea.KeyEsc || key.Matches(msg, m.keys.Enter) {
				m.mode = listView
				m.selectedTodo = nil
				return m, tea.ClearScreen
			}
			// Allow Ctrl+P to also exit calendar view
			if m.mode == calendarView && key.Matches(msg, m.keys.Calendar) {
				m.mode = listView
				return m, tea.ClearScreen
			}

		case taskView:
			// If in input mode, handle input first
			if m.taskInputMode {
				switch {
				case msg.Type == tea.KeyEsc:
					// Cancel input mode
					m.taskInputMode = false
					m.taskInput.SetValue("")
					m.taskInput.Blur()
					return m, nil

				case msg.Type == tea.KeyEnter:
					// Add the task to processing queue
					if m.taskInput.Value() != "" && m.selectedTodo != nil {
						taskText := m.taskInput.Value()

						// Add placeholder task immediately for responsiveness
						placeholderTask := models.Task{
							Text:      fmt.Sprintf("⏳ Processing: %s", taskText),
							Completed: false,
						}
						m.selectedTodo.Tasks = append(m.selectedTodo.Tasks, placeholderTask)
						m.selectedTask = len(m.selectedTodo.Tasks) - 1 // Select the new task

						// Add to pending queue
						m.pendingTasks = append(m.pendingTasks, taskText)
						m.processingTask = true

						m.statusMsg = "🤖 Fixing grammar..."
						m.statusType = "info"
						m.addLog(fmt.Sprintf("⏳ Processing task: %s", taskText))

						// Clear input for next task
						m.taskInput.SetValue("")

						// Start processing
						return m, tea.Batch(
							m.processTaskGrammar(taskText, m.selectedTodo.ID),
							tickCmd(),
						)
					}
					return m, nil

				default:
					// Update the input field
					var cmd tea.Cmd
					m.taskInput, cmd = m.taskInput.Update(msg)
					return m, cmd
				}
			}

			// Normal task view navigation
			switch {
			case key.Matches(msg, m.keys.Add):
				// Ctrl+A toggles input mode
				m.taskInputMode = true
				m.taskInput.SetValue("")
				m.taskInput.Focus()
				m.statusMsg = "Type task and press Enter. Esc to cancel"
				m.statusType = "info"
				return m, textinput.Blink

			case msg.Type == tea.KeyLeft || msg.Type == tea.KeyEsc:
				// Left arrow or Esc goes back to list view
				if m.selectedTodo != nil {
					// Save any changes
					m.db.UpdateTodo(m.selectedTodo)
				}
				m.mode = listView
				m.selectedTodo = nil
				m.taskInputMode = false // Reset input mode
				m.taskInput.SetValue("")
				m.taskInput.Blur()
				m.addLog("⬅️ Back to todo list")
				return m, m.loadTodos()

			case msg.Type == tea.KeyUp:
				if m.selectedTodo != nil && len(m.selectedTodo.Tasks) > 0 {
					// Build sorted task list to find current position
					var uncompletedTasks []int
					var completedTasks []int

					for i, task := range m.selectedTodo.Tasks {
						if task.Completed {
							completedTasks = append(completedTasks, i)
						} else {
							uncompletedTasks = append(uncompletedTasks, i)
						}
					}

					sortedIndices := append(uncompletedTasks, completedTasks...)

					// Find current position in sorted list
					currentSortedPos := -1
					for pos, idx := range sortedIndices {
						if idx == m.selectedTask {
							currentSortedPos = pos
							break
						}
					}

					// Move up in sorted order
					if currentSortedPos > 0 {
						m.selectedTask = sortedIndices[currentSortedPos-1]
					}
				}
				return m, nil

			case msg.Type == tea.KeyDown:
				if m.selectedTodo != nil && len(m.selectedTodo.Tasks) > 0 {
					// Build sorted task list to find current position
					var uncompletedTasks []int
					var completedTasks []int

					for i, task := range m.selectedTodo.Tasks {
						if task.Completed {
							completedTasks = append(completedTasks, i)
						} else {
							uncompletedTasks = append(uncompletedTasks, i)
						}
					}

					sortedIndices := append(uncompletedTasks, completedTasks...)

					// Find current position in sorted list
					currentSortedPos := -1
					for pos, idx := range sortedIndices {
						if idx == m.selectedTask {
							currentSortedPos = pos
							break
						}
					}

					// Move down in sorted order
					if currentSortedPos < len(sortedIndices)-1 {
						m.selectedTask = sortedIndices[currentSortedPos+1]
					}
				}
				return m, nil

			case msg.Type == tea.KeySpace || msg.Type == tea.KeyEnter:
				// Toggle task completion
				if m.selectedTodo != nil && m.selectedTask < len(m.selectedTodo.Tasks) {
					m.selectedTodo.Tasks[m.selectedTask].Completed = !m.selectedTodo.Tasks[m.selectedTask].Completed

					// Update in database
					err := m.db.UpdateTaskStatus(m.selectedTodo.ID, m.selectedTask, m.selectedTodo.Tasks[m.selectedTask].Completed)
					if err != nil {
						m.statusMsg = fmt.Sprintf("❌ Failed to update task: %v", err)
						m.statusType = "error"
					} else {
						status := "completed"
						if !m.selectedTodo.Tasks[m.selectedTask].Completed {
							status = "reopened"
						}
						m.statusMsg = fmt.Sprintf("✅ Subtask %s", status)
						m.statusType = "success"
						m.addLog(fmt.Sprintf("✅ Subtask %s: %s", status, m.selectedTodo.Tasks[m.selectedTask].Text))
					}
					// Refresh to show updated progress
					return m, m.loadTodos()
				}
				return m, nil

			case key.Matches(msg, m.keys.Delete):
				// Ctrl+D deletes the selected subtask
				if m.selectedTodo != nil && m.selectedTask < len(m.selectedTodo.Tasks) {
					taskText := m.selectedTodo.Tasks[m.selectedTask].Text

					// Remove the task from the slice
					m.selectedTodo.Tasks = append(
						m.selectedTodo.Tasks[:m.selectedTask],
						m.selectedTodo.Tasks[m.selectedTask+1:]...,
					)

					// Update in database
					err := m.db.UpdateTodo(m.selectedTodo)
					if err != nil {
						m.statusMsg = fmt.Sprintf("❌ Failed to delete task: %v", err)
						m.statusType = "error"
					} else {
						m.statusMsg = fmt.Sprintf("🗑️ Deleted subtask: %s", taskText)
						m.statusType = "success"
						m.addLog(fmt.Sprintf("🗑️ Deleted subtask: %s", taskText))

						// Adjust selected index if needed
						if m.selectedTask >= len(m.selectedTodo.Tasks) && m.selectedTask > 0 {
							m.selectedTask--
						}

						// If no more tasks, stay in task view but show empty state
						if len(m.selectedTodo.Tasks) == 0 {
							m.statusMsg = "📋 All subtasks deleted. Press Ctrl+A to add new tasks"
							m.statusType = "info"
							// Don't exit task view - user can add more tasks
						}
					}
					// Refresh to show updated list
					return m, m.loadTodos()
				}
				return m, nil
			}

		case quitConfirmView:
			switch msg.Type {
			case tea.KeyEsc:
				// Esc cancels the quit
				m.mode = m.previousMode
				return m, nil
			case tea.KeyEnter:
				// Enter confirms the quit - start closing animation
				m.mode = closingView
				m.closingFrame = 0
				m.closingProgress = 0
				// Save any pending changes
				if m.db != nil {
					m.db.Close()
				}
				return m, closingTickCmd()
			}
		}

		// Update list
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// View
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	switch m.mode {
	case splashView:
		return m.splashViewRender()
	case apiKeySetupView:
		return m.apiKeyViewRender()
	case setupView:
		return m.setupViewRender()
	case idleView:
		return m.idleViewRender()
	case addView:
		return m.addViewRenderFixed()
	case editView:
		return m.editViewRenderFixed()
	case detailView:
		return m.detailViewRender()
	case taskView:
		return m.taskViewRender()
	case suggestView:
		return m.suggestViewRenderFixed()
	case statsView:
		return m.statsViewRender()
	case quitConfirmView:
		return m.quitConfirmViewRender()
	case closingView:
		return m.closingViewRender()
	case calendarView:
		return m.calendarViewRender()
	default:
		return m.listViewRender()
	}
}
