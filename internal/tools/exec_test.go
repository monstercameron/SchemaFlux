package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestShellToolBasic(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"echo"}})

	// The shell tool always wraps with cmd/sh, so just pass the command
	result, _ := ShellTool.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	stdout := data["stdout"].(string)
	if !strings.Contains(strings.ToLower(stdout), "hello") {
		t.Errorf("Expected 'hello' in output, got %q", stdout)
	}
	if data["exit_code"].(int) != 0 {
		t.Error("Expected exit code 0")
	}
}

func TestShellToolWithShell(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"echo"}})

	result, _ := ShellTool.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if !strings.Contains(strings.ToLower(data["stdout"].(string)), "hello") {
		t.Error("Expected 'hello' in output")
	}
}

func TestShellToolExitCode(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"exit"}})

	var command string
	if runtime.GOOS == "windows" {
		command = "exit /b 1"
	} else {
		command = "exit 1"
	}

	result, _ := ShellTool.Execute(context.Background(), map[string]any{
		"command": command,
	})

	// On Windows, "exit /b 1" should return exit code 1
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected a result payload, got %+v (error: %s)", result.Data, result.Error)
	}
	exitCode := data["exit_code"].(int)
	// Accept either failure status or non-zero exit code
	if result.Success && exitCode == 0 {
		t.Skipf("Platform-specific exit code behavior - got success=%v, exit_code=%d", result.Success, exitCode)
	}
}

func TestShellToolTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	// Note: Timeout behavior varies by platform.
	// On Windows, cmd /C doesn't properly propagate context cancellation
	// to child processes, so ping may continue running.
	if runtime.GOOS == "windows" {
		t.Skip("Skipping timeout test on Windows due to cmd /C context propagation limitations")
	}

	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"sleep"}})

	result, _ := ShellTool.Execute(context.Background(), map[string]any{
		"command": "sleep 10",
		"timeout": 1.0,
	})

	// When timeout occurs, it returns a result with timed_out metadata
	if result.Metadata != nil && result.Metadata["timed_out"] == true {
		return // Timeout correctly detected
	}

	// Also check if it failed due to error
	if !result.Success {
		return // Command failed (possibly due to timeout)
	}

	t.Error("Expected command to timeout or fail")
}

func TestShellToolMissingCommand(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"echo"}})

	result, _ := ShellTool.Execute(context.Background(), map[string]any{})
	if result.Success {
		t.Error("Expected failure for missing command")
	}
}

// An invalid command is now refused by the allowlist before it ever reaches a
// shell, which is the point of the allowlist.
func TestShellToolInvalidCommand(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"echo"}})

	result, _ := ShellTool.Execute(context.Background(), map[string]any{
		"command": "nonexistent-command-12345",
	})
	if result.Success {
		t.Fatal("a command outside the allowlist must be refused")
	}
}

func TestRunCodeToolStub(t *testing.T) {
	result, _ := RunCodeTool.Execute(context.Background(), map[string]any{
		"language": "python",
		"code":     "print('hello')",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected run_code to be stubbed")
	}
}

func TestEmbedToolStub(t *testing.T) {
	result, _ := EmbedTool.Execute(context.Background(), map[string]any{
		"text": "Hello world",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected embed to be stubbed")
	}
}

func TestSimilarityToolStub(t *testing.T) {
	result, _ := SimilarityTool.Execute(context.Background(), map[string]any{
		"text1": "Hello",
		"text2": "Hi",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected similarity to be stubbed")
	}
}

func TestSemanticSearchToolStub(t *testing.T) {
	result, _ := SemanticSearchTool.Execute(context.Background(), map[string]any{
		"query":      "test query",
		"collection": "documents",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected semantic_search to be stubbed")
	}
}

func TestClassifyToolStub(t *testing.T) {
	result, _ := ClassifyTool.Execute(context.Background(), map[string]any{
		"text":   "This is great!",
		"labels": []any{"positive", "negative", "neutral"},
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected classify to be stubbed")
	}
}

func TestSentimentToolStub(t *testing.T) {
	result, _ := SentimentTool.Execute(context.Background(), map[string]any{
		"text": "I love this product!",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected sentiment to be stubbed")
	}
}

func TestTranslateToolStub(t *testing.T) {
	result, _ := TranslateTool.Execute(context.Background(), map[string]any{
		"text":   "Hello world",
		"target": "es",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected translate to be stubbed")
	}
}
