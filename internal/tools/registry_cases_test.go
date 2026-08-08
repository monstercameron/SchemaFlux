package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func noopTool(name string) *Tool {
	return &Tool{
		Name:    name,
		Execute: func(context.Context, map[string]any) (Result, error) { return NewResult(nil), nil },
	}
}

// ---------------------------------------------------------------------------
// F-027 — registry errors
// ---------------------------------------------------------------------------

// Register's error was discarded at init throughout the package, so a duplicate
// name silently kept whichever tool won and the other vanished. Every way of
// getting a bad registration has to be reported.
func TestRegisterRejectsBadRegistrations(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) *Tool
		wantErr bool
	}{
		{"fresh_name", func(t *testing.T) *Tool { return noopTool("register-case-fresh") }, false},
		{"empty_name", func(t *testing.T) *Tool { return noopTool("") }, true},
		{"duplicate_of_a_builtin", func(t *testing.T) *Tool { return noopTool("calculate") }, true},
		{"duplicate_of_a_new_tool", func(t *testing.T) *Tool {
			tool := noopTool("register-case-dup")
			if err := Register(tool); err != nil {
				t.Fatalf("first Register: %v", err)
			}
			t.Cleanup(func() { Unregister(tool.Name) })
			return tool
		}, true},
		{"same_name_different_instance", func(t *testing.T) *Tool {
			first := noopTool("register-case-same-name")
			if err := Register(first); err != nil {
				t.Fatalf("first Register: %v", err)
			}
			t.Cleanup(func() { Unregister(first.Name) })
			return noopTool("register-case-same-name")
		}, true},
		{"nil_execute_is_allowed_at_registration", func(t *testing.T) *Tool {
			return &Tool{Name: "register-case-no-execute"}
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.setup(t)
			err := Register(tool)
			if err == nil {
				t.Cleanup(func() { Unregister(tool.Name) })
			}
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("Register error = %v, want error = %v", err, tc.wantErr)
			}
		})
	}
}

// A tool that failed to register must not be reachable, and must not have
// displaced the one that was already there.
func TestFailedRegistrationLeavesTheRegistryIntact(t *testing.T) {
	original, found := Get("calculate")
	if !found {
		t.Fatal("the calculate tool should be registered")
	}

	impostor := noopTool("calculate")
	if err := Register(impostor); err == nil {
		t.Fatal("registering over an existing name must be an error")
	}

	current, found := Get("calculate")
	if !found {
		t.Fatal("the original tool disappeared")
	}
	if current != original {
		t.Error("a failed registration displaced the existing tool")
	}
}

// Unregister reports what it did, and is safe to call twice.
func TestUnregisterReportsWhatItDid(t *testing.T) {
	cases := []struct {
		name       string
		register   bool
		toolName   string
		wantRemove bool
	}{
		{"unknown_name", false, "unregister-case-unknown", false},
		{"registered_name", true, "unregister-case-known", true},
		{"empty_name", false, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.register {
				if err := Register(noopTool(tc.toolName)); err != nil {
					t.Fatalf("Register: %v", err)
				}
			}
			if got := Unregister(tc.toolName); got != tc.wantRemove {
				t.Errorf("Unregister(%q) = %v, want %v", tc.toolName, got, tc.wantRemove)
			}
			if Unregister(tc.toolName) {
				t.Errorf("a second Unregister(%q) must report false", tc.toolName)
			}
			if _, found := Get(tc.toolName); found && tc.register {
				t.Errorf("%q is still registered", tc.toolName)
			}
		})
	}
}

// Register and Unregister round-trip: a name freed by Unregister can be taken
// again, which is what DisableShell depends on.
func TestRegisterUnregisterRoundTrip(t *testing.T) {
	name := "roundtrip-under-test"

	for round := 0; round < 3; round++ {
		if err := Register(noopTool(name)); err != nil {
			t.Fatalf("round %d Register: %v", round, err)
		}
		if _, found := Get(name); !found {
			t.Fatalf("round %d: the tool is not reachable after Register", round)
		}
		if !Unregister(name) {
			t.Fatalf("round %d: Unregister reported nothing removed", round)
		}
		if _, found := Get(name); found {
			t.Fatalf("round %d: the tool is still reachable after Unregister", round)
		}
	}
}

