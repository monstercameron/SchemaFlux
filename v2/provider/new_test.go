package provider

import (
	"errors"
	"slices"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
)

// variableRow is the variables of one provider: the ones its key is read from and
// the ones its base URL is read from.
type variableRow struct{ keys, baseURLs []string }

// providerVariables is the row of every built-in provider that reads a key, as
// the root holds it (bind.ProviderVariables). The providers are the ones
// config.Read has a key for.
var providerVariables = func() map[string]variableRow {
	rows := map[string]variableRow{}
	for name := range config.Read(lookupOf(sharedKeyVariable, "shared")).Keys {
		keys, baseURLs, ok := bind.ProviderVariables(name)
		if !ok {
			panic("the root has no row for " + name)
		}
		rows[name] = variableRow{keys, baseURLs}
	}
	return rows
}()

// secretValue is a value set in a variable that no error may repeat.
const secretValue = "VALUE-that-must-not-appear-9f2c"

func TestTheVariableTableIsWhatTheEnvironmentReaderReads(t *testing.T) {
	t.Parallel()
	for name, row := range providerVariables {
		for _, variable := range row.keys {
			t.Run(name+" key "+variable, func(t *testing.T) {
				t.Parallel()
				env := config.Read(lookupOf(variable, secretValue))
				if got := env.Keys[name]; got != secretValue {
					t.Errorf("config.Read gives %s the key %q, want the value of %s", name, got, variable)
				}
			})
		}
		for _, variable := range row.baseURLs {
			t.Run(name+" address "+variable, func(t *testing.T) {
				t.Parallel()
				env := config.Read(lookupOf(variable, "https://example.test/v1"))
				if got := env.BaseURLs[name]; got != "https://example.test/v1" {
					t.Errorf("config.Read gives %s the address %q, want the value of %s", name, got, variable)
				}
			})
		}
	}
	t.Run("every provider the reader knows has a row", func(t *testing.T) {
		t.Parallel()
		env := config.Read(lookupOf(sharedKeyVariable, "shared"))
		for name := range env.Keys {
			if _, ok := providerVariables[name]; !ok {
				t.Errorf("config.Read has a key for %s and this package has no row for it", name)
			}
		}
		if len(env.Keys) != len(providerVariables) {
			t.Errorf("config.Read has %d providers with keys, this package %d", len(env.Keys), len(providerVariables))
		}
	})
	t.Run("the decision providers' variables are the presets' own", func(t *testing.T) {
		t.Parallel()
		for _, preset := range []llm.SystemOnePreset{llm.SystemOneTypeSafe, llm.SystemOneOpenRouter, llm.SystemOneLiteLLM} {
			row := providerVariables[preset.Name]
			if !slices.Equal(row.keys, preset.KeyVariables) || !slices.Equal(row.baseURLs, preset.BaseURLVariables) {
				t.Errorf("%s: this package reads %v and %v, the preset %v and %v",
					preset.Name, row.keys, row.baseURLs, preset.KeyVariables, preset.BaseURLVariables)
			}
		}
	})
}

func TestEveryKeyVariableOfEveryProviderGivesItsKey(t *testing.T) {
	t.Parallel()
	for name := range providerVariables {
		variables, _ := variablesFor(name)
		for _, variable := range variables.keys {
			t.Run(name+" from "+variable, func(t *testing.T) {
				t.Parallel()
				env := config.Read(lookupOf(variable, "  "+secretValue+"\n"))
				cfg, err := withEnvironment(opNew, name, Config{}, &env)
				if err != nil {
					t.Fatalf("withEnvironment: %v", err)
				}
				if cfg.APIKey != secretValue {
					t.Errorf("the key is %q, want the value of %s without the space around it", cfg.APIKey, variable)
				}
			})
		}
		for _, variable := range variables.baseURLs {
			t.Run(name+" address from "+variable, func(t *testing.T) {
				t.Parallel()
				env := config.Read(lookupOf(variables.keys[0], "k", variable, "https://example.test/v1"))
				cfg, err := withEnvironment(opNew, name, Config{}, &env)
				if err != nil {
					t.Fatalf("withEnvironment: %v", err)
				}
				if cfg.BaseURL != "https://example.test/v1" {
					t.Errorf("the address is %q, want the value of %s", cfg.BaseURL, variable)
				}
			})
		}
	}
}

