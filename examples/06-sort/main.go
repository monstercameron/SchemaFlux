// Example: 06-sort
//
// Operation: Sort[T] - Semantically sort items by criteria
//
// Input: []Task (5 tasks with varying urgency/impact/effort)
//   - #1: "Update documentation" (next week, low impact, 2hrs)
//   - #2: "Fix critical security vulnerability" (today, critical, 4hrs)
//   - #3: "Add dark mode feature" (next month, medium, 3 days)
//   - #4: "Database backup failing" (ASAP, high, 1hr)
//   - #5: "Refactor payment module" (Q2 2025, low, 1 week)
//
// Expected Output: Sorted by priority (urgency + impact + effort)
//  1. #2 - Security vulnerability (critical, today)
//  2. #4 - Database backup (high impact, ASAP, quick fix)
//  3. #1 - Documentation (next week, quick)
//  4. #3 - Dark mode (medium, next month)
//  5. #5 - Refactor (low, Q2 2025)
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500-1000ms
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Task represents a work task
type Task struct {
	ID          int    `json:"id"`          // Expected: Task identifier
	Title       string `json:"title"`       // Expected: Task name
	Description string `json:"description"` // Expected: Task details
	Deadline    string `json:"deadline"`    // Expected: When due
	Impact      string `json:"impact"`      // Expected: Business impact level
	Effort      string `json:"effort"`      // Expected: Time required
}

// loadEnv loads environment variables from a .env file
func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return scanner.Err()
}

func main() {
	// Load .env file from project root
	if err := loadEnv("../../.env"); err != nil {
		fmt.Printf("Warning: Could not load .env file: %v\n", err)
	}

	// Initialize SchemaFlux
	if err := schemaflux.InitWithEnv(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		os.Exit(1)
	}

	// Unsorted tasks
	tasks := []Task{
		{
			ID:          1,
			Title:       "Update documentation",
			Description: "Update API documentation for v2.0 release",
			Deadline:    "Next week",
			Impact:      "Low - only affects developers",
			Effort:      "2 hours",
		},
		{
			ID:          2,
			Title:       "Fix critical security vulnerability",
			Description: "Patch SQL injection vulnerability in login system",
			Deadline:    "Today",
			Impact:      "Critical - affects all users, security risk",
			Effort:      "4 hours",
		},
		{
			ID:          3,
			Title:       "Add dark mode feature",
			Description: "Implement dark mode toggle in settings",
			Deadline:    "Next month",
			Impact:      "Medium - user requested feature",
			Effort:      "3 days",
		},
		{
			ID:          4,
			Title:       "Database backup failing",
			Description: "Automated backups haven't run in 3 days",
			Deadline:    "ASAP",
			Impact:      "High - data loss risk",
			Effort:      "1 hour",
		},
		{
			ID:          5,
			Title:       "Refactor payment module",
			Description: "Clean up legacy payment processing code",
			Deadline:    "Q2 2025",
			Impact:      "Low - technical debt",
			Effort:      "1 week",
		},
	}

	fmt.Println("📋 Sort Example - Task Prioritization")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Println("\n📥 Unsorted Tasks:")
	for _, t := range tasks {
		fmt.Printf("  %d. %s (Deadline: %s)\n", t.ID, t.Title, t.Deadline)
	}

	// Sort tasks by priority (urgency + impact + effort)
	sortOpts := schemaflux.NewSortOptions().WithCriteria("Priority by: 1) Urgency (deadline), 2) Business impact, 3) Effort (quick wins first)")
	sortOpts.OpOptions.Intelligence = schemaflux.Fast
	sortOpts.OpOptions.Steering = "Consider deadline urgency, business impact, and effort. Quick high-impact tasks should be prioritized."

	sortedTasks, err := schemaflux.Sort(tasks, sortOpts)

	if err != nil {
		schemaflux.GetLogger().Error("Sorting failed", "error", err)
		os.Exit(1)
	}

	// Display sorted tasks
	fmt.Println("\n✅ Prioritized Tasks (Highest Priority First):")
	fmt.Println("---")
	for i, t := range sortedTasks {
		fmt.Printf("\n%d. 🎯 %s\n", i+1, t.Title)
		fmt.Printf("   Deadline: %s\n", t.Deadline)
		fmt.Printf("   Impact:   %s\n", t.Impact)
		fmt.Printf("   Effort:   %s\n", t.Effort)
		fmt.Printf("   Why:      ")
		if i == 0 {
			fmt.Println("Critical security issue with immediate deadline")
		} else if i == 1 {
			fmt.Println("High impact with minimal effort - quick win")
		} else if i == 2 {
			fmt.Println("Quick task before moving to longer projects")
		} else {
			fmt.Println("Can be scheduled for later")
		}
	}

	fmt.Println("\n✨ Success! Tasks intelligently prioritized")
}
