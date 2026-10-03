package testfixtures

import (
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/ops"
)

type fixtureTarget struct {
	Name string `json:"name"`
}

// The model a fixture's response reports is the library's default, so a fixture
// never names a model the library no longer targets.
func TestResponsesMessageReportsTheLibraryDefaultModel(t *testing.T) {
	body := ResponsesMessage("an answer")
	if want := `"model":"` + config.ModelDefaultFast + `"`; !strings.Contains(strings.ReplaceAll(body, " ", ""), want) {
		t.Errorf("the body does not report model %q: %s", config.ModelDefaultFast, body)
	}
}

func extractWithTheDefault() (fixtureTarget, error) {
	result, err := ops.ExtractResult[fixtureTarget]("input", ops.NewExtractOptions())
	return result.Value, err
}

// A scripted provider is the process default for the test that installs it and
// no longer. It used to stay, so the next test found it.
func TestAScriptedProviderIsRemovedWhenItsTestEnds(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fixture-model")
	schemaflux.SetDefaultClient(nil)
	t.Cleanup(func() { schemaflux.SetDefaultClient(nil) })

	t.Run("installs one", func(t *testing.T) {
		provider := WithScriptedProvider(t, `{"name":"scripted"}`, nil)
		got, err := extractWithTheDefault()
		if err != nil || got.Name != "scripted" {
			t.Fatalf("with the provider installed: %+v, %v", got, err)
		}
		if len(provider.Requests()) != 1 {
			t.Errorf("the provider saw %d requests, want 1", len(provider.Requests()))
		}
	})

	if _, err := extractWithTheDefault(); !errors.Is(err, ops.ErrNoProvider) {
		t.Errorf("after the test that installed it ended, an extraction gave %v, want ErrNoProvider", err)
	}
}

func TestEveryInstallingHelperPutsTheDefaultBack(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fixture-model")
	outer := schemaflux.NewClient("").WithProviderInstance(NewScripted(`{"name":"outer"}`))
	schemaflux.SetDefaultClient(outer)
	t.Cleanup(func() { schemaflux.SetDefaultClient(nil) })

	helpers := map[string]func(t *testing.T){
		"WithScriptedProvider":        func(t *testing.T) { WithScriptedProvider(t, `{"name":"inner"}`, nil) },
		"WithScriptedProviderReplies": func(t *testing.T) { WithScriptedProviderReplies(t, `{"name":"inner"}`, `{"name":"inner"}`) },
		"a failing provider":          func(t *testing.T) { WithScriptedProvider(t, "", errors.New("down")) },
	}
	for name, helper := range helpers {
		t.Run(name, func(t *testing.T) {
			t.Run("inside", func(t *testing.T) {
				helper(t)
				got, err := extractWithTheDefault()
				if name != "a failing provider" && (err != nil || got.Name != "inner") {
					t.Fatalf("inside the test: %+v, %v", got, err)
				}
				if name == "a failing provider" && err == nil {
					t.Fatal("the failing provider answered")
				}
			})

			got, err := extractWithTheDefault()
			if err != nil || got.Name != "outer" {
				t.Errorf("after the test ended the default is %+v, %v; want the outer provider back", got, err)
			}
		})
	}
}
