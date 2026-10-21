package telemetry_test

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// idKey is the context key of the ids a test's readers find.
type idKey struct{}

// reader returns a reader that answers prefix plus the value under idKey, or
// the empty string when the context carries none.
func reader(prefix string) func(context.Context) string {
	return func(ctx context.Context) string {
		if id, ok := ctx.Value(idKey{}).(string); ok {
			return prefix + id
		}
		return ""
	}
}

func TestWithNoReadersInstalledTheIDsAreEmpty(t *testing.T) {
	ctx := context.WithValue(context.Background(), idKey{}, "42")
	if telemetry.TraceID(ctx) != "" || telemetry.SpanID(ctx) != "" {
		t.Errorf("trace %q and span %q with no reader installed, want both empty", telemetry.TraceID(ctx), telemetry.SpanID(ctx))
	}
	//lint:ignore SA1012 a nil context is what a careless caller passes
	if telemetry.TraceID(nil) != "" || telemetry.SpanID(nil) != "" {
		t.Error("a nil context gave an id")
	}
}

func TestInstalledReadersAnswerForTheContextTheyAreGiven(t *testing.T) {
	restore := telemetry.SetSpanIDSources(reader("t-"), reader("s-"))
	defer restore()

	cases := []struct {
		name        string
		ctx         context.Context
		trace, span string
	}{
		{"a context with ids", context.WithValue(context.Background(), idKey{}, "7"), "t-7", "s-7"},
		{"a context derived from one with ids", context.WithValue(context.WithValue(context.Background(), idKey{}, "8"), stampKey{}, "x"), "t-8", "s-8"},
		{"a context with none", context.Background(), "", ""},
		{"a nil context", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := telemetry.TraceID(tc.ctx); got != tc.trace {
				t.Errorf("TraceID = %q, want %q", got, tc.trace)
			}
			if got := telemetry.SpanID(tc.ctx); got != tc.span {
				t.Errorf("SpanID = %q, want %q", got, tc.span)
			}
		})
	}
}

// The reader in effect is the newest that has not been removed, whatever the
// order the removals run in, and a removal run a second time brings nothing back.
func TestReadersComeOutInAnyOrderAndARestoreRunTwiceBringsNothingBack(t *testing.T) {
	ctx := context.WithValue(context.Background(), idKey{}, "1")
	cases := []struct {
		name  string
		order []int // restores run, by install position; a position may repeat
		want  string
	}{
		{"none", nil, "t-b1"},
		{"the older only", []int{0}, "t-b1"},
		{"the newer only", []int{1}, "t-a1"},
		{"newer then older", []int{1, 0}, ""},
		{"older then newer", []int{0, 1}, ""},
		{"newer, older, newer again", []int{1, 0, 1}, ""},
		{"older, newer, older again", []int{0, 1, 0}, ""},
		{"the older twice", []int{0, 0}, "t-b1"},
		{"the newer twice", []int{1, 1}, "t-a1"},
		{"the newer twice then the older twice", []int{1, 1, 0, 0}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restores := []func(){
				telemetry.SetSpanIDSources(reader("t-a"), reader("s-a")),
				telemetry.SetSpanIDSources(reader("t-b"), reader("s-b")),
			}
			t.Cleanup(func() {
				for _, restore := range restores {
					restore()
				}
			})
			for _, index := range tc.order {
				restores[index]()
			}
			if got := telemetry.TraceID(ctx); got != tc.want {
				t.Errorf("TraceID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestANilReaderIsTheDefaultForThatReaderAlone(t *testing.T) {
	ctx := context.WithValue(context.Background(), idKey{}, "5")
	defer telemetry.SetSpanIDSources(reader("t-"), nil)()
	if got := telemetry.TraceID(ctx); got != "t-5" {
		t.Errorf("TraceID = %q, want the installed reader's answer", got)
	}
	if got := telemetry.SpanID(ctx); got != "" {
		t.Errorf("SpanID = %q, want the empty default", got)
	}

	// A newer install of a nil reader gives that reader its default again, and
	// leaves the other as the newer install set it.
	restore := telemetry.SetSpanIDSources(nil, reader("s-"))
	if telemetry.TraceID(ctx) != "" || telemetry.SpanID(ctx) != "s-5" {
		t.Errorf("trace %q and span %q after a newer install of (nil, reader)", telemetry.TraceID(ctx), telemetry.SpanID(ctx))
	}
	restore()
	if got := telemetry.TraceID(ctx); got != "t-5" {
		t.Errorf("TraceID = %q after the newer install was removed, want the older one's", got)
	}
}
