package schemaflux_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The canaries are strings that no log line of the library may hold: the
// caller's input, a model's answer, a provider's message and a provider's code
// are all somebody's data, and every caller logs what the library logs.
const (
	canaryInput  = "CANARY_INPUT_7f3a"
	canaryAnswer = "CANARY_ANSWER_7f3a"
	canaryVendor = "CANARY_VENDOR_7f3a"
	canaryCode   = "CANARY_CODE_7f3a"
	canaryKey    = "sk-CANARY_KEY_7f3aabcdefghijklmnop"
)

var canaries = []string{canaryInput, canaryAnswer, canaryVendor, canaryCode, "CANARY_KEY"}

// logBoth is a logger that writes every level, in both of the standard
// handlers, so that a leak in a text line and a leak in a JSON line are both
// seen. What it wrote is read with text.
type logBoth struct {
	text, json bytes.Buffer
}

func (l *logBoth) logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	return slog.New(teeHandler{slog.NewTextHandler(&l.text, opts), slog.NewJSONHandler(&l.json, opts)})
}

func (l *logBoth) all() string { return l.text.String() + l.json.String() }

type teeHandler struct{ a, b slog.Handler }

func (t teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.a.Enabled(ctx, level)
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	if err := t.a.Handle(ctx, r.Clone()); err != nil {
		return err
	}
	return t.b.Handle(ctx, r.Clone())
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{t.a.WithAttrs(attrs), t.b.WithAttrs(attrs)}
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{t.a.WithGroup(name), t.b.WithGroup(name)}
}

// leakedIn returns the canaries that text holds.
func leakedIn(text string) []string {
	var held []string
	for _, canary := range canaries {
		if strings.Contains(text, canary) {
			held = append(held, canary)
		}
	}
	return held
}

// quotingProvider is a provider written outside the library whose failures
// quote what it was sent, as a real vendor's do.
type quotingProvider struct {
	name string
	fail func(req schemaflux.Request) error
}

func (p quotingProvider) Name() string {
	if p.name == "" {
		return "quoting"
	}
	return p.name
}

func (p quotingProvider) ModelFor(schemaflux.Tier) string { return "quoting-model" }

func (p quotingProvider) Complete(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	if p.fail != nil {
		if err := p.fail(req); err != nil {
			return schemaflux.Response{}, err
		}
	}
	return schemaflux.Response{Content: `{"number":"` + canaryAnswer + `","total":"` + canaryAnswer + `"}`, FinishReason: "stop"}, nil
}

// quoteOfInput is the text a vendor's error holds: its own words and the input.
func quoteOfInput(schemaflux.Request) string {
	return canaryVendor + " says " + canaryInput + " is bad"
}

type leakInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

// leakCalls are the operations a failing call is made through. Each is given
// the canary as its input.
var leakCalls = map[string]func(ctx context.Context) error{
	"Extract": func(ctx context.Context) error {
		_, err := schemaflux.Extract[leakInvoice](ctx, "invoice "+canaryInput,
			schemaflux.ExtractOptions{Options: schemaflux.Options{Steering: "steer " + canaryInput}})
		return err
	},
	"Is": func(ctx context.Context) error {
		_, err := schemaflux.Is(ctx, canaryInput, "is it a canary?")
		return err
	},
	"Classify": func(ctx context.Context) error {
		_, err := schemaflux.Classify(ctx, canaryInput, []string{"a", "b"})
		return err
	},
	"Summarize": func(ctx context.Context) error {
		_, err := schemaflux.Summarize(ctx, "text "+canaryInput+" long enough to summarise")
		return err
	},
}

func TestNoLogLineHoldsTheTextOfAnError(t *testing.T) {
	failures := map[string]func(req schemaflux.Request) error{
		"a plain error that quotes the input": func(req schemaflux.Request) error {
			return errors.New(quoteOfInput(req))
		},
		"an Error of a custom provider with a free-text code, message and cause": func(req schemaflux.Request) error {
			return &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, Code: "free text " + canaryCode,
				Message: quoteOfInput(req), Err: errors.New("body " + canaryVendor)}
		},
		"an Error with a fixed-word code": func(req schemaflux.Request) error {
			return &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Status: 503, Code: canaryCode, Err: errors.New(quoteOfInput(req))}
		},
		"an error that carries a wait": func(req schemaflux.Request) error {
			return &schemaflux.Error{Kind: schemaflux.ErrRateLimited, RetryAfter: time.Millisecond, Err: errors.New(quoteOfInput(req))}
		},
		"an unauthorized kind": func(req schemaflux.Request) error {
			return &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401, Err: errors.New(quoteOfInput(req))}
		},
		"a refusal": func(req schemaflux.Request) error {
			return &schemaflux.Error{Kind: schemaflux.ErrRefused, Err: errors.New(quoteOfInput(req))}
		},
	}
	for opName, call := range leakCalls {
		for failName, fail := range failures {
			for _, retries := range []int{-1, 2} {
				t.Run(fmt.Sprintf("%s/%s/retries %d", opName, failName, retries), func(t *testing.T) {
					t.Parallel()
					var logs logBoth
					ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{
						Provider: quotingProvider{fail: fail},
						Logger:   logs.logger(),
						Retries:  retries,
					})
					err := call(ctx)
					if err == nil {
						t.Fatal("the call succeeded")
					}
					if held := leakedIn(logs.all()); len(held) > 0 {
						t.Errorf("the log holds %v:\n%s", held, logs.text.String())
					}
					if logs.text.Len() == 0 {
						t.Error("the failing call logged nothing: the test reads no lines")
					}
				})
			}
		}
	}
}