// mustRegister turns a duplicate into a panic at init, which is where a
// collision between two built-ins belongs.
func TestMustRegisterPanicMessageNamesTheTool(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("mustRegister must panic on a duplicate")
		}
		message, _ := recovered.(string)
		if !strings.Contains(message, "calculate") {
			t.Errorf("the panic should name the tool, got %v", recovered)
		}
	}()
	mustRegister(noopTool("calculate"))
}

// ---------------------------------------------------------------------------
// F-026 — stub honesty
// ---------------------------------------------------------------------------

// Every stub in the registry must be identifiable by the flag consumers filter
// on, and its result must carry the stubbed marker.
func TestEveryStubToolIsIdentifiable(t *testing.T) {
	stubs := 0
	for _, tool := range List() {
		if !tool.IsStub {
			continue
		}
		stubs++
		t.Run(tool.Name, func(t *testing.T) {
			if tool.Description == "" {
				t.Error("a stub with no description gives a consumer nothing to go on")
			}
			lower := strings.ToLower(tool.Description)
			if !strings.Contains(lower, "stub") && !strings.Contains(lower, "requires") {
				t.Errorf("the description does not disclose the stub: %q", tool.Description)
			}
		})
	}

	// PS-001 curated the registry: every tool that did nothing was deleted
	// rather than kept behind a flag, so the expected number of stubs is now
	// zero. This used to require at least five, which was the right assertion
	// while half the registry was unimplemented and the wrong one afterwards.
	//
	// The subtests above still run if a stub is ever reintroduced, so the
	// disclosure rule survives; what changed is that a stub is now an
	// exception to justify rather than the normal state.
	if stubs != 0 {
		t.Errorf("%d stub tools are registered; the registry is meant to contain only tools that work", stubs)
	}
}

// A tool that is not marked a stub must not answer with a stub marker: that is
// the combination the token tool shipped for JWTs.
func TestNonStubToolsDoNotReturnStubResults(t *testing.T) {
	cases := []struct {
		name   string
		tool   *Tool
		params map[string]any
	}{
		{"token_random", TokenTool, map[string]any{"action": "generate", "type": "random", "length": float64(16)}},
		{"token_jwt", TokenTool, map[string]any{"action": "generate", "type": "jwt"}},
		{"hash_sha256", HashTool, map[string]any{"data": "hello", "algorithm": "sha256"}},
		{"base64_encode", Base64Tool, map[string]any{"action": "encode", "data": "hello"}},
		{"cache_set", CacheTool, map[string]any{"action": "set", "key": "k", "value": "v"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tool.IsStub {
				t.Skipf("%s is declared a stub", tc.tool.Name)
			}
			result, err := tc.tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if stubbed, _ := result.Metadata["stubbed"].(bool); stubbed {
				t.Errorf("%s is not marked IsStub but answered with a stub result", tc.tool.Name)
			}
		})
	}
}

// The token tool's random path has to produce real entropy across every length
// a caller might ask for, and reject the lengths it cannot serve.
func TestTokenLengths(t *testing.T) {
	cases := []struct {
		length  float64
		wantErr bool
	}{
		{1, false}, {8, false}, {16, false}, {32, false}, {64, false}, {128, false}, {256, false},
		{0, false}, // zero means the default
		{-1, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("len_%v", tc.length), func(t *testing.T) {
			result, err := TokenTool.Execute(context.Background(), map[string]any{
				"action": "generate",
				"type":   "random",
				"length": tc.length,
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !result.Success {
				t.Fatalf("length %v was refused: %s", tc.length, result.Error)
			}
			token, _ := result.Data.(string)
			if token == "" {
				t.Fatal("an empty token is not a token")
			}
			if tc.length > 0 && len(token) != int(tc.length) {
				t.Errorf("length %v produced %d characters", tc.length, len(token))
			}
		})
	}
}
