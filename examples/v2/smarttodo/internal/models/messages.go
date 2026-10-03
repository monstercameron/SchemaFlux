package models

import "time"

// Messages for the update loop
type TodosLoadedMsg struct {
	Todos []*SmartTodo
}

type StatsLoadedMsg struct {
	Stats map[string]int
}

type TodoAddedMsg struct {
	Todo *SmartTodo
}

type TodoProcessingMsg struct {
	Input string
}

type TodoSuggestedMsg struct {
	Todo *SmartTodo
}

type TickMsg time.Time

type SetupCompleteMsg struct {
	UserName  string
	ListTitle string
}

type DeadlineUpdateMsg struct {
	Success bool
	Message string
}

type TaskProcessedMsg struct {
	OriginalText string
	FixedText    string
	TodoID       string
}

type StartClosingMsg struct{}

type ClosingTickMsg struct{}

type FinalQuitMsg struct{}

type SplashDismissMsg struct{}

type ErrMsg struct {
	Err error
}

type IdleCheckMsg struct{}

type WakeupMsg struct{}

// AI-generated quote for idle mode
type AiQuoteMsg struct {
	Quote string
}

// Edit processing result
type EditProcessedMsg struct {
	Todo *SmartTodo
	Err  error
}

// Smart prioritization result
type SmartPrioritizeMsg struct {
	Todos []*SmartTodo
	Err   error
}
