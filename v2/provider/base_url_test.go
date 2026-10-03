package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
)

// baseURLCases are base URLs and what a provider built for each does. A URL a
// request can be sent to is accepted; every other is a configuration error that
// names the field and does not print the URL, which can hold a key.
var baseURLCases = []struct {
	url     string
	problem string // what the error says, or "" when the URL is fine
}{
	{"https://proxy.example/v1", ""},
	{"http://localhost:8000/v1", ""},
	{"HTTP://LOCALHOST:8000", ""},
	{"https://proxy.example", ""},
	{"https://proxy.example/v1/", ""},
	{"http://[::1]:8080/v1", ""},
	{"http://127.0.0.1:1234", ""},
	{"not a url", "absolute http or https"},
	{"proxy.example/v1", "absolute http or https"},
	{"//proxy.example/v1", "absolute http or https"},
	{"ftp://x/y", "absolute http or https"},
	{"file:///etc/passwd", "absolute http or https"},
	{"javascript:alert(1)", "absolute http or https"},
	{"http://", "no host"},
	{"https://:8080/v1", "no host"},
	{"https://user:SECRET-PASSWORD@proxy.example/v1", "credentials"},
	{"https://SECRET-TOKEN@proxy.example/v1", "credentials"},
	{"https://proxy.example/v1?key=SECRET-QUERY", "query"},
	{"https://proxy.example/v1?", "query"},
	{"https://proxy.example/v1#SECRET-FRAGMENT", "fragment"},
	{"https://proxy.example/v1#", "fragment"},
	{"http://exa mple.test/", "not a URL"},
	{"https://proxy.example/%zz", "not a URL"},
}

func TestABaseURLNoRequestCanUseIsAConfigurationErrorThatNamesTheField(t *testing.T) {
	t.Parallel()
	for _, tc := range baseURLCases {
		t.Run(tc.url, func(t *testing.T) {
			t.Parallel()
			byConfig, errConfig := newFrom(lookupOf(), "openai", Config{APIKey: testKey, BaseURL: tc.url})
			byArgument, errArgument := OpenAICompatible("acme", tc.url, Config{APIKey: testKey})
			if tc.problem == "" {
				if errConfig != nil || errArgument != nil || byConfig == nil || byArgument == nil {
					t.Fatalf("a URL a request can be sent to was refused: %v, %v", errConfig, errArgument)
				}
				return
			}
			for how, err := range map[string]error{"Config.BaseURL": errConfig, "the baseURL argument": errArgument} {
				e := errorOf(t, err)
				if e.Kind != schemaflux.ErrConfiguration {
					t.Errorf("%s: kind = %v, want configuration", how, e.Kind)
				}
				if !strings.Contains(e.Message, tc.problem) {
					t.Errorf("%s: the message %q does not say %q", how, e.Message, tc.problem)
				}
				field := "Config.BaseURL"
				if how != "Config.BaseURL" {
					field = "baseURL"
				}
				if !strings.Contains(e.Message, field) {
					t.Errorf("%s: the message %q does not name %s", how, e.Message, field)
				}
				if strings.Contains(err.Error(), "SECRET") {
					t.Errorf("%s: the error prints what the URL held: %v", how, err)
				}
				if e.Retryable() || errors.Is(err, schemaflux.ErrUnavailable) {
					t.Errorf("%s: a URL no request can use was reported as a failure the library retries", how)
				}
			}
			if byConfig != nil || byArgument != nil {
				t.Errorf("a provider was returned beside the error")
			}
		})
	}
}

func TestABaseURLIsCheckedForARegisteredFactoryToo(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	called := false
	if err := Register(name, func(Config) (schemaflux.Provider, error) { called = true; return &scripted{name: name}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := newFrom(lookupOf(), name, Config{BaseURL: "ftp://x"}); !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("got %v, want a configuration error", err)
	}
	if called {
		t.Error("the factory was called with a base URL no request can use")
	}
}

func TestTheProvidersThatAnswerWithAPlaceholderAreNotOffered(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"local", "mock", " Local ", "MOCK"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, err := newFrom(lookupOf(), name, Config{})
			e := errorOf(t, err)
			if p != nil || e.Kind != schemaflux.ErrConfiguration || e.Op != opNew {
				t.Fatalf("New = %v, %+v, want a configuration error from %s", p, e, opNew)
			}
			if !strings.Contains(e.Message, "schemafluxtest") {
				t.Errorf("the message %q does not name schemafluxtest", e.Message)
			}
			if !bind.PlaceholderProvider(name) {
				t.Errorf("bind.PlaceholderProvider(%q) is false", name)
			}
		})
	}
	for _, name := range Names() {
		if bind.PlaceholderProvider(name) {
			t.Errorf("Names lists %q, which is not offered", name)
		}
	}
	// A name that only contains the word is a provider like any other.
	if bind.PlaceholderProvider("localhost-proxy") || bind.PlaceholderProvider("") {
		t.Error("a name that is not one of the two was refused")
	}
}

func TestAKeylessProviderIsDeclaredAndAProviderThatNeedsAKeyIsNot(t *testing.T) {
	t.Parallel()
	keyless, needsKey := nameFor(t)+"-keyless", nameFor(t)+"-key"
	factory := func(Config) (schemaflux.Provider, error) { return &scripted{name: "built"}, nil }
	if err := RegisterKeyless(keyless, factory); err != nil {
		t.Fatal(err)
	}
	if err := Register(needsKey, factory); err != nil {
		t.Fatal(err)
	}
	if !bind.Keyless(keyless) || !bind.Keyless(" "+strings.ToUpper(keyless)+" ") {
		t.Error("a provider registered with RegisterKeyless is not declared keyless")
	}
	if bind.Keyless(needsKey) || bind.Keyless("openai") || bind.Keyless("") {
		t.Error("a provider that needs a key was declared keyless")
	}
	for _, name := range []string{keyless, needsKey} {
		if _, err := newFrom(lookupOf(), name, Config{}); err != nil {
			t.Errorf("New(%q): %v", name, err)
		}
	}
	if err := RegisterKeyless(keyless, factory); !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("registering a name twice: %v", err)
	}
	if err := RegisterKeyless("", factory); !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("an empty name: %v", err)
	}
	if err := RegisterKeyless(fmt.Sprint(keyless, "-nil"), nil); !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("a nil factory: %v", err)
	}
	if bind.Keyless(fmt.Sprint(keyless, "-nil")) {
		t.Error("a registration that failed declared its name keyless")
	}
}

func TestAFactoryThatReturnsATypedNilIsRefused(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	if err := Register(name, func(Config) (schemaflux.Provider, error) {
		var none *scripted
		return none, nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := newFrom(lookupOf(), name, Config{})
	if p != nil || !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Errorf("New = %v, %v, want a configuration error", p, err)
	}
}