func TestWithEnvironmentChoosesTheKeyAndTheAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		which   string
		env     []string
		cfg     Config
		wantKey string
		wantURL string
		wantErr string // a substring of the error, or empty
	}{
		{name: "a key in the Config wins over the provider's variable", which: "openai",
			env: []string{"OPENAI_API_KEY", "from-env"}, cfg: Config{APIKey: "from-config"}, wantKey: "from-config"},
		{name: "a key in the Config wins over the shared variable", which: "anthropic",
			env: []string{sharedKeyVariable, "from-env"}, cfg: Config{APIKey: "from-config"}, wantKey: "from-config"},
		{name: "the SCHEMAFLUX_ form wins over the vendor's", which: "openai",
			env: []string{"OPENAI_API_KEY", "vendor", "SCHEMAFLUX_OPENAI_API_KEY", "ours"}, wantKey: "ours"},
		{name: "the vendor's variable is used when the SCHEMAFLUX_ one is not set", which: "openai",
			env: []string{"OPENAI_API_KEY", "vendor"}, wantKey: "vendor"},
		{name: "the provider's own variable wins over the shared one", which: "deepseek",
			env: []string{sharedKeyVariable, "shared", "DEEPSEEK_API_KEY", "own"}, wantKey: "own"},
		{name: "the shared variable is the key when the provider's own are not set", which: "cerebras",
			env: []string{sharedKeyVariable, "shared"}, wantKey: "shared"},
		{name: "a variable set to space counts as unset", which: "zai",
			env: []string{"SCHEMAFLUX_ZAI_API_KEY", "   ", "GLM_API_KEY", "glm"}, wantKey: "glm"},
		{name: "the third variable of a row is read", which: "qwen",
			env: []string{"DASHSCOPE_API_KEY", "dash"}, wantKey: "dash"},
		{name: "the first variable of a row set wins over the third", which: "qwen",
			env: []string{"DASHSCOPE_API_KEY", "dash", "QWEN_API_KEY", "qwen"}, wantKey: "qwen"},
		{name: "the decision provider through OpenRouter reads OpenRouter's key", which: "jev-openrouter",
			env: []string{"OPENROUTER_API_KEY", "router"}, wantKey: "router"},
		{name: "TypeSafe's decision provider reads TypeSafe's key", which: "jev",
			env: []string{"TYPESAFE_API_KEY", "typesafe", "OPENAI_API_KEY", "other"}, wantKey: "typesafe"},
		{name: "a key set only for another provider is not used", which: "anthropic",
			env: []string{"OPENAI_API_KEY", secretValue}, wantErr: "SCHEMAFLUX_ANTHROPIC_API_KEY, ANTHROPIC_API_KEY, SCHEMAFLUX_API_KEY"},
		{name: "a Config key of only space is no key", which: "openai",
			env: []string{"OPENAI_API_KEY", "from-env"}, cfg: Config{APIKey: "   "}, wantKey: "from-env"},
		{name: "a Config key is trimmed", which: "openai", cfg: Config{APIKey: "  padded\n"}, wantKey: "padded"},
		{name: "the address in the Config wins over the variable", which: "openai",
			env: []string{"OPENAI_API_KEY", "k", "OPENAI_BASE_URL", "https://env.test/v1"},
			cfg: Config{BaseURL: "https://config.test/v1"}, wantKey: "k", wantURL: "https://config.test/v1"},
		{name: "the address comes from the variable when the Config has none", which: "openrouter",
			env: []string{"OPENROUTER_API_KEY", "k", "OPENROUTER_BASE_URL", "https://env.test/v1"}, wantKey: "k", wantURL: "https://env.test/v1"},
		{name: "no address anywhere is left for the provider's own", which: "openai",
			env: []string{"OPENAI_API_KEY", "k"}, wantKey: "k"},
		{name: "a key with a control character is refused by name", which: "openai",
			env: []string{"OPENAI_API_KEY", "bad\x01" + secretValue}, wantErr: "OPENAI_API_KEY is not usable"},
		{name: "a key that cannot be used is not replaced by the next variable of its row", which: "openai",
			env: []string{"SCHEMAFLUX_OPENAI_API_KEY", "bad\x01key", "OPENAI_API_KEY", "good"}, wantErr: "SCHEMAFLUX_OPENAI_API_KEY is not usable"},
		{name: "a key that cannot be used is not read when the Config has one", which: "openai",
			env: []string{"OPENAI_API_KEY", "bad\x01key"}, cfg: Config{APIKey: "from-config"}, wantKey: "from-config"},
		{name: "an address that is not a URL is refused by name", which: "openai",
			env: []string{"OPENAI_API_KEY", "k", "OPENAI_BASE_URL", "not a url " + secretValue}, wantErr: "OPENAI_BASE_URL is not usable"},
		{name: "an address that cannot be used is not read when the Config has one", which: "openai",
			env: []string{"OPENAI_API_KEY", "k", "OPENAI_BASE_URL", "nonsense"}, cfg: Config{BaseURL: "https://config.test"},
			wantKey: "k", wantURL: "https://config.test"},
		{name: "a missing key names the variables and the Config field", which: "openai",
			wantErr: "Config.APIKey, or one of SCHEMAFLUX_OPENAI_API_KEY, OPENAI_API_KEY, SCHEMAFLUX_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := config.Read(lookupOf(tt.env...))
			got, err := withEnvironment(opNew, tt.which, tt.cfg, &env)
			if tt.wantErr != "" {
				e := errorOf(t, err)
				if e.Kind != schemaflux.ErrConfiguration {
					t.Errorf("kind = %v, want configuration", e.Kind)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not say %q", err, tt.wantErr)
				}
				for _, text := range []string{err.Error(), e.Detail()} {
					if strings.Contains(text, secretValue) || strings.Contains(text, "bad\x01") {
						t.Errorf("the error repeats a value from the environment: %q", text)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("withEnvironment: %v", err)
			}
			if got.APIKey != tt.wantKey || got.BaseURL != tt.wantURL {
				t.Errorf("got key %q and address %q, want %q and %q", got.APIKey, got.BaseURL, tt.wantKey, tt.wantURL)
			}
		})
	}
}

