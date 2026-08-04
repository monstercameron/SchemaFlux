//go:build js && wasm

package notifier

import "github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"

type Notifier struct {
	enabled bool
	isMacOS bool
}

func NewNotifier() *Notifier {
	return &Notifier{enabled: false, isMacOS: false}
}

func (n *Notifier) Notify(title, message string) error {
	return nil
}

func (n *Notifier) NotifyWithAction(title, message, action string) error {
	return nil
}

func (n *Notifier) NotifyUrgent(todo *models.SmartTodo) error {
	return nil
}

func (n *Notifier) NotifyOverdue(todos []*models.SmartTodo) error {
	return nil
}

func UpdateTerminalTitle(pendingCount int) {}
