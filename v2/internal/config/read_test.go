package config

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// keptVariables is the table of core_5 2.1, written out by hand and not derived
// from the reader: the 48 names the library reads and no others. The 40 of the
// first table are the seven providers' keys and base URLs, the model settings,
// the timeout and the codex executable; the eight added are the variables of the
// decision providers "jev" (TypeSafe) and "jev-litellm", which have their own,
// and "jev-openrouter", which reads OpenRouter's.
var keptVariables = []string{
	"SCHEMAFLUX_PROVIDER",
	"SCHEMAFLUX_API_KEY",
	"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY",
	"SCHEMAFLUX_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY",
	"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY",
	"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY",
	"SCHEMAFLUX_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY",
	"SCHEMAFLUX_QWEN_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY",
	"SCHEMAFLUX_ZAI_API_KEY", "ZAI_API_KEY", "GLM_API_KEY",
	"SCHEMAFLUX_OPENAI_BASE_URL", "OPENAI_BASE_URL",
	"SCHEMAFLUX_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL",
	"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL",
	"SCHEMAFLUX_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL",
	"SCHEMAFLUX_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL",
	"SCHEMAFLUX_QWEN_BASE_URL", "QWEN_BASE_URL", "DASHSCOPE_BASE_URL",
	"SCHEMAFLUX_ZAI_BASE_URL", "ZAI_BASE_URL", "GLM_BASE_URL",
	"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY",
	"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY",
	"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL",
	"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL",
	"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK",
	"SCHEMAFLUX_TIMEOUT",
	"SCHEMAFLUX_CODEX_BIN",
}

// removedVariables is the table of core_5 2.2, the bare names that were taken as
// keys, and the variables net/http reads. None of them is read by the reader.
var removedVariables = []string{
	"OPENAI", "CEREBRAS",
	"SCHEMAFLUX_LLM_MAX_RETRIES", "SCHEMAFLUX_LLM_RETRY_BACKOFF", "SCHEMAFLUX_REPAIR_ATTEMPTS",
	"SCHEMAFLUX_PROMPT_REINFORCEMENT",
	"SCHEMAFLUX_DEBUG",
	"SCHEMAFLUX_LOG_LEVEL", "SCHEMAFLUX_LOG_FORMAT", "SCHEMAFLUX_LOG_FILE", "SCHEMAFLUX_LOG_BUFFER",
	"SCHEMAFLUX_LOG_DISABLE_CAPTURE", "SCHEMAFLUX_LOG_SOURCE", "SCHEMAFLUX_LOG_DISABLE_STDERR",
	"SCHEMAFLUX_ERROR_DETAIL",
	"SCHEMAFLUX_REQUEST_TRACKING", "SCHEMAFLUX_REQUEST_ID_STRATEGY", "SCHEMAFLUX_CORRELATION_ID_STRATEGY",
	"SCHEMAFLUX_REQUEST_ID_HEADER", "SCHEMAFLUX_CORRELATION_ID_HEADER",
	"SCHEMAFLUX_METRICS",
	"SCHEMAFLUX_TRACE", "SCHEMAFLUX_ENABLE_TRACING",
	"SCHEMAFLUX_TRACE_SAMPLE_RATE", "SCHEMAFLUX_EXPORT_TRACES_STDOUT", "SCHEMAFLUX_JAEGER_ENDPOINT",
	"SCHEMAFLUX_OTLP_ENDPOINT", "SCHEMAFLUX_ENVIRONMENT", "ENVIRONMENT", "ENV",
	"OTEL_EXPORTER_OTLP_TRACES_INSECURE", "OTEL_EXPORTER_OTLP_INSECURE",
	"SCHEMAFLUX_COST_INPUT_GPT_6_LUNA", "SCHEMAFLUX_COST_OUTPUT_GPT_6_LUNA", "SCHEMAFLUX_COST_INPUT_SMART",
	"SCHEMAFLUX_LIVE_TESTS", "SCHEMAFLUX_SEMANTIC_BUDGET_USD", "SCHEMAFLUX_UPDATE_API",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
}

// environmentOf is a lookup over a fixed set of variables. A variable that is in
// the map is set, even when its value is empty.
func environmentOf(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
}

// providerRow is one provider's row of the key table and of the base-URL table.
type providerRow struct {
	provider string
	keys     []string
	urls     []string
}

