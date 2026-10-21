package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Task represents a work task.
type Task struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Deadline    string `json:"deadline"`
	Impact      string `json:"impact"`
	Effort      string `json:"effort"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Sorting failed: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	tasks := []Task{
		{ID: 1, Title: "Update documentation", Description: "Update API documentation for v2.0 release", Deadline: "Next week", Impact: "Low - only affects developers", Effort: "2 hours"},
		{ID: 2, Title: "Fix critical security vulnerability", Description: "Patch SQL injection vulnerability in login system", Deadline: "Today", Impact: "Critical - affects all users, security risk", Effort: "4 hours"},
		{ID: 3, Title: "Add dark mode feature", Description: "Implement dark mode toggle in settings", Deadline: "Next month", Impact: "Medium - user requested feature", Effort: "3 days"},
		{ID: 4, Title: "Database backup failing", Description: "Automated backups haven't run in 3 days", Deadline: "ASAP", Impact: "High - data loss risk", Effort: "1 hour"},
		{ID: 5, Title: "Refactor payment module", Description: "Clean up legacy payment processing code", Deadline: "Q2 2025", Impact: "Low - technical debt", Effort: "1 week"},
	}

	fmt.Fprintln(out, "Sort Example - Task Prioritization")
	fmt.Fprintln(out, "==================================================")
	fmt.Fprintln(out, "\nUnsorted Tasks:")
	for _, task := range tasks {
		fmt.Fprintf(out, "  %d. %s (Deadline: %s)\n", task.ID, task.Title, task.Deadline)
	}

	// The caller's criteria and steering guide semantic ordering; Sort checks
	// that the returned tasks are a permutation of the input.
	sortedTasks, err := schemaflux.Sort(ctx, tasks,
		"Priority by urgency (deadline), business impact, then effort (quick wins first)",
		schemaflux.SortOptions{Options: schemaflux.Options{Steering: "Consider deadline urgency, business impact, and effort. Quick high-impact tasks should be prioritized."}},
	)
	if err != nil {
		return fmt.Errorf("sort tasks: %w", err)
	}

	fmt.Fprintln(out, "\nPrioritized Tasks (Highest Priority First):")
	fmt.Fprintln(out, "---")
	for i, task := range sortedTasks {
		fmt.Fprintf(out, "\n%d. %s\n", i+1, task.Title)
		fmt.Fprintf(out, "   Deadline: %s\n", task.Deadline)
		fmt.Fprintf(out, "   Impact:   %s\n", task.Impact)
		fmt.Fprintf(out, "   Effort:   %s\n", task.Effort)
		why := "Can be scheduled for later"
		switch i {
		case 0:
			why = "Critical security issue with immediate deadline"
		case 1:
			why = "High impact with minimal effort - quick win"
		case 2:
			why = "Quick task before moving to longer projects"
		}
		fmt.Fprintf(out, "   Why:      %s\n", why)
	}
	fmt.Fprintln(out, "\nSuccess! Tasks intelligently prioritized")
	return nil
}
