package localization

// UI string constants - all user-facing text in one place for localization
// These are the English defaults that will be translated on-demand based on user locale

const (
	// Application name
	AppName = "SchemaFlux CommandDeck"

	// Headers
	HeaderTakingBreak   = "Taking a Break"
	HeaderIdleMessage   = "Idle for %d minutes • Press any key to continue"
	HeaderStatistics    = "Statistics"
	HeaderAISuggestion  = "AI Suggestion"
	HeaderNewTask       = "New Task"
	HeaderEditTask      = "Edit Task"
	HeaderQuitConfirm   = "Quit SchemaFlux CommandDeck?"
	HeaderSubtasks      = "Subtasks"
	HeaderTaskDetails   = "Task Details"
	HeaderSetup         = "SchemaFlux CommandDeck Setup"
	HeaderDailySchedule = "Daily Schedule"

	// Welcome/Setup
	WelcomeTitle      = "AI-Powered Task Management"
	WelcomeFeature1   = "Intelligent task processing"
	WelcomeFeature2   = "Smart deadline management"
	WelcomeFeature3   = "Context-aware suggestions"
	WelcomeFeature4   = "Automatic task organization"
	WelcomePressEnter = "Press Enter to get started"
	WelcomeBack       = "Welcome back, %s"

	// Setup prompts
	SetupNamePrompt    = "What's your name?"
	SetupListPrompt    = "Hi %s! What would you like to call your todo list?"
	SetupAPIKeyPrompt  = "Please enter your OpenAI API key (starts with sk-):"
	SetupAPIKeyHint    = "Don't have one? Get it at: https://platform.openai.com/api-keys"
	SetupValidating    = "Validating API key..."
	SetupAPIKeyValid   = "API key validated!"
	SetupAPIKeyInvalid = "Invalid key. Please check and try again."

	// Task status
	StatusToday      = "Today: %d/%d done"
	StatusUrgent     = "🔥 %d urgent"
	StatusDue        = "⏰ %d due"
	StatusStreak     = "🔥 %d day streak"
	StatusLocation   = "📍 %s"
	StatusTasks      = "⚡ %d/%d tasks today"
	StatusNoTasks    = "No tasks yet"
	StatusProcessing = "Processing %d task(s)..."

	// Actions
	ActionAdd        = "Add todo"
	ActionComplete   = "Complete"
	ActionDelete     = "Delete"
	ActionEdit       = "Edit"
	ActionSubtasks   = "Subtasks"
	ActionNavigate   = "Navigate"
	ActionDetails    = "Details"
	ActionQuit       = "Quit"
	ActionSuggest    = "AI suggest"
	ActionStats      = "Statistics"
	ActionCalendar   = "Calendar"
	ActionPrioritize = "Smart prioritize"
	ActionFilter     = "Filter"

	// Modal prompts
	ModalWhatToDo      = "What needs to be done?"
	ModalAddContext    = "Add context or updates. AI will merge with existing todo."
	ModalAIWillExtract = "AI will extract:"
	ModalPriority      = "Priority, deadline, category"
	ModalSubtasksHint  = "Subtasks if mentioned"
	ModalLocationHint  = "Location context"
	ModalEnterSave     = "Enter: Save • Esc: Cancel"
	ModalEnterAdd      = "Enter: Add • Esc: Cancel"
	ModalTypeTask      = "Type task and press Enter. Esc to cancel"
	ModalNoSubtasks    = "No subtasks yet"
	ModalPressAddTask  = "Press Ctrl+A to add your first task!"

	// Progress/Stats
	ProgressCompleted      = "Completed today: %d tasks"
	ProgressPending        = "Pending: %d tasks"
	ProgressUrgentDue      = "Urgent (due soon): %d tasks"
	ProgressOverdue        = "Overdue: %d tasks"
	ProgressNoTasks        = "No tasks for today. Time to add some!"
	ProgressNextTask       = "Next Suggested Task:"
	ProgressTotalTasks     = "Total Tasks: %d"
	ProgressByPriority     = "Priority Breakdown"
	ProgressHigh           = "High: %d"
	ProgressMedium         = "Medium: %d"
	ProgressLow            = "Low: %d"
	ProgressCompletionRate = "Completion Rate"

	// Time related
	TimeToday          = "TODAY!"
	TimeTomorrow       = "Tomorrow"
	TimeOverdue        = "OVERDUE %d days!"
	TimeDaysLeft       = "%d days"
	TimeMinutes        = "%d minutes"
	TimeLessThanMinute = "less than a minute"

	// Activity log
	LogAdded            = "Added: %s"
	LogCompleted        = "Completed: %s"
	LogDeleted          = "Deleted: %s"
	LogEdited           = "Edited: %s"
	LogReopened         = "Reopened: %s"
	LogQueued           = "Queued task: %.50s..."
	LogProcessing       = "Processing %d more tasks..."
	LogStartingAI       = "Starting AI processing..."
	LogDeadlinesChecked = "Deadlines checked for updates"
	LogAISuggests       = "AI suggests: %s"
	LogEnteringIdle     = "Entering idle mode (power saving)"
	LogWakingUp         = "Waking up from idle mode"
	LogCost             = "Cost: $%.4f"
	LogSmartPrioritized = "Smart prioritized %d todos"

	// Errors
	ErrorNoTasks      = "no pending tasks"
	ErrorAPIKey       = "API key error. Press Ctrl+K to update key."
	ErrorOfflineMode  = "Running in offline mode - AI features disabled"
	ErrorDeleteFailed = "Failed to delete: %v"
	ErrorUpdateFailed = "Failed to update: %v"
	ErrorSaveFailed   = "Failed to save: %v"
	ErrorInvalidID    = "Invalid todo ID: %s"
	ErrorCannotEdit   = "Cannot edit a task that's being processed"
	ErrorCannotDelete = "Cannot delete a task that's being processed"

	// Closing
	ClosingSaving     = "Saving your progress..."
	ClosingDatabase   = "Closing database connections..."
	ClosingTidying    = "Tidying up..."
	ClosingAlmostDone = "Almost done..."
	ClosingGoodbye    = "Goodbye! 👋"
	ClosingSummary    = "Today's Summary:"
	ClosingCompleted  = "Completed: %d tasks"
	ClosingRemaining  = "Remaining: %d tasks"
	ClosingGreatWork  = "Great work, %s! See you next time!"

	// Categories
	CategoryWork     = "work"
	CategoryPersonal = "personal"
	CategoryShopping = "shopping"
	CategoryHealth   = "health"
	CategoryFinance  = "finance"
	CategoryHome     = "home"

	// Priorities
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"

	// Effort levels
	EffortQuick     = "quick"
	EffortModerate  = "moderate"
	EffortExtensive = "extensive"

	// Default location
	LocationHome    = "Home"
	LocationOffice  = "Office"
	LocationOutside = "Outside"

	// Keyboard shortcuts display
	ShortcutsTitle     = "⌨️  Shortcuts"
	ShortcutAdd        = "Ctrl+A"
	ShortcutComplete   = "Space"
	ShortcutDelete     = "Ctrl+D"
	ShortcutEdit       = "Ctrl+E"
	ShortcutSubtasks   = "→"
	ShortcutNavigate   = "↑/↓"
	ShortcutDetails    = "v"
	ShortcutQuit       = "Esc"
	ShortcutSuggest    = "Ctrl+S"
	ShortcutStats      = "Ctrl+I"
	ShortcutCalendar   = "Ctrl+P"
	ShortcutPrioritize = "Ctrl+R"

	// Activity log title
	ActivityLogTitle = "📜 Activity Log"
	ActivityNoRecent = "No recent activity..."

	// Idle quotes themes
	QuoteThemeTime         = "time management"
	QuoteThemeDiscipline   = "discipline"
	QuoteThemeConsistency  = "consistency"
	QuoteThemeProductivity = "productivity"
)

// Date format constants
const (
	DateFormatFull     = "Monday, January 2, 2006"
	DateFormatShort    = "Jan 2, 2006"
	DateFormatTime     = "3:04 PM"
	DateFormatDateTime = "Jan 2, 2006 at 3:04 PM"
	TimeFormat24       = "15:04:05"
)
