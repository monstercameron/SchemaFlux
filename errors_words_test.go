package schemaflux

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// version1Names are names of the old root's settings and variables that no error
// of version 2 may tell its caller to use.
var version1Names = []string{"Client.WithModel", "Client.Context", "WithHandler", "WithProviderInstance", "SetDefaultProvider",
	"MaxTokens,", "raise MaxTokens", "set SCHEMAFLUX_MODEL", "Providers with a built-in mapping", "groq", "mistral"}

func TestAnErrorOfTheInternalPackagesSpeaksVersion2(t *testing.T) {
	t.Parallel()
	missing, _ := config.ValidateModelResolution("no-such-provider"), 0
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no default model at call time", config.NoDefaultModelError("acme"),
			`no default model for provider "acme"; name one with Client.Model, or with Model in the call's options`},
		{"no model for a tier at construction", missing,
			`name one with Client.Model, or with Model in the call's options`},
		{"a truncated answer", &types.OperationError{Kind: types.KindOutputTruncated, Op: "llm.complete",
			Message: "the answer was cut off at the token ceiling before any text was produced; raise MaxTokens, or lower the reasoning effort"},
			"raise MaxOutputTokens, or lower the reasoning effort"},
		{"a result that does not fit", &types.OperationError{Kind: types.KindContextTooLarge, Op: "x",
			Message: "the result needs roughly 90 tokens. Reduce the batch, raise the tier, or set SCHEMAFLUX_MODEL to a model with a larger output budget"},
			"or set Model to a model with a larger output budget"},
		{"a message with nothing of the old root in it", &types.OperationError{Kind: types.KindInvalidRequest, Op: "x", Message: "the input is empty"},
			"the input is empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := asError(t, "extract", c.err)
			if !strings.Contains(e.Message, c.want) {
				t.Errorf("the message is %q, want it to say %q", e.Message, c.want)
			}
			for _, name := range version1Names {
				if strings.Contains(e.Error(), name) {
					t.Errorf("the error %q names %q, which version 2 does not have", e.Error(), name)
				}
			}
		})
	}
}

// bareProvider is a provider written outside the library with no model for any
// tier: the first error its author meets.
type bareProvider struct{}

func (bareProvider) Name() string { return "bare" }
func (bareProvider) Complete(context.Context, Request) (Response, error) {
	return Response{Content: `{"number":"A","total":1}`}, nil
}

func TestAProviderWithNoModelIsToldHowVersion2NamesOne(t *testing.T) {
	t.Parallel()
	_, err := Extract[limitsInvoice](WithClient(context.Background(), Client{Provider: bareProvider{}}), "invoice")
	e := asError(t, "extract", err)
	if e.Kind != ErrConfiguration {
		t.Fatalf("kind = %v, want configuration (%v)", e.Kind, err)
	}
	for _, want := range []string{`no default model for provider "bare"`, "Client.Model", "the call's options"} {
		if !strings.Contains(e.Error(), want) {
			t.Errorf("the error %q does not say %q", e.Error(), want)
		}
	}
	for _, name := range version1Names {
		if strings.Contains(e.Error(), name) {
			t.Errorf("the error %q names %q, which version 2 does not have", e.Error(), name)
		}
	}
}
