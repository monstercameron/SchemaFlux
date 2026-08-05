package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ShellPolicy governs what the shell tool may run. There is no permissive
// default: a policy has to name the commands it allows.
type ShellPolicy struct {
	// AllowedCommands lists the executables the tool may invoke, by base name.
	// A command not on this list is refused before anything is executed.
	AllowedCommands []string

	// WorkingDir, when set, is the only directory the tool will run in. A
	// caller-supplied dir outside it is refused.
	WorkingDir string

	// MaxTimeout caps the timeout a caller may request. Zero means one minute.
	MaxTimeout time.Duration
}

var (
	shellMu     sync.RWMutex
	shellPolicy *ShellPolicy
)

// EnableShell registers the shell tool under the given policy, and returns an
// error if the policy allows nothing.
//
// The tool is not registered by default. It hands a model-authored string to
// `sh -c` or `cmd /C`, and it used to self-register into the global registry
// that GetOpenAITools exports — so every deployment shipped arbitrary command
// execution to whatever model it was talking to, with no allowlist, sandbox, or
// approval step.
func EnableShell(policy ShellPolicy) error {
	if len(policy.AllowedCommands) == 0 {
		return errors.New("tools: EnableShell needs at least one allowed command")
	}

	allowed := make([]string, 0, len(policy.AllowedCommands))
	for _, command := range policy.AllowedCommands {
		command = strings.TrimSpace(command)
		if command == "" {
			return errors.New("tools: EnableShell was given an empty command name")
		}
		allowed = append(allowed, command)
	}

	if policy.MaxTimeout <= 0 {
		policy.MaxTimeout = time.Minute
	}
	policy.AllowedCommands = allowed

	shellMu.Lock()
	shellPolicy = &policy
	shellMu.Unlock()

	return Register(ShellTool)
}

// DisableShell removes the shell tool and clears the policy.
func DisableShell() {
	shellMu.Lock()
	shellPolicy = nil
	shellMu.Unlock()

	DefaultRegistry.Unregister(ShellTool.Name)
}

// activeShellPolicy returns the policy in force, if any.
func activeShellPolicy() *ShellPolicy {
	shellMu.RLock()
	defer shellMu.RUnlock()
	return shellPolicy
}

// ErrShellDisabled is returned when the shell tool runs without a policy.
var ErrShellDisabled = errors.New("tools: the shell tool is disabled; call tools.EnableShell(policy) with a command allowlist")

// shellMetacharacters are the characters that let one command carry another
// past the allowlist. `ls; rm -rf /` starts with an allowed base name.
const shellMetacharacters = ";&|<>`$()\n\r"

// checkShellCommand refuses anything the policy does not allow. It rejects the
// shell metacharacters that would otherwise let an allowed command carry a
// disallowed one: `ls; rm -rf /` starts with an allowed base name.
func checkShellCommand(policy *ShellPolicy, command, dir string) error {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return errors.New("command is required")
	}


	if index := strings.IndexAny(trimmed, shellMetacharacters); index >= 0 {
		return fmt.Errorf("refused: the command contains the shell metacharacter %q, which could chain a command the policy does not allow", trimmed[index:index+1])
	}

	fields := strings.Fields(trimmed)
	base := filepath.Base(fields[0])
	base = strings.TrimSuffix(base, filepath.Ext(base))

	permitted := false
	for _, allowed := range policy.AllowedCommands {
		if strings.EqualFold(base, allowed) || strings.EqualFold(fields[0], allowed) {
			permitted = true
			break
		}
	}
	if !permitted {
		return fmt.Errorf("refused: %q is not in the allowlist %v", fields[0], policy.AllowedCommands)
	}

	if policy.WorkingDir != "" && dir != "" {
		absolutePolicy, err := filepath.Abs(policy.WorkingDir)
		if err != nil {
			return fmt.Errorf("refused: the policy working directory is unusable: %w", err)
		}
		absoluteDir, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("refused: the requested working directory is unusable: %w", err)
		}
		if absoluteDir != absolutePolicy && !strings.HasPrefix(absoluteDir, absolutePolicy+string(filepath.Separator)) {
			return fmt.Errorf("refused: %q is outside the policy working directory", dir)
		}
	}

	return nil
}

// ShellTool executes shell commands. It is registered only by EnableShell.
var ShellTool = &Tool{
	Name:         "shell",
	Description:  "Execute an allowlisted shell command with timeout and output capture.",
	Category:     CategoryComputation,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"command": StringParam("Shell command to execute; must be on the configured allowlist"),
		"args":    {Type: "array", Description: "Command arguments"},
		"dir":     StringParam("Working directory"),
		"timeout": NumberParam("Timeout in seconds (default: 30)"),
	}, []string{"command"}),
	Execute: executeShell,
}