func TestNewBuildsEveryBuiltInName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		env      []string
		wantName string
	}{
		{"openai", []string{"OPENAI_API_KEY", "k"}, "openai"},
		{"anthropic", []string{"ANTHROPIC_API_KEY", "k"}, "anthropic"},
		{"openrouter", []string{"OPENROUTER_API_KEY", "k"}, "openrouter"},
		{"cerebras", []string{"CEREBRAS_API_KEY", "k"}, "cerebras"},
		{"deepseek", []string{"DEEPSEEK_API_KEY", "k"}, "deepseek"},
		{"qwen", []string{"QWEN_API_KEY", "k"}, "qwen"},
		{"zai", []string{"ZAI_API_KEY", "k"}, "zai"},
		{"jev", []string{"TYPESAFE_API_KEY", "k"}, "jev"},
		{"jev-openrouter", []string{"OPENROUTER_API_KEY", "k"}, "jev-openrouter"},
		{"jev-litellm", []string{"LITELLM_API_KEY", "k", "LITELLM_BASE_URL", "http://127.0.0.1:4000"}, "jev-litellm"},
		{"  OpenAI ", []string{"OPENAI_API_KEY", "k"}, "openai"},
		{"ANTHROPIC", []string{sharedKeyVariable, "k"}, "anthropic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := newFrom(lookupOf(tt.env...), tt.name, Config{})
			if err != nil {
				t.Fatalf("New(%q): %v", tt.name, err)
			}
			if p.Name() != tt.wantName {
				t.Errorf("Name() = %q, want %q", p.Name(), tt.wantName)
			}
		})
	}
}