var providerRows = []providerRow{
	{"openai", []string{"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY"},
		[]string{"SCHEMAFLUX_OPENAI_BASE_URL", "OPENAI_BASE_URL"}},
	{"anthropic", []string{"SCHEMAFLUX_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
		[]string{"SCHEMAFLUX_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"}},
	{"openrouter", []string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		[]string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
	{"cerebras", []string{"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"},
		[]string{"SCHEMAFLUX_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"}},
	{"deepseek", []string{"SCHEMAFLUX_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"},
		[]string{"SCHEMAFLUX_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL"}},
	{"qwen", []string{"SCHEMAFLUX_QWEN_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY"},
		[]string{"SCHEMAFLUX_QWEN_BASE_URL", "QWEN_BASE_URL", "DASHSCOPE_BASE_URL"}},
	{"zai", []string{"SCHEMAFLUX_ZAI_API_KEY", "ZAI_API_KEY", "GLM_API_KEY"},
		[]string{"SCHEMAFLUX_ZAI_BASE_URL", "ZAI_BASE_URL", "GLM_BASE_URL"}},
	// The decision providers. jev-openrouter is the same protocol at OpenRouter,
	// under OpenRouter's key and base URL.
	{"jev", []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"},
		[]string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"}},
	{"jev-openrouter", []string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		[]string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
	{"jev-litellm", []string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY"},
		[]string{"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"}},
}

// readRow is one row of the table: the variables that say one thing, first set
// wins, and where the reader puts it.
type readRow struct {
	name  string
	names []string
	// value is a usable value for the variable, distinct for each name.
	value func(name string) string
	// show is what the reader reports for the row, as text.
	show func(Environment) string
	// reported is the text the reader should report for a value.
	reported func(value string) string
	// unset is what the reader reports when none of the row is set.
	unset string
}

func identity(value string) string { return value }

func readRows() []readRow {
	var rows []readRow
	for _, row := range providerRows {
		provider := row.provider
		rows = append(rows, readRow{
			name:     provider + " key",
			names:    append(append([]string{}, row.keys...), "SCHEMAFLUX_API_KEY"),
			value:    func(name string) string { return "key-of-" + name },
			show:     func(e Environment) string { return e.Keys[provider] },
			reported: identity,
		})
		rows = append(rows, readRow{
			name:     provider + " base URL",
			names:    row.urls,
			value:    func(name string) string { return "https://" + strings.ToLower(name) + ".example.test/v1" },
			show:     func(e Environment) string { return e.BaseURLs[provider] },
			reported: identity,
		})
	}
	rows = append(rows,
		readRow{
			name:     "provider",
			names:    []string{"SCHEMAFLUX_PROVIDER"},
			value:    func(string) string { return "Anthropic" },
			show:     func(e Environment) string { return e.Provider },
			reported: strings.ToLower,
			unset:    "openai",
		},
		readRow{
			name:     "model",
			names:    []string{"SCHEMAFLUX_MODEL"},
			value:    func(name string) string { return "model-of-" + name },
			show:     func(e Environment) string { return e.Model },
			reported: identity,
		},
		readRow{
			name:     "smart model",
			names:    []string{"SCHEMAFLUX_MODEL_SMART"},
			value:    func(name string) string { return "model-of-" + name },
			show:     func(e Environment) string { return e.Models[0] },
			reported: identity,
		},
		readRow{
			name:     "fast model",
			names:    []string{"SCHEMAFLUX_MODEL_FAST"},
			value:    func(name string) string { return "model-of-" + name },
			show:     func(e Environment) string { return e.Models[1] },
			reported: identity,
		},
		readRow{
			name:     "quick model",
			names:    []string{"SCHEMAFLUX_MODEL_QUICK"},
			value:    func(name string) string { return "model-of-" + name },
			show:     func(e Environment) string { return e.Models[2] },
			reported: identity,
		},
		readRow{
			name:     "timeout",
			names:    []string{"SCHEMAFLUX_TIMEOUT"},
			value:    func(string) string { return "45s" },
			show:     func(e Environment) string { return e.Timeout.String() },
			reported: func(string) string { return "45s" },
			unset:    "0s",
		},
		readRow{
			name:     "codex executable",
			names:    []string{"SCHEMAFLUX_CODEX_BIN"},
			value:    func(string) string { return `C:\tools\codex.exe` },
			show:     func(e Environment) string { return e.CodexBin },
			reported: identity,
		},
	)
	return rows
}

func TestReadEveryVariable(t *testing.T) {
	t.Parallel()

	rows := readRows()

	// The rows between them name every kept variable, apart from the shared key,
	// which closes each of the ten key rows. OpenRouter's variables are in two
	// providers' rows.
	seen := map[string]bool{}
	for _, row := range rows {
		for _, name := range row.names {
			seen[name] = true
		}
	}
	var covered []string
	for name := range seen {
		covered = append(covered, name)
	}
	sort.Strings(covered)
	want := append([]string{}, keptVariables...)
	sort.Strings(want)
	if !reflect.DeepEqual(covered, want) {
		t.Fatalf("the table names %d variables, want the %d of core_5 2.1\n got %v\nwant %v", len(covered), len(want), covered, want)
	}
	if len(keptVariables) != 48 {
		t.Fatalf("keptVariables holds %d names, want 48", len(keptVariables))
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			check := func(t *testing.T, vars map[string]string, wantText string) {
				t.Helper()
				got := row.show(Read(environmentOf(vars)))
				if got != wantText {
					t.Fatalf("reported %q, want %q (set: %v)", got, wantText, setNames(vars))
				}
			}

			t.Run("unset", func(t *testing.T) {
				check(t, map[string]string{}, row.unset)
			})
			for _, name := range row.names {
				t.Run("only "+name, func(t *testing.T) {
					check(t, map[string]string{name: row.value(name)}, row.reported(row.value(name)))
				})
			}
			t.Run("surrounding space is trimmed", func(t *testing.T) {
				name := row.names[0]
				check(t, map[string]string{name: " \t" + row.value(name) + "\r\n"}, row.reported(row.value(name)))
			})
			t.Run("the first of the row that is set wins", func(t *testing.T) {
				all := map[string]string{}
				for _, name := range row.names {
					all[name] = row.value(name)
				}
				check(t, all, row.reported(row.value(row.names[0])))
			})
			// A variable that is set to nothing says nothing: the next one decides.
			for k := 1; k < len(row.names); k++ {
				for _, blank := range []string{"", "   "} {
					t.Run(fmt.Sprintf("%d blank before %s (%q)", k, row.names[k], blank), func(t *testing.T) {
						vars := map[string]string{}
						for i, name := range row.names {
							if i < k {
								vars[name] = blank
							} else {
								vars[name] = row.value(name)
							}
						}
						check(t, vars, row.reported(row.value(row.names[k])))
					})
				}
			}
		})
	}
}

func setNames(vars map[string]string) []string {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestReadAsksForTheKeptVariablesAndNothingElse(t *testing.T) {
	t.Parallel()

	asked := map[string]bool{}
	Read(func(name string) (string, bool) {
		asked[name] = true
		return "", false
	})

	var got []string
	for name := range asked {
		got = append(got, name)
	}
	sort.Strings(got)
	want := append([]string{}, keptVariables...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read asked for %d names, want the %d kept ones\n got %v\nwant %v", len(got), len(want), got, want)
	}
}

func TestReadIgnoresTheRemovedVariables(t *testing.T) {
	t.Parallel()

	empty := Read(environmentOf(nil))
	kept := map[string]bool{}
	for _, name := range keptVariables {
		kept[name] = true
	}

	for _, name := range removedVariables {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if kept[name] {
				t.Fatalf("%s is in both tables", name)
			}
			got := Read(environmentOf(map[string]string{name: "a-value-the-reader-must-not-take"}))
			if !reflect.DeepEqual(got, empty) {
				t.Fatalf("setting %s changed what Read reports: %v", name, got)
			}
		})
	}

	t.Run("every name that is not kept, at once", func(t *testing.T) {
		t.Parallel()
		got := Read(func(name string) (string, bool) {
			if kept[name] {
				return "", false
			}
			return "a-value-the-reader-must-not-take", true
		})
		if !reflect.DeepEqual(got, empty) {
			t.Fatalf("variables outside the table changed what Read reports: %v", got)
		}
	})
}

func TestReadIgnoresTheBareNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vars map[string]string
	}{
		{"OPENAI alone", map[string]string{"OPENAI": "bare-key"}},
		{"CEREBRAS alone", map[string]string{"CEREBRAS": "bare-key"}},
		{"both", map[string]string{"OPENAI": "bare-key", "CEREBRAS": "bare-key"}},
		{"beside a provider selection", map[string]string{"SCHEMAFLUX_PROVIDER": "cerebras", "CEREBRAS": "bare-key"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Read(environmentOf(test.vars))
			for provider, key := range got.Keys {
				t.Errorf("Keys[%q] = a key (length %d) from a bare name", provider, len(key))
			}
		})
	}

	t.Run("the prefixed name beside the bare one is the one taken", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"OPENAI": "bare-key", "OPENAI_API_KEY": "real-key"}))
		if got.Keys["openai"] != "real-key" {
			t.Fatalf("Keys[openai] = %q, want real-key", got.Keys["openai"])
		}
	})
}