func executeShell(ctx context.Context, params map[string]any) (Result, error) {
	policy := activeShellPolicy()
	if policy == nil {
		return ErrorResultFromError(ErrShellDisabled), nil
	}

	command, _ := params["command"].(string)
	dir, _ := params["dir"].(string)

	// Check before anything runs, not after.
	if err := checkShellCommand(policy, command, dir); err != nil {
		return ErrorResultFromError(err), nil
	}

	timeout := 30.0
	if t, ok := params["timeout"].(float64); ok && t > 0 {
		timeout = t
	}
	if maximum := policy.MaxTimeout.Seconds(); timeout > maximum {
		timeout = maximum
	}

	if dir == "" {
		dir = policy.WorkingDir
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Prepare command based on OS
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	if dir != "" {
		cmd.Dir = dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	if ctx.Err() == context.DeadlineExceeded {
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return NewResultWithMeta(map[string]any{
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
			"exit_code": exitCode,
			"error":     "command timed out",
			"duration":  duration.Seconds(),
		}, map[string]any{"timed_out": true}), nil
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ErrorResultFromError(fmt.Errorf("command failed: %w", err)), nil
		}
	}

	return NewResultWithMeta(map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
		"duration":  duration.Seconds(),
	}, nil), nil
}

// RunCodeTool executes code snippets (stub - requires sandboxed environment)
var RunCodeTool = &Tool{
	Name:        "run_code",
	Description: "Execute code snippets in sandboxed environment (stub - security considerations)",
	Category:    CategoryComputation,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"language": EnumParam("Programming language", []string{"python", "javascript", "go", "ruby", "php"}),
		"code":     StringParam("Code to execute"),
		"timeout":  NumberParam("Timeout in seconds"),
	}, []string{"language", "code"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		language, _ := params["language"].(string)
		code, _ := params["code"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":       true,
			"language":   language,
			"code_lines": len(strings.Split(code, "\n")),
			"message":    "Code execution requires sandboxed environment for security",
		}, map[string]any{"stubbed": true}), nil
	},
}

// EmbedTool generates text embeddings (stub - requires embedding model)
var EmbedTool = &Tool{
	Name:         "embed",
	Description:  "Generate text embeddings (stub - requires OpenAI or similar API)",
	Category:     CategoryAI,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"text":  StringParam("Text to embed"),
		"model": StringParam("Embedding model name"),
	}, []string{"text"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		text, _ := params["text"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":        true,
			"text_length": len(text),
			"message":     "Embedding generation requires AI API integration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// SimilarityTool calculates text similarity (stub)
var SimilarityTool = &Tool{
	Name:         "similarity",
	Description:  "Calculate semantic similarity between texts (stub - requires embeddings)",
	Category:     CategoryAI,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"text1": StringParam("First text"),
		"text2": StringParam("Second text"),
	}, []string{"text1", "text2"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"message": "Semantic similarity requires embedding model",
		}, map[string]any{"stubbed": true}), nil
	},
}

// SemanticSearchTool performs semantic search (stub)
var SemanticSearchTool = &Tool{
	Name:         "semantic_search",
	Description:  "Search documents by semantic meaning (stub - requires vector database)",
	Category:     CategoryAI,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"query":     StringParam("Search query"),
		"documents": {Type: "array", Description: "Documents to search"},
		"top_k":     NumberParam("Number of results to return"),
	}, []string{"query", "documents"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		query, _ := params["query"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"query":   query,
			"message": "Semantic search requires embedding model and vector database",
		}, map[string]any{"stubbed": true}), nil
	},
}

// ClassifyTool classifies text into categories (stub)
var ClassifyTool = &Tool{
	Name:         "classify",
	Description:  "Classify text into predefined categories (stub - requires LLM)",
	Category:     CategoryAI,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"text":       StringParam("Text to classify"),
		"categories": {Type: "array", Description: "Possible categories"},
	}, []string{"text", "categories"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		text, _ := params["text"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":        true,
			"text_length": len(text),
			"message":     "Classification requires LLM integration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// SentimentTool analyzes text sentiment (stub)
var SentimentTool = &Tool{
	Name:         "sentiment",
	Description:  "Analyze text sentiment (stub - requires sentiment model or LLM)",
	Category:     CategoryAI,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"text": StringParam("Text to analyze"),
	}, []string{"text"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		text, _ := params["text"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":        true,
			"text_length": len(text),
			"message":     "Sentiment analysis requires ML model or LLM",
		}, map[string]any{"stubbed": true}), nil
	},
}

// TranslateTool translates text (stub)
var TranslateTool = &Tool{
	Name:         "translate",
	Description:  "Translate text between languages (stub - requires translation API)",
	Category:     CategoryAI,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"text": StringParam("Text to translate"),
		"from": StringParam("Source language code"),
		"to":   StringParam("Target language code"),
	}, []string{"text", "to"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		text, _ := params["text"].(string)
		to, _ := params["to"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":        true,
			"text_length": len(text),
			"target":      to,
			"message":     "Translation requires translation API (Google, DeepL, etc.)",
		}, map[string]any{"stubbed": true}), nil
	},
}

func init() {
	// ShellTool is deliberately absent: register it with EnableShell(policy).
	mustRegister(RunCodeTool)
	mustRegister(EmbedTool)
	mustRegister(SimilarityTool)
	mustRegister(SemanticSearchTool)
	mustRegister(ClassifyTool)
	mustRegister(SentimentTool)
	mustRegister(TranslateTool)
}
