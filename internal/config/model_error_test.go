package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A provider the library has no model table for, injected by a caller under a
// name of their own, failed every call with a message that spoke only of
// environment variables. A caller using a client had no way to learn from the
// message that Client.WithModel exists, and the workaround that spread was to
// name the provider "local".

func TestNoDefaultModelErrorSaysWhatToDo(t *testing.T) {
	providers := []string{
		"mine",
		"Mine",
		"qwen",
		"zai",
		"my-internal-gateway",
		"a_b.c",
		"模型",
		"  padded  ",
		`has"quote`,
		"",
	}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			err := NoDefaultModelError(provider)
			if err == nil {
				t.Fatal("no error")
			}
			text := err.Error()

			// The phrase the error classifier and the tests match on.
			if !strings.Contains(text, "no default model for provider") {
				t.Errorf("message does not contain the matched phrase: %q", text)
			}
			if want := fmt.Sprintf("provider %q;", provider); !strings.Contains(text, want) {
				t.Errorf("message does not name the provider as %s: %q", want, text)
			}
			for _, way := range []string{"Client.WithModel", "Client.Context", "Model on the call", "SCHEMAFLUX_MODEL", "_FAST"} {
				if !strings.Contains(text, way) {
					t.Errorf("message does not mention %q: %q", way, text)
				}
			}
			for _, known := range KnownProviders() {
				if !strings.Contains(text, known) {
					t.Errorf("message does not list the built-in provider %q: %q", known, text)
				}
			}
			if !errors.Is(err, ErrNoModelMapping) {
				t.Error("errors.Is(err, ErrNoModelMapping) = false")
			}
			if !errors.Is(err, types.ErrConfiguration) || types.KindOf(err) != types.KindConfiguration {
				t.Errorf("the error is not classified as configuration: kind %v", types.KindOf(err))
			}
			if errors.Is(err, errors.New("another error")) {
				t.Error("the error matches an unrelated one")
			}
		})
	}
}

func TestNoDefaultModelErrorNamesTheProviderQuoted(t *testing.T) {
	text := NoDefaultModelError("mine").Error()
	if !strings.Contains(text, `provider "mine"`) {
		t.Errorf("message = %q, want the provider named in quotes", text)
	}
}

// The construction-time error gives the same ways out, so the two messages for
// one problem cannot send a caller to different places.
func TestValidateModelResolutionGivesTheSameWaysOut(t *testing.T) {
	clearModelEnv(t)

	err := ValidateModelResolution("mine")
	if err == nil {
		t.Fatal("an unmapped provider validated")
	}
	if !strings.Contains(err.Error(), noModelAdvice) {
		t.Errorf("the construction error does not carry the advice: %v", err)
	}
}