func TestReadKeysFallBackToTheSharedKey(t *testing.T) {
	t.Parallel()

	shared := map[string]string{"SCHEMAFLUX_API_KEY": "shared-key"}

	t.Run("every provider of the table gets it", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(shared))
		for _, row := range providerRows {
			if got.Keys[row.provider] != "shared-key" {
				t.Errorf("Keys[%s] = %q, want shared-key", row.provider, got.Keys[row.provider])
			}
		}
	})
	t.Run("the provider's own variable comes before it", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"SCHEMAFLUX_API_KEY": "shared-key", "GLM_API_KEY": "glm-key"}))
		if got.Keys["zai"] != "glm-key" || got.Keys["openai"] != "shared-key" {
			t.Fatalf("Keys = zai %q, openai %q", got.Keys["zai"], got.Keys["openai"])
		}
	})
	t.Run("a provider outside the table gets it when the environment selects that provider", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"SCHEMAFLUX_PROVIDER": "Groq", "SCHEMAFLUX_API_KEY": "shared-key"}))
		if got.Keys["groq"] != "shared-key" {
			t.Fatalf("Keys[groq] = %q, want shared-key", got.Keys["groq"])
		}
	})
	t.Run("a provider outside the table is not invented when the environment selects another", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(shared))
		if _, ok := got.Keys["groq"]; ok {
			t.Fatalf("Keys holds groq: %v", reflect.ValueOf(got.Keys).MapKeys())
		}
	})
	t.Run("no key at all gives no entry", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"SCHEMAFLUX_PROVIDER": "groq"}))
		if len(got.Keys) != 0 {
			t.Fatalf("Keys has %d entries, want none", len(got.Keys))
		}
	})
}

