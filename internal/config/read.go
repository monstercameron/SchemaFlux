package config

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Environment is what the library takes from the environment, read once.
//
// Nothing in it is written after Read returns it. The value Process returns is
// shared, so a caller that wants to change a map copies it first.
//
// A key, a base URL or a model name is a value somebody configured, and a key or
// a URL may hold a credential. Printing an Environment names what is set and
// never what it is set to.
type Environment struct {
	// Provider is SCHEMAFLUX_PROVIDER in lower case, or "openai" when it is unset.
	Provider string

	// Keys holds, for each of the ten providers that have variables of their own
	// (the seven that generate text and the three decision providers), the first
	// of those that is set, and otherwise SCHEMAFLUX_API_KEY. When Provider is
	// another provider, Keys holds SCHEMAFLUX_API_KEY under that name. A provider
	// with no key set has no entry.
	Keys map[string]string

	// BaseURLs holds, for each of the ten providers, the first of its base-URL
	// variables that is set. A provider with none set has no entry.
	BaseURLs map[string]string

	// Model is SCHEMAFLUX_MODEL, which names one model for every tier.
	Model string

	// Models is SCHEMAFLUX_MODEL_SMART, _FAST and _QUICK, in that order.
	Models [3]string

	// Timeout is SCHEMAFLUX_TIMEOUT, or zero when it is unset or unusable.
	Timeout time.Duration

	// CodexBin is SCHEMAFLUX_CODEX_BIN, the codex executable, or empty.
	CodexBin string

	// Problems is every variable that is set to a value the library cannot use,
	// in the order they were read. It names variables and never their values.
	Problems []EnvProblem
}

const (
	sharedKeyVariable = "SCHEMAFLUX_API_KEY"

	wantKey     = "a value without control characters"
	wantBaseURL = "an absolute http or https URL"
	wantTimeout = "a positive duration such as 30s"
)

