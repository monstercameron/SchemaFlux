package codex_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests run the real provider, the real command line it assembles and the
// real event parser, against a program that is not Codex: this test binary, started
// by the provider as `<binary> exec ... -o <reply file> -`. A test binary is never
// started with "exec" as its first argument, so the first argument is how the
// binary knows it is being used as the Codex program. What the fake does is told
// by the first line of the prompt, which is the system prompt of the request: a
// JSON object, a "script".

// scriptVariable holds a script, for a request whose first line is not one.
const scriptVariable = "SCHEMAFLUX_V2_CODEX_FAKE_SCRIPT"

// script tells the fake what to do.
type script struct {
	// Mode is "ok" (the default), "failed" (a failed turn and exit status 1),
	// "silent" (exit status 0 and no event at all) or "hang" (never end).
	Mode string `json:"mode,omitempty"`

	// Reply is the final message written to the reply file.
	Reply string `json:"reply,omitempty"`

	// Log is a file the fake appends one line to for each run: its arguments and
	// its standard input.
	Log string `json:"log,omitempty"`

	// Words are what a failed turn says, which is vendor prose.
	Words string `json:"words,omitempty"`
}

// invocation is a line of the log.
type invocation struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Env   []string `json:"env"`
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		os.Exit(fakeCodex(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeCodex is the program the provider starts.
func fakeCodex(args []string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	var s script
	first, _, _ := strings.Cut(string(stdin), "\n")
	if json.Unmarshal([]byte(first), &s) != nil {
		// A request made by the library's own operations has a system prompt of
		// its own, so a test that makes one gives its script in the environment.
		s = script{}
		_ = json.Unmarshal([]byte(os.Getenv(scriptVariable)), &s)
	}

	if s.Log != "" {
		line, _ := json.Marshal(invocation{Args: args, Stdin: string(stdin), Env: keyVariablesIn(os.Environ())})
		if f, err := os.OpenFile(s.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = fmt.Fprintln(f, string(line))
			_ = f.Close()
		}
	}
	switch s.Mode {
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "silent":
		return 0
	case "failed":
		fmt.Println(`{"type":"turn.failed","error":{"message":` + quote(s.Words) + `}}`)
		fmt.Fprintln(os.Stderr, s.Words)
		return 1
	}
	if i := slices.Index(args, "-o"); i >= 0 && i+1 < len(args) {
		_ = os.WriteFile(args[i+1], []byte(s.Reply+"\n"), 0o644)
	}
	fmt.Println(`{"type":"thread.started"}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":40,"output_tokens":31,"reasoning_output_tokens":7}}`)
	return 0
}

func quote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// keyVariablesIn lists the names of the variables of env that hold an API key.
func keyVariablesIn(env []string) []string {
	var names []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "OPENAI_API_KEY", "OPENAI_KEY", "CODEX_API_KEY":
			names = append(names, name)
		}
	}
	return names
}

// fakeExecutable is the path of the binary the provider runs.
func fakeExecutable(t testing.TB) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// systemOf is the system prompt that carries a script to the fake.
func systemOf(s script) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// invocations reads what the fake logged.
func invocations(t testing.TB, path string) []invocation {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	defer f.Close()
	var out []invocation
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var inv invocation
		if err := json.Unmarshal(scanner.Bytes(), &inv); err != nil {
			t.Fatalf("a line of the log: %v", err)
		}
		out = append(out, inv)
	}
	return out
}