func TestReadProviderIsLowerCaseAndDefaultsToOpenAI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		set  bool
		want string
	}{
		{"", false, "openai"},
		{"", true, "openai"},
		{"   ", true, "openai"},
		{"openai", true, "openai"},
		{"OpenAI", true, "openai"},
		{" ANTHROPIC ", true, "anthropic"},
		{"deepseek", true, "deepseek"},
		{"Local", true, "local"},
		{"z.ai", true, "z.ai"},
		{"my-gateway", true, "my-gateway"},
		{"CEREBRAS\n", true, "cerebras"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%q set=%v", test.raw, test.set), func(t *testing.T) {
			t.Parallel()
			vars := map[string]string{}
			if test.set {
				vars["SCHEMAFLUX_PROVIDER"] = test.raw
			}
			if got := Read(environmentOf(vars)).Provider; got != test.want {
				t.Fatalf("Provider = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadTimeoutAcceptsPositiveDurations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1s", time.Second},
		{"1ms", time.Millisecond},
		{"250ms", 250 * time.Millisecond},
		{"2m", 2 * time.Minute},
		{"1m30s", 90 * time.Second},
		{"1.5s", 1500 * time.Millisecond},
		{"1h", time.Hour},
		{" 45s ", 45 * time.Second},
		{"90000000000ns", 90 * time.Second},
		{"1us", time.Microsecond},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			got := Read(environmentOf(map[string]string{"SCHEMAFLUX_TIMEOUT": test.raw}))
			if got.Timeout != test.want || len(got.Problems) != 0 {
				t.Fatalf("Timeout = %v, problems %v; want %v and none", got.Timeout, got.Problems, test.want)
			}
		})
	}
}

// secretMarker is in every malformed value below, so that a test can look for it
// anywhere a report is made.
const secretMarker = "SECRETMARKER"

func TestReadReportsProblemsByNameOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		variable string
		value    string
	}{
		{"SCHEMAFLUX_TIMEOUT", "soon-" + secretMarker},
		{"SCHEMAFLUX_TIMEOUT", "30"},
		{"SCHEMAFLUX_TIMEOUT", "0"},
		{"SCHEMAFLUX_TIMEOUT", "0s"},
		{"SCHEMAFLUX_TIMEOUT", "-5s"},
		{"SCHEMAFLUX_TIMEOUT", "1e9" + secretMarker},
		{"SCHEMAFLUX_TIMEOUT", "30 seconds"},
		{"SCHEMAFLUX_OPENAI_BASE_URL", "localhost:11434/" + secretMarker},
		{"OPENAI_BASE_URL", "not a url " + secretMarker},
		{"SCHEMAFLUX_ANTHROPIC_BASE_URL", "ftp://example.test/" + secretMarker},
		{"ANTHROPIC_BASE_URL", "https://"},
		{"SCHEMAFLUX_QWEN_BASE_URL", "https://user:" + secretMarker + "@"},
		{"GLM_BASE_URL", "://" + secretMarker},
		{"DASHSCOPE_BASE_URL", "/relative/" + secretMarker},
		{"SCHEMAFLUX_OPENAI_API_KEY", "sk-bad\nkey-" + secretMarker},
		{"ANTHROPIC_API_KEY", "sk-bad\x00key-" + secretMarker},
		{"SCHEMAFLUX_API_KEY", "sk-bad\x7fkey-" + secretMarker},
		{"DEEPSEEK_API_KEY", "sk-\tbad-" + secretMarker},
	}
	for _, test := range tests {
		t.Run(test.variable+" "+fmt.Sprintf("%.12q", test.value), func(t *testing.T) {
			t.Parallel()
			got := Read(environmentOf(map[string]string{test.variable: test.value}))

			if len(got.Problems) == 0 {
				t.Fatalf("no problem recorded for %s", test.variable)
			}
			named := false
			for _, problem := range got.Problems {
				if problem.Variable == test.variable {
					named = true
				}
			}
			if !named {
				t.Fatalf("no problem names %s: %+v", test.variable, got.Problems)
			}

			err := got.Problem(test.variable)
			if err == nil {
				t.Fatalf("Problem(%s) = nil", test.variable)
			}
			if types.KindOf(err) != types.KindConfiguration || !errors.Is(err, types.ErrConfiguration) {
				t.Errorf("Problem(%s) is not a configuration error: kind %v", test.variable, types.KindOf(err))
			}
			if !strings.Contains(err.Error(), test.variable) {
				t.Errorf("the error does not name %s: %v", test.variable, err)
			}

			// The value appears in nothing the reader produces.
			for label, text := range map[string]string{
				"error":   err.Error(),
				"%v":      fmt.Sprintf("%v", err),
				"%+v":     fmt.Sprintf("%+v", err),
				"%#v":     fmt.Sprintf("%#v", err),
				"problem": fmt.Sprintf("%+v %#v", got.Problems, got.Problems),
				"env %v":  fmt.Sprintf("%v", got),
				"env %+v": fmt.Sprintf("%+v", got),
				"env %#v": fmt.Sprintf("%#v", got),
				"env ptr": fmt.Sprintf("%+v %s", &got, &got),
			} {
				if strings.Contains(text, secretMarker) {
					t.Errorf("%s holds the value: %s", label, text)
				}
			}
		})
	}
}

func TestReadRecordsAMalformedValueAsNoValue(t *testing.T) {
	t.Parallel()

	t.Run("the timeout is zero", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"SCHEMAFLUX_TIMEOUT": "soon"}))
		if got.Timeout != 0 {
			t.Fatalf("Timeout = %v, want 0", got.Timeout)
		}
	})
	t.Run("the base URL is not stored", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"SCHEMAFLUX_OPENAI_BASE_URL": "localhost:11434"}))
		if _, ok := got.BaseURLs["openai"]; ok {
			t.Fatalf("BaseURLs holds a malformed value")
		}
	})
	t.Run("the key is not stored", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "bad\nkey"}))
		if _, ok := got.Keys["openai"]; ok {
			t.Fatalf("Keys holds a malformed value")
		}
	})
	t.Run("a malformed first variable is not skipped for the second", func(t *testing.T) {
		// A deployment must not run on a setting nobody chose.
		t.Parallel()
		got := Read(environmentOf(map[string]string{
			"SCHEMAFLUX_OPENAI_BASE_URL": "localhost:11434",
			"OPENAI_BASE_URL":            "https://fine.example.test/v1",
		}))
		if _, ok := got.BaseURLs["openai"]; ok {
			t.Fatalf("BaseURLs[openai] = the second variable's value")
		}
		if got.Problem("SCHEMAFLUX_OPENAI_BASE_URL") == nil {
			t.Fatalf("the malformed first variable is not reported")
		}
		if got.Problem("OPENAI_BASE_URL") != nil {
			t.Fatalf("the usable second variable is reported")
		}
	})
	t.Run("a malformed key is not skipped for the shared key", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{
			"SCHEMAFLUX_OPENAI_API_KEY": "bad\nkey",
			"SCHEMAFLUX_API_KEY":        "shared-key",
		}))
		if _, ok := got.Keys["openai"]; ok {
			t.Fatalf("Keys[openai] fell through to the shared key")
		}
		if got.Keys["anthropic"] != "shared-key" {
			t.Fatalf("the other providers lost the shared key: %q", got.Keys["anthropic"])
		}
	})
	t.Run("a problem in one variable leaves the others read", func(t *testing.T) {
		t.Parallel()
		got := Read(environmentOf(map[string]string{
			"SCHEMAFLUX_TIMEOUT":        "soon",
			"SCHEMAFLUX_MODEL":          "m",
			"SCHEMAFLUX_OPENAI_API_KEY": "k",
		}))
		if got.Model != "m" || got.Keys["openai"] != "k" || len(got.Problems) != 1 {
			t.Fatalf("got model %q, key %q, %d problems", got.Model, got.Keys["openai"], len(got.Problems))
		}
	})
}

