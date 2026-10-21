package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunScriptFindsAncestorEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a Bash script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}

	output, invokedGo, err := runLauncherFixture(t, true)
	if err != nil {
		t.Fatalf("run.sh rejected an ancestor .env: %v\n%s", err, output)
	}
	if !invokedGo {
		t.Fatalf("run.sh found the ancestor .env but did not build the app:\n%s", output)
	}
	if !strings.Contains(output, "Build successful") {
		t.Fatalf("run.sh did not finish its build step:\n%s", output)
	}
}

func TestRunScriptStopsWhenNoAncestorEnvExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a Bash script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}

	output, invokedGo, err := runLauncherFixture(t, false)
	if err == nil {
		t.Fatalf("run.sh succeeded without any .env file:\n%s", output)
	}
	if invokedGo {
		t.Fatalf("run.sh tried to build before finding a .env file:\n%s", output)
	}
	if !strings.Contains(output, "ancestor") {
		t.Fatalf("run.sh did not explain where it looked for .env:\n%s", output)
	}
}

func runLauncherFixture(t *testing.T, withAncestorEnv bool) (output string, invokedGo bool, runErr error) {
	t.Helper()

	temp := t.TempDir()
	repo := filepath.Join(temp, "repo")
	module := filepath.Join(repo, "v2", "examples", "smarttodo")
	bin := filepath.Join(temp, "bin")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if withAncestorEnv {
		if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("OPENAI_API_KEY=test-key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sourceScript, err := os.ReadFile(filepath.Join("..", "..", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "run.sh"), sourceScript, 0o755); err != nil {
		t.Fatal(err)
	}

	goStub := "#!/bin/sh\nprintf invoked > go-invoked\ncat > smarttodo <<'APP'\n#!/bin/sh\nexit 0\nAPP\nchmod +x smarttodo\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(goStub), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "run.sh")
	cmd.Dir = module
	pathValue := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+pathValue)
	combined, runErr := cmd.CombinedOutput()
	_, statErr := os.Stat(filepath.Join(module, "go-invoked"))
	return string(combined), statErr == nil, runErr
}