// providerVariables are the variables each provider's key and base URL are taken
// from, in the order they are tried. It is the table of client.go's
// providerAPIKeyEnvVars and providerBaseURLEnvVars, less the bare OPENAI and
// CEREBRAS names, which are too generic to claim as a credential.
//
// The last three rows are the decision providers. Their variables are those the
// presets in internal/llm (systemone.go) declare, which this package cannot read
// because internal/llm imports it: a test in internal/api/client holds the two to
// each other. "jev-openrouter" is the same protocol at OpenRouter and takes
// OpenRouter's own key and base URL, as the preset says.
var providerVariables = []struct {
	provider string
	keys     []string
	baseURLs []string
}{
	{"openai",
		[]string{"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY"},
		[]string{"SCHEMAFLUX_OPENAI_BASE_URL", "OPENAI_BASE_URL"}},
	{"anthropic",
		[]string{"SCHEMAFLUX_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
		[]string{"SCHEMAFLUX_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"}},
	{"openrouter",
		[]string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		[]string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
	{"cerebras",
		[]string{"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"},
		[]string{"SCHEMAFLUX_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"}},
	{"deepseek",
		[]string{"SCHEMAFLUX_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"},
		[]string{"SCHEMAFLUX_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL"}},
	{"qwen",
		[]string{"SCHEMAFLUX_QWEN_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY"},
		[]string{"SCHEMAFLUX_QWEN_BASE_URL", "QWEN_BASE_URL", "DASHSCOPE_BASE_URL"}},
	{"zai",
		[]string{"SCHEMAFLUX_ZAI_API_KEY", "ZAI_API_KEY", "GLM_API_KEY"},
		[]string{"SCHEMAFLUX_ZAI_BASE_URL", "ZAI_BASE_URL", "GLM_BASE_URL"}},
	{"jev",
		[]string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"},
		[]string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"}},
	{"jev-openrouter",
		[]string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		[]string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
	{"jev-litellm",
		[]string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY"},
		[]string{"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"}},
}

// Read parses an environment. It is pure: lookup is the only thing it reads, so
// a test gives it any environment it likes and runs in parallel with any other.
// A nil lookup is an empty environment.
//
// These are every variable it reads. A variable that is set to nothing, or to
// space, counts as unset, and a value is read without the space around it. In a
// row of variables the first that is set wins.
//
//	SCHEMAFLUX_PROVIDER      the provider the environment selects; "openai" when unset
//	SCHEMAFLUX_API_KEY       the key of any provider whose own variables are not set
//	the key of each provider, before SCHEMAFLUX_API_KEY:
//	  openai       SCHEMAFLUX_OPENAI_API_KEY, OPENAI_API_KEY
//	  anthropic    SCHEMAFLUX_ANTHROPIC_API_KEY, ANTHROPIC_API_KEY
//	  openrouter   SCHEMAFLUX_OPENROUTER_API_KEY, OPENROUTER_API_KEY
//	  cerebras     SCHEMAFLUX_CEREBRAS_API_KEY, CEREBRAS_API_KEY
//	  deepseek     SCHEMAFLUX_DEEPSEEK_API_KEY, DEEPSEEK_API_KEY
//	  qwen         SCHEMAFLUX_QWEN_API_KEY, QWEN_API_KEY, DASHSCOPE_API_KEY
//	  zai          SCHEMAFLUX_ZAI_API_KEY, ZAI_API_KEY, GLM_API_KEY
//	  jev          SCHEMAFLUX_TYPESAFE_API_KEY, TYPESAFE_API_KEY
//	  jev-openrouter  SCHEMAFLUX_OPENROUTER_API_KEY, OPENROUTER_API_KEY
//	  jev-litellm  SCHEMAFLUX_LITELLM_API_KEY, LITELLM_API_KEY
//	the base URL of each provider:
//	  openai       SCHEMAFLUX_OPENAI_BASE_URL, OPENAI_BASE_URL
//	  anthropic    SCHEMAFLUX_ANTHROPIC_BASE_URL, ANTHROPIC_BASE_URL
//	  openrouter   SCHEMAFLUX_OPENROUTER_BASE_URL, OPENROUTER_BASE_URL
//	  cerebras     SCHEMAFLUX_CEREBRAS_BASE_URL, CEREBRAS_BASE_URL
//	  deepseek     SCHEMAFLUX_DEEPSEEK_BASE_URL, DEEPSEEK_BASE_URL
//	  qwen         SCHEMAFLUX_QWEN_BASE_URL, QWEN_BASE_URL, DASHSCOPE_BASE_URL
//	  zai          SCHEMAFLUX_ZAI_BASE_URL, ZAI_BASE_URL, GLM_BASE_URL
//	  jev          SCHEMAFLUX_TYPESAFE_BASE_URL, TYPESAFE_BASE_URL
//	  jev-openrouter  SCHEMAFLUX_OPENROUTER_BASE_URL, OPENROUTER_BASE_URL
//	  jev-litellm  SCHEMAFLUX_LITELLM_BASE_URL, LITELLM_BASE_URL
//	SCHEMAFLUX_MODEL         one model for every tier
//	SCHEMAFLUX_MODEL_SMART, SCHEMAFLUX_MODEL_FAST, SCHEMAFLUX_MODEL_QUICK   one model for each tier
//	SCHEMAFLUX_TIMEOUT       a positive duration
//	SCHEMAFLUX_CODEX_BIN     the codex executable
//
// The names that are not here are not read, the bare OPENAI and CEREBRAS among
// them, which are too generic to take as a credential. The proxy variables
// HTTP_PROXY, HTTPS_PROXY and NO_PROXY, and their lower-case forms http_proxy,
// https_proxy and no_proxy, are read by net/http for every request the built-in
// providers send, and not by this library.
//
// A variable whose value cannot be used (a timeout that is not a positive
// duration, a base URL that is not an absolute http or https URL, a key with a
// control character in it) is recorded in Problems, by name and never by value,
// and gives no value. It is not skipped for the next variable of its row: a
// setting nobody chose is worse than none. Problem turns it into the error the
// first call that needs it returns.
func Read(lookup func(string) (string, bool)) Environment {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	r := envReader{lookup: lookup, reported: map[string]bool{}}
	env := Environment{
		Provider: "openai",
		Keys:     map[string]string{},
		BaseURLs: map[string]string{},
	}

	if _, value, ok := r.firstSet("SCHEMAFLUX_PROVIDER"); ok {
		env.Provider = strings.ToLower(value)
	}

	for _, row := range providerVariables {
		r.addKey(&env, row.provider, append(append([]string{}, row.keys...), sharedKeyVariable))
		if name, value, ok := r.firstSet(row.baseURLs...); ok {
			if usableBaseURL(value) {
				env.BaseURLs[row.provider] = value
			} else {
				r.problem(&env, name, wantBaseURL)
			}
		}
	}
	// The environment's own provider has the shared key when it is not one of the
	// seven that have variables of their own.
	if !hasProviderVariables(env.Provider) {
		r.addKey(&env, env.Provider, []string{sharedKeyVariable})
	}

	if _, value, ok := r.firstSet("SCHEMAFLUX_MODEL"); ok {
		env.Model = value
	}
	for i, name := range []string{"SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
		if _, value, ok := r.firstSet(name); ok {
			env.Models[i] = value
		}
	}

	if name, value, ok := r.firstSet("SCHEMAFLUX_TIMEOUT"); ok {
		if timeout, err := time.ParseDuration(value); err == nil && timeout > 0 {
			env.Timeout = timeout
		} else {
			r.problem(&env, name, wantTimeout)
		}
	}

	if _, value, ok := r.firstSet("SCHEMAFLUX_CODEX_BIN"); ok {
		env.CodexBin = value
	}
	return env
}

func hasProviderVariables(provider string) bool {
	for _, row := range providerVariables {
		if row.provider == provider {
			return true
		}
	}
	return false
}

// envReader is one Read: the lookup it was given, and the variables it has
// already reported, so that the shared key, which closes every provider's row,
// is reported once.
type envReader struct {
	lookup   func(string) (string, bool)
	reported map[string]bool
}

// firstSet returns the first of names that is set to something, and its value
// without the space around it.
func (r *envReader) firstSet(names ...string) (name, value string, ok bool) {
	for _, candidate := range names {
		raw, set := r.lookup(candidate)
		if !set {
			continue
		}
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return candidate, trimmed, true
		}
	}
	return "", "", false
}

// addKey records the first of names that is set as provider's key, or the
// problem with it.
func (r *envReader) addKey(env *Environment, provider string, names []string) {
	name, value, ok := r.firstSet(names...)
	if !ok {
		return
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		r.problem(env, name, wantKey)
		return
	}
	env.Keys[provider] = value
}

// problem records a variable the library cannot use, once.
func (r *envReader) problem(env *Environment, name, want string) {
	if r.reported[name] {
		return
	}
	r.reported[name] = true
	env.Problems = append(env.Problems, EnvProblem{Variable: name, Want: want})
}

// usableBaseURL reports whether value is an absolute http or https URL with a
// host, which is what a request can be sent to. The parse error is dropped: it
// quotes the URL, and a URL may hold a credential.
func usableBaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

// Problem returns a configuration error naming the first of names that the
// environment holds a value for that cannot be used, and never the value. It is
// nil when none of them is malformed, and for a variable that is not set.
//
// It is what a call that needs a setting returns when the setting is one of
// Problems: the call asks for the names it needs, so a malformed base URL for a
// provider that is not in use fails nothing.
func (e *Environment) Problem(names ...string) error {
	if e == nil {
		return nil
	}
	for _, name := range names {
		for _, problem := range e.Problems {
			if problem.Variable == name {
				return types.Newf(types.KindConfiguration, "config",
					"%s is not usable; it takes %s", problem.Variable, problem.Want)
			}
		}
	}
	return nil
}

// String names what is set and not what it is set to.
func (e Environment) String() string {
	set := func(value string) string {
		if value == "" {
			return "unset"
		}
		return "set"
	}
	names := func(values map[string]string) []string {
		list := make([]string, 0, len(values))
		for name := range values {
			list = append(list, name)
		}
		sort.Strings(list)
		return list
	}
	problems := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		problems = append(problems, problem.Variable)
	}
	return fmt.Sprintf("config.Environment{Provider: %s, Keys: %v, BaseURLs: %v, Model: %s, Models: [%s %s %s], Timeout: %s, CodexBin: %s, Problems: %v}",
		e.Provider, names(e.Keys), names(e.BaseURLs), set(e.Model),
		set(e.Models[0]), set(e.Models[1]), set(e.Models[2]), e.Timeout, set(e.CodexBin), problems)
}

// GoString is String, so that %#v names what is set and not what it is set to.
func (e Environment) GoString() string { return e.String() }

// onceEnvironment holds an Environment read the first time it is asked for. It is
// a type of its own so that a test can read once from a lookup it controls.
type onceEnvironment struct {
	once  sync.Once
	value *Environment
}

func (o *onceEnvironment) get(lookup func(string) (string, bool)) *Environment {
	o.once.Do(func() {
		env := Read(lookup)
		o.value = &env
	})
	return o.value
}

var processEnvironment onceEnvironment

// Process returns Read(os.LookupEnv), made once: the environment as it was the
// first time anything asked, however many ask. The value is shared and is not
// to be modified. A program that changes its environment afterwards and wants
// the change to count builds what it needs from Read.
func Process() *Environment { return processEnvironment.get(os.LookupEnv) }

// BuiltInModel returns the model the library names for provider at tier when no
// variable and no setting says otherwise: the provider's row of the model table,
// or the OpenAI tier constants for OpenAI, and the empty string for a provider
// with neither. A tier the table has no entry for gets the Fast one.
//
// It is GetModel with the environment taken out, so that a provider can answer
// which model a tier means for it without the answer depending on a process-wide
// variable.
func BuiltInModel(provider string, tier types.Speed) string {
	if models, known := providerModels[normaliseProviderName(provider)]; known {
		if model := models[tier]; model != "" {
			return model
		}
		return models[types.Fast]
	}
	if !isOpenAIProvider(provider) {
		return ""
	}
	switch tier {
	case types.Smart:
		return ModelDefaultSmart
	case types.Fast:
		return ModelDefaultFast
	case types.Quick:
		return ModelDefaultQuick
	default:
		return ModelDefaultFast
	}
}