func TestReadAcceptsUsableBaseURLs(t *testing.T) {
	t.Parallel()

	tests := []string{
		"https://api.example.test/v1",
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080",
		"http://[::1]:9000/v1",
		"HTTPS://Example.test/x",
		"https://example.test:8443/path/with/segments?x=1",
		"https://example.test/v1/",
		"https://user:pass@gateway.example.test/v1",
		" https://api.example.test/v1 ",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			got := Read(environmentOf(map[string]string{"SCHEMAFLUX_DEEPSEEK_BASE_URL": raw}))
			if len(got.Problems) != 0 || got.BaseURLs["deepseek"] != strings.TrimSpace(raw) {
				t.Fatalf("BaseURLs[deepseek] = %q, problems %v", got.BaseURLs["deepseek"], got.Problems)
			}
		})
	}
}

func TestProblemNamesTheFirstOfTheNamesThatIsMalformed(t *testing.T) {
	t.Parallel()

	env := Read(environmentOf(map[string]string{
		"SCHEMAFLUX_TIMEOUT":         "soon",
		"SCHEMAFLUX_OPENAI_BASE_URL": "localhost:1",
		"SCHEMAFLUX_MODEL":           "fine",
	}))

	tests := []struct {
		name  string
		names []string
		want  string // the variable the error names, or "" for nil
	}{
		{"no names", nil, ""},
		{"a malformed one", []string{"SCHEMAFLUX_TIMEOUT"}, "SCHEMAFLUX_TIMEOUT"},
		{"the order given decides", []string{"SCHEMAFLUX_OPENAI_BASE_URL", "SCHEMAFLUX_TIMEOUT"}, "SCHEMAFLUX_OPENAI_BASE_URL"},
		{"the other order", []string{"SCHEMAFLUX_TIMEOUT", "SCHEMAFLUX_OPENAI_BASE_URL"}, "SCHEMAFLUX_TIMEOUT"},
		{"a usable one first is skipped", []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_TIMEOUT"}, "SCHEMAFLUX_TIMEOUT"},
		{"only usable ones", []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_PROVIDER"}, ""},
		{"a variable that is not set", []string{"SCHEMAFLUX_ANTHROPIC_BASE_URL"}, ""},
		{"a name the library does not read", []string{"NOT_A_VARIABLE"}, ""},
		{"repeated", []string{"SCHEMAFLUX_TIMEOUT", "SCHEMAFLUX_TIMEOUT"}, "SCHEMAFLUX_TIMEOUT"},
		{"empty name", []string{""}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := env.Problem(test.names...)
			if test.want == "" {
				if err != nil {
					t.Fatalf("Problem = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Problem = %v, want one naming %s", err, test.want)
			}
			for _, other := range []string{"SCHEMAFLUX_TIMEOUT", "SCHEMAFLUX_OPENAI_BASE_URL"} {
				if other != test.want && strings.Contains(err.Error(), other) {
					t.Fatalf("Problem names %s as well: %v", other, err)
				}
			}
		})
	}

	t.Run("a nil environment has none", func(t *testing.T) {
		t.Parallel()
		var none *Environment
		if err := none.Problem("SCHEMAFLUX_TIMEOUT"); err != nil {
			t.Fatalf("Problem on nil = %v", err)
		}
	})
}

func TestReadOfNothingIsTheDefaults(t *testing.T) {
	t.Parallel()

	for name, lookup := range map[string]func(string) (string, bool){
		"a nil lookup":   nil,
		"an empty one":   environmentOf(nil),
		"all set blank":  func(string) (string, bool) { return "", true },
		"all whitespace": func(string) (string, bool) { return " \t ", true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := Read(lookup)
			if got.Provider != "openai" || got.Model != "" || got.Models != [3]string{} || got.Timeout != 0 ||
				got.CodexBin != "" || len(got.Keys) != 0 || len(got.BaseURLs) != 0 || len(got.Problems) != 0 {
				t.Fatalf("Read = %+v", got)
			}
			if got.Keys == nil || got.BaseURLs == nil {
				t.Fatalf("a map is nil: a caller may add to it")
			}
		})
	}
}

func TestReadReturnsMapsOfItsOwn(t *testing.T) {
	t.Parallel()

	vars := map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "k", "SCHEMAFLUX_OPENAI_BASE_URL": "https://a.example.test"}
	first := Read(environmentOf(vars))
	first.Keys["openai"] = "changed"
	first.BaseURLs["openai"] = "changed"

	second := Read(environmentOf(vars))
	if second.Keys["openai"] != "k" || second.BaseURLs["openai"] != "https://a.example.test" {
		t.Fatalf("a change to one Read's maps reached another's: %v %v", second.Keys, second.BaseURLs)
	}
}