func TestNewRefusesWhatNoProviderCanBeBuiltFrom(t *testing.T) {
	t.Parallel()
	withKey := []string{"OPENAI_API_KEY", testKey}
	tests := []struct {
		name    string
		which   string
		env     []string
		cfg     Config
		wantErr string
	}{
		{name: "an empty name", which: "", wantErr: "the provider name is empty"},
		{name: "a name of only space", which: "   ", wantErr: "the provider name is empty"},
		{name: "a name no provider has", which: "no-such-provider", env: withKey, wantErr: `no provider is called "no-such-provider"`},
		{name: "no key anywhere", which: "openai", wantErr: "no API key"},
		{name: "no key for a decision provider", which: "jev", wantErr: "SCHEMAFLUX_TYPESAFE_API_KEY, TYPESAFE_API_KEY"},
		{name: "a negative timeout", which: "openai", env: withKey, cfg: Config{Timeout: -1}, wantErr: "Config.Timeout is negative"},
		{name: "a negative backoff", which: "openai", env: withKey, cfg: Config{RetryBackoff: -1}, wantErr: "Config.RetryBackoff is negative"},
		{name: "a key with a control character in the Config", which: "openai", cfg: Config{APIKey: "k\nbad"}, wantErr: "Config.APIKey holds a control character"},
		{name: "a service tier on a provider that has none", which: "anthropic", env: []string{"ANTHROPIC_API_KEY", testKey},
			cfg: Config{ServiceTier: "priority"}},
		{name: "a service tier OpenAI does not name", which: "openai", env: withKey, cfg: Config{ServiceTier: "gold"}},
		{name: "a service tier behind another address", which: "openai", env: withKey,
			cfg: Config{ServiceTier: "priority", BaseURL: "https://proxy.example/v1"}},
		{name: "an address the endpoint policy refuses", which: "openai", env: withKey,
			cfg: Config{BaseURL: "https://other.example/v1", Endpoint: &EndpointPolicy{Schemes: []string{"https"}, Hosts: []string{"api.openai.com"}}}},
		{name: "an endpoint policy that allows nothing", which: "openrouter", env: []string{"OPENROUTER_API_KEY", testKey},
			cfg: Config{BaseURL: "https://openrouter.example/v1", Endpoint: &EndpointPolicy{}}},
		{name: "a private address the endpoint policy does not allow", which: "openai", env: withKey,
			cfg: Config{BaseURL: "http://127.0.0.1:9/v1", Endpoint: &EndpointPolicy{Schemes: []string{"http"}, Hosts: []string{"127.0.0.1"}}}},
		{name: "a decision provider with no address", which: "jev-litellm", env: []string{"LITELLM_API_KEY", testKey},
			wantErr: "LITELLM_BASE_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := newFrom(lookupOf(tt.env...), tt.which, tt.cfg)
			if err == nil {
				t.Fatalf("New succeeded with %v, want an error", p)
			}
			if p != nil {
				t.Errorf("New returned %v beside its error, want nil", p)
			}
			e := errorOf(t, err)
			if e.Kind != schemaflux.ErrConfiguration || !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Errorf("kind = %v, want configuration", e.Kind)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not say %q", err, tt.wantErr)
			}
			for _, text := range []string{err.Error(), e.Detail()} {
				if strings.Contains(text, testKey) {
					t.Errorf("the error contains the key: %q", text)
				}
			}
		})
	}
}

func TestNewAKeyInTheConfigNeedsNoVariable(t *testing.T) {
	t.Parallel()
	for name := range providerVariables {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{APIKey: testKey}
			if name == "jev-litellm" {
				cfg.BaseURL = "http://127.0.0.1:4000"
			}
			p, err := newFrom(lookupOf(), name, cfg)
			if err != nil {
				t.Fatalf("New(%q) with a key in the Config and an empty environment: %v", name, err)
			}
			if p.Name() != name {
				t.Errorf("Name() = %q, want %q", p.Name(), name)
			}
		})
	}
}

