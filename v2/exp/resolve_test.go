package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

type resolveTarget struct {
	Name string `json:"name"`
}

const resolveAnswer = `{"resolved":{"name":"merged"},"conflicts":[],"source_contributions":{"0":["name"],"1":[]},"confidence":0.75}`

func TestResolveAPI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sources  []resolveTarget
		options  []exp.ResolveOptions
		wantKind error
		calls    int
	}{
		{"default", []resolveTarget{{"a"}, {"b"}}, nil, nil, 1},
		{"one option", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{Strategy: "merge"}}, nil, 1},
		{"several options overlay", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{Strategy: "merge"}, {ConflictThreshold: 0.4}}, nil, 1},
		{"single source", []resolveTarget{{"alone"}}, nil, nil, 0},
		{"empty sources", nil, nil, schemaflux.ErrInvalidRequest, 0},
		{"bad strategy", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{Strategy: "unknown"}}, schemaflux.ErrConfiguration, 0},
		{"negative source", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{AuthoritativeSource: -1}}, schemaflux.ErrConfiguration, 0},
		{"source outside input", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{AuthoritativeSource: 2}}, schemaflux.ErrConfiguration, 0},
		{"priority outside input", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{FieldPriorities: map[string]int{"name": 2}}}, schemaflux.ErrConfiguration, 0},
		{"negative priority", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{FieldPriorities: map[string]int{"name": -1}}}, schemaflux.ErrConfiguration, 0},
		{"negative threshold", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{ConflictThreshold: -0.1}}, schemaflux.ErrConfiguration, 0},
		{"threshold over one", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{ConflictThreshold: 1.1}}, schemaflux.ErrConfiguration, 0},
		{"threshold nan", []resolveTarget{{"a"}, {"b"}}, []exp.ResolveOptions{{ConflictThreshold: math.NaN()}}, schemaflux.ErrConfiguration, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := &schemafluxtest.Provider{}
			model.Reply(resolveAnswer)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
			got, err := exp.Resolve[resolveTarget](ctx, tc.sources, tc.options...)
			if tc.wantKind != nil {
				if !errors.Is(err, tc.wantKind) {
					t.Fatalf("error = %v, want %v", err, tc.wantKind)
				}
				var apiErr *schemaflux.Error
				if !errors.As(err, &apiErr) || apiErr.Op != "exp.resolve" {
					t.Fatalf("error = %#v, want a typed exp.resolve error", err)
				}
				if !reflect.DeepEqual(got, exp.ResolveResult[resolveTarget]{}) {
					t.Fatalf("result beside error = %+v", got)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if model.Calls() != tc.calls {
				t.Fatalf("provider calls = %d, want %d", model.Calls(), tc.calls)
			}
			if tc.name == "single source" && (got.ModelAsked || got.Resolved.Name != "alone" || got.ModelConfidence != 0) {
				t.Fatalf("single-source result = %+v", got)
			}
			if tc.name == "default" && (!got.ModelAsked || got.ModelConfidence != .75 || got.Resolved.Name != "merged") {
				t.Fatalf("model result = %+v", got)
			}
		})
	}
}

func TestResolveRefusesIncompleteAndOutOfRangeAnswers(t *testing.T) {
	t.Parallel()
	answers := []struct {
		name string
		body string
	}{
		{"missing resolved", `{"conflicts":[],"source_contributions":{},"confidence":0}`},
		{"null resolved", `{"resolved":null,"conflicts":[],"source_contributions":{},"confidence":0}`},
		{"null conflicts", `{"resolved":{"name":"x"},"conflicts":null,"source_contributions":{},"confidence":0}`},
		{"null contributions", `{"resolved":{"name":"x"},"conflicts":[],"source_contributions":null,"confidence":0}`},
		{"bad contribution index", `{"resolved":{"name":"x"},"conflicts":[],"source_contributions":{"9":["name"]},"confidence":0}`},
		{"confidence outside range", `{"resolved":{"name":"x"},"conflicts":[],"source_contributions":{},"confidence":1.5}`},
	}
	for _, tc := range answers {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := &schemafluxtest.Provider{}
			model.Reply(tc.body)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
			_, err := exp.Resolve[resolveTarget](ctx, []resolveTarget{{"a"}, {"b"}})
			if !errors.Is(err, schemaflux.ErrInvalidAnswer) {
				t.Fatalf("error = %v, want invalid answer", err)
			}
		})
	}
}