// Read takes its lookup and nothing else: a program whose process environment
// says something else gets what the lookup says.
func TestReadDoesNotTouchTheProcessEnvironment(t *testing.T) {
	t.Setenv("SCHEMAFLUX_PROVIDER", "anthropic")
	t.Setenv("SCHEMAFLUX_TIMEOUT", "soon")
	t.Setenv("SCHEMAFLUX_OPENAI_API_KEY", "from-the-process")

	got := Read(environmentOf(map[string]string{"SCHEMAFLUX_MODEL": "m"}))
	if got.Provider != "openai" || len(got.Keys) != 0 || len(got.Problems) != 0 || got.Model != "m" {
		t.Fatalf("Read reached the process environment: %+v", got)
	}
}

func TestEnvironmentPrintsNoValue(t *testing.T) {
	t.Parallel()

	env := Read(environmentOf(map[string]string{
		"SCHEMAFLUX_PROVIDER":          "anthropic",
		"SCHEMAFLUX_ANTHROPIC_API_KEY": "key-" + secretMarker,
		"SCHEMAFLUX_OPENAI_BASE_URL":   "https://user:" + secretMarker + "@gateway.example.test",
		"SCHEMAFLUX_MODEL":             "model-" + secretMarker,
		"SCHEMAFLUX_MODEL_SMART":       "smart-" + secretMarker,
		"SCHEMAFLUX_CODEX_BIN":         "/opt/" + secretMarker,
		"SCHEMAFLUX_TIMEOUT":           "45s",
	}))

	for label, text := range map[string]string{
		"%v":        fmt.Sprintf("%v", env),
		"%+v":       fmt.Sprintf("%+v", env),
		"%#v":       fmt.Sprintf("%#v", env),
		"%s":        fmt.Sprintf("%s", env),
		"Sprint":    fmt.Sprint(env),
		"pointer":   fmt.Sprintf("%+v", &env),
		"in a list": fmt.Sprintf("%v", []Environment{env}),
	} {
		if strings.Contains(text, secretMarker) {
			t.Errorf("%s prints a value: %s", label, text)
		}
		if !strings.Contains(text, "anthropic") {
			t.Errorf("%s does not say which provider: %s", label, text)
		}
	}
}

func TestProcessReadsOnce(t *testing.T) {
	t.Parallel()

	t.Run("a once reads its lookup once however many ask", func(t *testing.T) {
		t.Parallel()
		var once onceEnvironment
		var calls atomic.Int64
		lookup := func(name string) (string, bool) {
			if name == "SCHEMAFLUX_PROVIDER" {
				calls.Add(1)
			}
			return "", false
		}

		var wg sync.WaitGroup
		results := make([]*Environment, 32)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = once.get(lookup)
			}()
		}
		wg.Wait()

		if calls.Load() != 1 {
			t.Fatalf("the lookup was asked for the provider %d times, want 1", calls.Load())
		}
		for i, got := range results {
			if got == nil || got != results[0] {
				t.Fatalf("caller %d got a different value", i)
			}
		}
	})

	t.Run("a second lookup is not used", func(t *testing.T) {
		t.Parallel()
		var once onceEnvironment
		first := once.get(environmentOf(map[string]string{"SCHEMAFLUX_MODEL": "first"}))
		second := once.get(environmentOf(map[string]string{"SCHEMAFLUX_MODEL": "second"}))
		if first != second || second.Model != "first" {
			t.Fatalf("the second call read again: %q", second.Model)
		}
	})

	t.Run("Process is one value", func(t *testing.T) {
		// It reads the process environment, which the tests that set one have put back by the time a parallel test runs.
		first, second := Process(), Process()
		if first == nil || first != second {
			t.Fatalf("Process returned %p and %p", first, second)
		}
		if first.Provider == "" || first.Keys == nil || first.BaseURLs == nil {
			t.Fatalf("Process returned an unread value: %+v", first)
		}
	})
}

