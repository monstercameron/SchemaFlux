package schemaflux

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func extractOn(ctx context.Context) error {
	_, err := Extract[limitsInvoice](ctx, "invoice")
	return err
}

func answeringClient() Client {
	return Client{Provider: &answerer{name: "recorder-settings", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"number":"A","total":1}`, FinishReason: "stop"}, nil
	}}, Model: "recorder-settings-model"}
}

// TestANegativeMaxBodyIsAConfigurationErrorWhereverTheRecorderIsAttached holds a
// recorder to the same setting whether it is on the client or on the context.
func TestANegativeMaxBodyIsAConfigurationErrorWhereverTheRecorderIsAttached(t *testing.T) {
	t.Parallel()
	for _, maxBody := range []int{-1, -4096, math.MinInt} {
		for _, bodies := range []bool{true, false} {
			t.Run("", func(t *testing.T) {
				t.Parallel()
				bad := &Recorder{Bodies: bodies, MaxBody: maxBody}

				onClient := answeringClient()
				onClient.Recorder = bad
				onContext := WithRecorder(WithClient(context.Background(), answeringClient()), bad)

				for where, ctx := range map[string]context.Context{
					"on the client":  WithClient(context.Background(), onClient),
					"on the context": onContext,
				} {
					err := extractOn(ctx)
					var e *Error
					if !errors.As(err, &e) || e.Kind != ErrConfiguration {
						t.Fatalf("%s: got %v, want a configuration error", where, err)
					}
					if !strings.Contains(e.Message, "Recorder.MaxBody") {
						t.Errorf("%s: the error does not name the field: %q", where, e.Message)
					}
					if e.Op != "extract" {
						t.Errorf("%s: Op = %q, want extract", where, e.Op)
					}
					if len(bad.Records()) != 0 {
						t.Errorf("%s: the bad recorder was given a Record", where)
					}
				}
			})
		}
	}
}

func TestARecorderThatIsFineStillRecordsACallRefusedForAnotherOnTheContext(t *testing.T) {
	t.Parallel()
	bad := &Recorder{MaxBody: -1}
	var fine Recorder
	ctx := WithRecorder(WithRecorder(WithClient(context.Background(), answeringClient()), bad), &fine)
	if err := extractOn(ctx); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("got %v, want a configuration error", err)
	}
	records := fine.Records()
	if len(records) != 1 || records[0].Op != "extract" || !errors.Is(records[0].Err, ErrConfiguration) || records[0].Requests != 0 {
		t.Errorf("the recorder that is fine holds %+v, want one record of the refused call", records)
	}
	if len(bad.Records()) != 0 {
		t.Error("the bad recorder was given a Record")
	}
}

func TestARecorderOnTheContextWithGoodSettingsStillRecords(t *testing.T) {
	t.Parallel()
	for _, maxBody := range []int{0, 1, 4096, math.MaxInt} {
		var rec Recorder
		rec.MaxBody = maxBody
		rec.Bodies = true
		if err := extractOn(WithRecorder(WithClient(context.Background(), answeringClient()), &rec)); err != nil {
			t.Fatalf("MaxBody %d: %v", maxBody, err)
		}
		if len(rec.Records()) != 1 {
			t.Errorf("MaxBody %d: %d records, want 1", maxBody, len(rec.Records()))
		}
	}
}
