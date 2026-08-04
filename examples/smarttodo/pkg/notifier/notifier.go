//go:build !js

package notifier

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

// Notifier handles system notifications
type Notifier struct {
	enabled bool
	isMacOS bool
}

// NewNotifier creates a new notifier
func NewNotifier() *Notifier {
	return &Notifier{
		enabled: true,
		isMacOS: runtime.GOOS == "darwin",
	}
}

// Notify sends a system notification
func (n *Notifier) Notify(title, message string) error {
	if !n.enabled {
		return nil
	}

	if n.isMacOS {
		// Use macOS Notification Center with osascript
		// This will show in Notification Center and respect Do Not Disturb
		script := fmt.Sprintf(`display notification "%s" with title "SchemaFlux CommandDeck" subtitle "%s" sound name "Glass"`, message, title)
		return exec.Command("osascript", "-e", script).Run()
	}

	// Fallback to terminal bell for other systems
	fmt.Print("\a")
	return nil
}

// NotifyWithAction sends a notification with an action button (macOS only)
func (n *Notifier) NotifyWithAction(title, message, action string) error {
	if !n.enabled || !n.isMacOS {
		return n.Notify(title, message)
	}

	// Use terminal-notifier if available for action buttons
	if _, err := exec.LookPath("terminal-notifier"); err == nil {
		return exec.Command("terminal-notifier",
			"-title", "SchemaFlux CommandDeck",
			"-subtitle", title,
			"-message", message,
			"-sound", "default",
			"-actions", action,
			"-appIcon", "https://cdn-icons-png.flaticon.com/512/4697/4697260.png",
		).Run()
	}

	// Fall back to regular notification
	return n.Notify(title, message)
}

// NotifyUrgent checks and notifies for urgent tasks
func (n *Notifier) NotifyUrgent(todo *models.SmartTodo) error {
	if todo.Deadline == nil {
		return nil
	}

	timeUntil := time.Until(*todo.Deadline)
	if timeUntil <= time.Hour && timeUntil > 0 {
		minutes := int(timeUntil.Minutes())
		return n.Notify(
			"⚠️ Task Due Soon!",
			fmt.Sprintf("%s is due in %d minutes", todo.Title, minutes),
		)
	}
	return nil
}

// NotifyOverdue notifies about overdue tasks
func (n *Notifier) NotifyOverdue(todos []*models.SmartTodo) error {
	overdue := 0
	for _, todo := range todos {
		if todo.Deadline != nil && !todo.Completed {
			if time.Now().After(*todo.Deadline) {
				overdue++
			}
		}
	}

	if overdue > 0 {
		return n.Notify(
			"📅 Overdue Tasks",
			fmt.Sprintf("You have %d overdue task(s)", overdue),
		)
	}
	return nil
}

// UpdateTerminalTitle updates the terminal window title
func UpdateTerminalTitle(pendingCount int) {
	if pendingCount > 0 {
		fmt.Printf("\033]0;SchemaFlux CommandDeck - %d pending tasks\007", pendingCount)
	} else {
		fmt.Printf("\033]0;SchemaFlux CommandDeck - All done! 🎉\007")
	}
}
