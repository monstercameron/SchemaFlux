package schemaflux_test

import (
	"context"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// localCalls are calls whose work Go can do without a request, in the forms
// that do: a context that has ended must still refuse them, as it refuses a
// call that asks a model.
func localCalls() map[string]func(ctx context.Context) error {
	type signup struct {
		Email string `json:"email"`
		Age   int    `json:"age"`
	}
	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	return map[string]func(ctx context.Context) error{
		"Diff": func(ctx context.Context) error {
			_, err := schemaflux.Diff(ctx, signup{Email: "a", Age: 1}, signup{Email: "b", Age: 2})
			return err
		},
		"Parse of JSON the Go parsers read": func(ctx context.Context) error {
			_, err := schemaflux.Parse[person](ctx, `{"name":"Ada","age":36}`)
			return err
		},
		"Parse of CSV the Go parsers read": func(ctx context.Context) error {
			_, err := schemaflux.Parse[[]person](ctx, "name,age\nAda,36")
			return err
		},
		"Validate with rules Go decides": func(ctx context.Context) error {
			_, err := schemaflux.Validate(ctx, signup{Email: "ada", Age: 17}, schemaflux.ValidateOptions{
				FieldRules: map[string]string{"Email": "email", "Age": "min:18"},
			})
			return err
		},
	}
}

func TestACallWhoseWorkIsLocalStillRefusesAContextThatHasEnded(t *testing.T) {
	t.Parallel()
	ends := []struct {
		name string
		end  func(t *testing.T, ctx context.Context) context.Context
		kind schemaflux.Kind
	}{
		{"cancelled", func(_ *testing.T, ctx context.Context) context.Context {
			ctx, stop := context.WithCancel(ctx)
			stop()
			return ctx
		}, schemaflux.ErrCanceled},
		{"deadline passed", func(t *testing.T, ctx context.Context) context.Context {
			ctx, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			t.Cleanup(stop)
			return ctx
		}, schemaflux.ErrTimeout},
	}
	for name, call := range localCalls() {
		for _, end := range ends {
			t.Run(name+"/"+end.name, func(t *testing.T) {
				t.Parallel()
				model := answeringWith("{}")
				var recorder schemaflux.Recorder
				err := call(end.end(t, operationClient(model, recordingTo(&recorder))))
				var e *schemaflux.Error
				if !errors.As(err, &e) || e.Kind != end.kind || !errors.Is(err, end.kind) {
					t.Fatalf("error = %v, want one of kind %v", err, end.kind)
				}
				if e.Op == "" {
					t.Error("the error carries no operation name")
				}
				if r := onlyRecord(t, &recorder); r.Requests != 0 || r.Err == nil || r.Op != e.Op {
					t.Errorf("record = op %q, %d requests, error %v; want the call's operation, no requests and its error", r.Op, r.Requests, r.Err)
				}
				if model.Calls() != 0 {
					t.Errorf("%d requests were sent", model.Calls())
				}
			})
		}
		t.Run(name+"/live", func(t *testing.T) {
			t.Parallel()
			ctx, stop := context.WithTimeout(operationClient(answeringWith("{}")), time.Hour)
			defer stop()
			if err := call(ctx); err != nil {
				t.Fatalf("a context that has not ended refused the call: %v", err)
			}
		})
	}
}