// The variables net/http reads for the proxy are not this library's, and the
// reader's comment is where that is said.
func TestReadDocumentsTheProxyVariables(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "read.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var doc string
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Read" && fn.Recv == nil && fn.Doc != nil {
			doc = fn.Doc.Text()
		}
	}
	if doc == "" {
		t.Fatal("Read has no doc comment")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "net/http"} {
		if !strings.Contains(doc, name) {
			t.Errorf("the doc comment of Read does not mention %s", name)
		}
	}
	// Every variable the reader takes is in the doc comment, so that the comment
	// is the one place a variable is documented.
	for _, name := range keptVariables {
		if !strings.Contains(doc, name) {
			t.Errorf("the doc comment of Read does not list %s", name)
		}
	}
}

// unsetModelVariables makes the process-wide model lookup see no variable.
func unsetModelVariables(t *testing.T) {
	t.Helper()
	t.Cleanup(ReloadEnvironment)
	for _, name := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
		t.Setenv(name, "")
	}
	ReloadEnvironment()
}

func TestBuiltInModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		tier     types.Speed
		want     string
	}{
		{"openai", types.Smart, ModelDefaultSmart},
		{"openai", types.Fast, ModelDefaultFast},
		{"openai", types.Quick, ModelDefaultQuick},
		{"", types.Fast, ModelDefaultFast},
		{" OpenAI ", types.Smart, ModelDefaultSmart},
		{"anthropic", types.Smart, "claude-3-5-sonnet-20240620"},
		{"anthropic", types.Fast, "claude-3-haiku-20240307"},
		{"anthropic", types.Quick, "claude-3-haiku-20240307"},
		{"OpenRouter", types.Smart, "openai/gpt-4o"},
		{"openrouter", types.Quick, "openai/gpt-4o-mini"},
		{"cerebras", types.Smart, "gemma-4-31b"},
		{"cerebras", types.Quick, "gemma-4-31b"},
		{"deepseek", types.Smart, "deepseek-reasoner"},
		{"deepseek", types.Fast, "deepseek-chat"},
		{"groq", types.Smart, "llama-3.3-70b-versatile"},
		{"mistral", types.Quick, "mistral-small-latest"},
		{"local", types.Smart, "local-mock"},
		// The decision providers name the model their preset sends, at every tier.
		{"jev", types.Smart, "jev-latest"},
		{"jev", types.Fast, "jev-latest"},
		{"jev", types.Quick, "jev-latest"},
		{" JEV ", types.Fast, "jev-latest"},
		{"jev-openrouter", types.Smart, "~typesafe/jev-latest"},
		{"jev-openrouter", types.Quick, "~typesafe/jev-latest"},
		{"jev-litellm", types.Smart, "jev-latest"},
		{"jev-litellm", types.TierUnset, "jev-latest"},
		{"qwen", types.Smart, ""},
		{"zai", types.Fast, ""},
		{"my-gateway", types.Fast, ""},
		{"my-gateway", types.Smart, ""},
		// A tier the table has no row for is the Fast one, as GetModel has it.
		{"anthropic", types.TierUnset, "claude-3-haiku-20240307"},
		{"openai", types.TierUnset, ModelDefaultFast},
		{"openai", types.Speed(99), ModelDefaultFast},
		{"deepseek", types.Speed(-1), "deepseek-chat"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%s/%d", test.provider, test.tier), func(t *testing.T) {
			t.Parallel()
			if got := BuiltInModel(test.provider, test.tier); got != test.want {
				t.Fatalf("BuiltInModel(%q, %d) = %q, want %q", test.provider, test.tier, got, test.want)
			}
		})
	}
}

// BuiltInModel is the part of GetModel that does not read the environment, and
// the two must not drift apart.
func TestBuiltInModelIsWhatGetModelAnswersWithNoVariables(t *testing.T) {
	unsetModelVariables(t)

	providers := append(KnownProviders(), "", " OpenAI ", "ANTHROPIC", "qwen", "zai", "local", "mock", "my-gateway")
	tiers := []types.Speed{types.TierUnset, types.Smart, types.Fast, types.Quick, types.Speed(-1), types.Speed(4), types.Speed(99)}
	for _, provider := range providers {
		for _, tier := range tiers {
			if got, want := BuiltInModel(provider, tier), GetModel(tier, provider); got != want {
				t.Errorf("BuiltInModel(%q, %d) = %q, GetModel = %q", provider, tier, got, want)
			}
		}
	}
}