func TestNoLogLineOfASucceedingCallHoldsTheCallersData(t *testing.T) {
	t.Parallel()
	for opName, call := range leakCalls {
		t.Run(opName, func(t *testing.T) {
			t.Parallel()
			var logs logBoth
			model := answeringWith(`{"number":"A","total":1}`, "a summary", `true`)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model, Logger: logs.logger()})
			_ = call(ctx)
			if held := leakedIn(logs.all()); len(held) > 0 {
				t.Errorf("the log holds %v:\n%s", held, logs.text.String())
			}
		})
	}
}

// TestNoLogLineHoldsTheTextOfABuiltInProvidersError reaches the real HTTP path
// of the built-in providers against a loopback server that answers every request
// with a message and a free-text code that quote what was sent, and with the
// variable that asks an error for its detail set: the variable changes what
// Error text carries, and no log line may follow it.
func TestNoLogLineHoldsTheTextOfABuiltInProvidersError(t *testing.T) {
	for _, detail := range []string{"", "1"} {
		for _, status := range []int{400, 401, 429, 500} {
			t.Run(fmt.Sprintf("status %d detail %q", status, detail), func(t *testing.T) {
				t.Setenv("SCHEMAFLUX_ERROR_DETAIL", detail)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					quoted := strings.Contains(string(body), canaryInput)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":{"message":"%s says %s (quoted=%v) key %s","type":"invalid_request_error","code":"free text %s"}}`,
						canaryVendor, canaryInput, quoted, r.Header.Get("Authorization"), canaryCode)
				}))
				defer srv.Close()
				built, err := provider.OpenAICompatible("acme", srv.URL, provider.Config{APIKey: canaryKey, Retries: -1})
				if err != nil {
					t.Fatal(err)
				}
				var logs logBoth
				ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: built, Model: "m", Logger: logs.logger(), Retries: -1})
				if err := leakCalls["Extract"](ctx); err == nil {
					t.Fatal("the call succeeded")
				}
				if held := leakedIn(logs.all()); len(held) > 0 {
					t.Errorf("the log holds %v:\n%s", held, logs.text.String())
				}
			})
		}
	}
}

// TestALogLineCarriesTheKindOfAFailureAndNoMore reads what a failed call does
// log: the operation, the kind, the route and the attempt, and no free text.
func TestALogLineCarriesTheKindOfAFailureAndNoMore(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	failing := quotingProvider{fail: func(req schemaflux.Request) error {
		return &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401, Err: errors.New(quoteOfInput(req))}
	}}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: failing, Logger: logs.Logger(), Retries: -1})
	if err := leakCalls["Extract"](ctx); err == nil {
		t.Fatal("the call succeeded")
	}
	var failed []slog.Record
	for _, entry := range logs.Entries() {
		if entry.Level == slog.LevelError {
			failed = append(failed, entry)
		}
	}
	if len(failed) == 0 {
		t.Fatal("a failed call wrote no line at error level")
	}
	for _, entry := range failed {
		entry.Attrs(func(a slog.Attr) bool {
			if a.Key == "kind" {
				if got := a.Value.String(); got != schemaflux.ErrUnauthorized.Error() {
					t.Errorf("line %q: kind = %q, want %q", entry.Message, got, schemaflux.ErrUnauthorized.Error())
				}
			}
			if a.Key == "error" {
				t.Errorf("line %q still carries an attribute named error: %v", entry.Message, a.Value)
			}
			return true
		})
	}
}

// TestLogLinesBoundTheNamesOfTheRoute holds the provider's and the model's names
// to what telemetry holds them to: printable ASCII, and not longer than the
// limit, so that a name a caller wrote cannot add a line to a log or fill one.
func TestLogLinesBoundTheNamesOfTheRoute(t *testing.T) {
	t.Parallel()
	var logs logBoth
	hostile := quotingProvider{
		name: "evil\nlevel=ERROR msg=forged " + strings.Repeat("p", 200),
		fail: func(schemaflux.Request) error { return errors.New("down") },
	}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{
		Provider: hostile,
		Model:    "model\nlevel=ERROR msg=forged " + strings.Repeat("m", 300),
		Logger:   logs.logger(),
		Retries:  -1,
	})
	if err := leakCalls["Extract"](ctx); err == nil {
		t.Fatal("the call succeeded")
	}
	for _, line := range strings.Split(logs.text.String(), "\n") {
		if strings.HasPrefix(line, "level=ERROR msg=forged") {
			t.Errorf("a name forged a log line: %.120q", line)
		}
	}
	if strings.Contains(logs.text.String(), strings.Repeat("p", 100)) || strings.Contains(logs.text.String(), strings.Repeat("m", 200)) {
		t.Errorf("a name was written whole: %.300q", logs.text.String())
	}
}