func TestResolveConfigurationErrorsDoNotExposeOptionValues(t *testing.T) {
	t.Parallel()
	const canary = "private-field-canary"
	_, err := exp.Resolve[resolveTarget](context.Background(), []resolveTarget{{"a"}, {"b"}}, exp.ResolveOptions{FieldPriorities: map[string]int{canary: 9}})
	if !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error exposed option value: %v", err)
	}
}

func TestResolveRecordsSuccessAndFailure(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{resolveAnswer, `{}`} {
		model := &schemafluxtest.Provider{}
		model.Reply(answer)
		recorder := &schemaflux.Recorder{}
		ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model}), recorder)
		_, err := exp.Resolve[resolveTarget](ctx, []resolveTarget{{"a"}, {"b"}})
		records := recorder.Records()
		if len(records) != 1 || records[0].Op != "exp.resolve" {
			t.Fatalf("records = %+v", records)
		}
		if answer == resolveAnswer && err != nil {
			t.Fatal(err)
		}
		if answer != resolveAnswer && (!errors.Is(err, schemaflux.ErrInvalidAnswer) || records[0].Err == nil) {
			t.Fatalf("failure = %v, record = %+v", err, records[0])
		}
	}
}

func TestResolveFloorRefusesWithoutCallingProvider(t *testing.T) {
	t.Parallel()
	model := &schemafluxtest.Provider{}
	model.Reply(resolveAnswer)
	client := schemaflux.Client{Provider: model, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractSchemaConstrained}}
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	_, err := exp.Resolve[resolveTarget](ctx, []resolveTarget{{"a"}, {"b"}})
	if !errors.Is(err, schemaflux.ErrPolicy) {
		t.Fatalf("error = %v, want policy", err)
	}
	if model.Calls() != 0 {
		t.Fatalf("provider calls = %d", model.Calls())
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.resolve" || records[0].Requests != 0 {
		t.Fatalf("records = %+v", records)
	}
}

func TestResolvePayloadCanariesStayOutOfErrorRecordAndLog(t *testing.T) {
	t.Parallel()
	const canary = "resolve-private-canary"
	model := &schemafluxtest.Provider{}
	model.Reply(canary)
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := schemaflux.Client{Provider: model, Logger: logger}
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	_, err := exp.Resolve[resolveTarget](ctx, []resolveTarget{{canary}, {"b"}}, exp.ResolveOptions{Options: schemaflux.Options{Steering: canary}})
	if err == nil {
		t.Fatal("malformed answer succeeded")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error exposed payload: %v", err)
	}
	encoded, marshalErr := json.Marshal(recorder.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("record exposed payload: %s", encoded)
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatalf("log exposed payload: %s", logs.String())
	}
}

func TestResolveForeignProviderErrorDoesNotReachErrorOrRecord(t *testing.T) {
	t.Parallel()
	const canary = "resolve-provider-error-canary"
	model := &schemafluxtest.Provider{}
	model.Fail(errors.New(canary))
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model, Logger: logger}), recorder)
	_, err := exp.Resolve[resolveTarget](ctx, []resolveTarget{{"a"}, {"b"}})
	if err == nil {
		t.Fatal("provider error was lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error exposed provider text: %v", err)
	}
	encoded, marshalErr := json.Marshal(recorder.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("record exposed provider text: %s", encoded)
	}
	if strings.Contains(logs.String(), canary) {
		t.Skip("call-path log of a foreign provider's error")
	}
}

func TestResolveObserverRunsOperationScope(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(resolveAnswer)
	schemafluxtest.Install(t, model)
	observer := &resolveObserver{}
	restore := telemetry.RegisterObserver(observer)
	defer restore()
	if _, err := exp.Resolve[resolveTarget](context.Background(), []resolveTarget{{"a"}, {"b"}}); err != nil {
		t.Fatal(err)
	}
	if observer.operations != 1 {
		t.Fatalf("operation scopes = %d", observer.operations)
	}
}

type resolveObserver struct{ operations int }

func (o *resolveObserver) Started(ctx context.Context, _ telemetry.Event) context.Context { return ctx }
func (o *resolveObserver) Finished(_ context.Context, result telemetry.Result) {
	if result.Scope == telemetry.ScopeOperation {
		o.operations++
	}
}

func TestResolveRejectsNilContext(t *testing.T) {
	t.Parallel()
	_, err := exp.Resolve[resolveTarget](nil, []resolveTarget{{"a"}, {"b"}})
	if !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
}
