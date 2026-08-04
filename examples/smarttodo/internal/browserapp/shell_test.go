package browserapp

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/examples/smarttodo/internal/models"
)

func TestShellSetContext(t *testing.T) {
	shell := NewShell()
	msg := shell.SetContext("office morning")
	if msg != "Planning context updated." {
		t.Fatalf("unexpected message: %s", msg)
	}
	if shell.state.Context != "office morning" {
		t.Fatalf("context not stored: %q", shell.state.Context)
	}
}

func TestShellExportImportState(t *testing.T) {
	shell := NewShell()
	shell.state.Context = "deep work"
	shell.state.NextID = 9
	shell.state.Todos = []*models.SmartTodo{{ID: "7", Title: "Ship release", Priority: "high"}}

	raw, err := shell.ExportState()
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	restored := NewShell()
	msg, err := restored.ImportState(raw)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if !strings.Contains(msg, "Restored 1 tasks") {
		t.Fatalf("unexpected restore message: %s", msg)
	}
	if restored.state.Context != "deep work" {
		t.Fatalf("context not restored: %q", restored.state.Context)
	}
	if restored.state.NextID != 9 {
		t.Fatalf("next id not restored: %d", restored.state.NextID)
	}
	if len(restored.state.Todos) != 1 || restored.state.Todos[0].Title != "Ship release" {
		t.Fatalf("todos not restored: %#v", restored.state.Todos)
	}
}

func TestShellLocalCommands(t *testing.T) {
	shell := NewShell()
	shell.state.Todos = []*models.SmartTodo{
		{ID: "1", Title: "Urgent thing", Priority: "high"},
		{ID: "2", Title: "Done thing", Priority: "low", Completed: true},
	}

	out, err := shell.Submit("/board hot")
	if err != nil {
		t.Fatalf("/board hot failed: %v", err)
	}
	if !strings.Contains(out, "HOT 1") {
		t.Fatalf("unexpected board output: %s", out)
	}

	out, err = shell.Submit("/complete 1")
	if err != nil {
		t.Fatalf("/complete failed: %v", err)
	}
	if !strings.Contains(out, "Completed") {
		t.Fatalf("unexpected complete output: %s", out)
	}
	if !shell.state.Todos[0].Completed {
		t.Fatal("todo should be completed")
	}

	out, err = shell.Submit("/drop 2")
	if err != nil {
		t.Fatalf("/drop failed: %v", err)
	}
	if !strings.Contains(out, "Deleted") {
		t.Fatalf("unexpected drop output: %s", out)
	}
	if len(shell.state.Todos) != 1 {
		t.Fatalf("expected one todo after delete, got %d", len(shell.state.Todos))
	}
}