func TestNewSendsTheKeyItResolved(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		env        []string
		cfg        Config
		reply      string
		wantHeader string
		wantValue  string
	}{
		{"openai from its variable", []string{"OPENAI_API_KEY", "env-key"}, Config{}, responsesOK, "Authorization", "Bearer env-key"},
		{"openai from the Config", []string{"OPENAI_API_KEY", "env-key"}, Config{APIKey: "code-key"}, responsesOK, "Authorization", "Bearer code-key"},
		{"openai from the shared variable", []string{sharedKeyVariable, "shared-key"}, Config{}, responsesOK, "Authorization", "Bearer shared-key"},
		{"anthropic from its variable", []string{"ANTHROPIC_API_KEY", "env-key"}, Config{}, anthropicOK, "X-Api-Key", "env-key"},
		{"anthropic from the Config", nil, Config{APIKey: "code-key"}, anthropicOK, "X-Api-Key", "code-key"},
		{"openrouter from its variable", []string{"OPENROUTER_API_KEY", "env-key"}, Config{}, chatOK, "Authorization", "Bearer env-key"},
		{"deepseek from its variable", []string{"DEEPSEEK_API_KEY", "env-key"}, Config{}, chatOK, "Authorization", "Bearer env-key"},
		{"qwen from DashScope's variable", []string{"DASHSCOPE_API_KEY", "env-key"}, Config{}, chatOK, "Authorization", "Bearer env-key"},
		{"zai from GLM's variable", []string{"GLM_API_KEY", "env-key"}, Config{}, chatOK, "Authorization", "Bearer env-key"},
		{"cerebras from the Config", nil, Config{APIKey: "code-key"}, chatOK, "Authorization", "Bearer code-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, tt.reply))
			name := strings.Fields(tt.name)[0]
			// The address comes from the environment too, as it would in a program.
			variables, _ := variablesFor(name)
			env := append(slices.Clone(tt.env), variables.baseURLs[0], srv.URL)
			p, err := newFrom(lookupOf(env...), name, tt.cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			resp, err := ask(p)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.Content != "hello" {
				t.Errorf("Content = %q, want %q", resp.Content, "hello")
			}
			if got := srv.last(t).header.Get(tt.wantHeader); got != tt.wantValue {
				t.Errorf("%s header = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
		})
	}
}

func TestNewReadsTheProcessEnvironmentWhenThereIsNoLookup(t *testing.T) {
	// Not parallel: it sets variables of the process.
	t.Setenv("SCHEMAFLUX_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", testKey)
	p, err := New("openai", Config{})
	if err != nil {
		t.Fatalf("New with OPENAI_API_KEY set: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("Name() = %q, want openai", p.Name())
	}

	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv(sharedKeyVariable, "")
	if _, err := New("openai", Config{}); !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("New with no key anywhere returned %v, want a configuration error", err)
	}
}

func TestNamesListsEveryBuiltInProvider(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "jev", "jev-openrouter", "jev-litellm"} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()
			if !slices.Contains(Names(), want) {
				t.Errorf("Names() = %v, which lacks %q", Names(), want)
			}
		})
	}
	t.Run("the list is sorted", func(t *testing.T) {
		t.Parallel()
		if names := Names(); !slices.IsSorted(names) {
			t.Errorf("Names() is not sorted: %v", names)
		}
	})
	t.Run("a name that was registered is listed", func(t *testing.T) {
		t.Parallel()
		name := nameFor(t)
		if err := Register(name, answering(nil)); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(Names(), name) {
			t.Errorf("Names() lacks %q", name)
		}
	})
	t.Run("a name is listed once", func(t *testing.T) {
		t.Parallel()
		names := Names()
		if len(slices.Compact(slices.Clone(names))) != len(names) {
			t.Errorf("Names() holds a name twice: %v", names)
		}
	})
	t.Run("every built-in name is one New builds or refuses with a configuration error", func(t *testing.T) {
		t.Parallel()
		for _, name := range Names() {
			if strings.HasPrefix(name, "test-") {
				continue // registered by another test, with a factory of its own
			}
			if _, err := newFrom(lookupOf(), name, Config{}); err != nil && !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Errorf("New(%q) = %v, want a provider or a configuration error", name, err)
			}
		}
	})
	t.Run("the slice is the caller's", func(t *testing.T) {
		t.Parallel()
		names := Names()
		names[0] = "changed"
		if Names()[0] == "changed" {
			t.Error("Names() returned its own slice, and changing it changed the next call's")
		}
	})
}
