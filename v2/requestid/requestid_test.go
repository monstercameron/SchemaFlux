package requestid_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/requestid"
)

// idCase is one id offered to With or WithCorrelation and the id the context
// holds afterwards ("" when the id is refused).
type idCase struct {
	name string
	id   string
	want string
}

// idCases are the ids both With and WithCorrelation judge by the same rule:
// at most 128 bytes of printable ASCII without spaces, after surrounding white
// space is trimmed.
var idCases = []idCase{
	{"a plain id is kept", "order-7", "order-7"},
	{"a version 4 UUID is kept", "3b241101-e2bb-4255-8caf-4136c566a962", "3b241101-e2bb-4255-8caf-4136c566a962"},
	{"every printable punctuation mark is kept", "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"},
	{"surrounding blanks are trimmed", " \t order-7 \r\n", "order-7"},
	{"an id of exactly 128 bytes is kept", strings.Repeat("a", 128), strings.Repeat("a", 128)},
	{"an id of 128 bytes inside a great deal of white space is kept", strings.Repeat(" ", 2436) + strings.Repeat("a", 128) + strings.Repeat("\n", 2436), strings.Repeat("a", 128)},
	{"an empty id is refused", "", ""},
	{"a blank id is refused", "  \t\r\n ", ""},
	{"an id of 129 bytes is refused", strings.Repeat("a", 129), ""},
	{"an id of 5000 plain bytes is refused", strings.Repeat("a", 5000), ""},
	{"an id of 5000 bytes holding a newline and a NUL is refused", strings.Repeat("a", 2500) + "\n\x00" + strings.Repeat("b", 2498), ""},
	{"an id with a space inside is refused", "order 7", ""},
	{"an id with a newline inside is refused", "order\n7", ""},
	{"an attempt to add a header line is refused", "7\r\nX-Evil: 1", ""},
	{"an id with a NUL inside is refused", "order\x007", ""},
	{"an id ending in a NUL is refused", "order-7\x00", ""},
	{"an id with a tab inside is refused", "order\t7", ""},
	{"an id with a DEL byte is refused", "order\x7f7", ""},
	{"an id with a non-ASCII letter is refused", "café", ""},
	{"an id of bytes that are not UTF-8 is refused", "\xff\xfe", ""},
	{"an id of one printable byte is kept", "~", "~"},
}

// writers are the two functions that store a caller's id, with the field of IDs
// each one fills and the field it must leave alone.
var writers = []struct {
	name  string
	write func(context.Context, string) context.Context
	read  func(requestid.IDs) string
	other func(requestid.IDs) string
}{
	{"With", requestid.With, func(i requestid.IDs) string { return i.Request }, func(i requestid.IDs) string { return i.Correlation }},
	{"WithCorrelation", requestid.WithCorrelation, func(i requestid.IDs) string { return i.Correlation }, func(i requestid.IDs) string { return i.Request }},
}

func TestWritersStoreAnIdOnlyWhenItIsBoundedAndPrintable(t *testing.T) {
	for _, w := range writers {
		for _, c := range idCases {
			t.Run(w.name+"/"+c.name, func(t *testing.T) {
				base := context.Background()
				ctx := w.write(base, c.id)
				if got := w.read(requestid.From(ctx)); got != c.want {
					t.Errorf("%s(ctx, %.40q) stored %q, want %q", w.name, c.id, got, c.want)
				}
				if got := w.other(requestid.From(ctx)); got != "" {
					t.Errorf("%s set the other id to %q", w.name, got)
				}
				if c.want == "" && ctx != base {
					t.Errorf("%s(ctx, %.40q) refused the id but returned a different context", w.name, c.id)
				}
			})
		}
	}
}

func TestWritersApplyTheRuleTheTrackingCodeAppliesToHeaders(t *testing.T) {
	defaults := requesttracking.GetConfig()
	for _, c := range idCases {
		t.Run(c.name, func(t *testing.T) {
			carrier := map[string]string{defaults.RequestIDHeader: c.id}
			internal := requesttracking.FromContext(requesttracking.Extract(context.Background(), carrier)).RequestID
			if internal != c.want {
				t.Fatalf("internal\\requesttracking kept %q for %.40q, the table says %q: the two bounds have drifted apart", internal, c.id, c.want)
			}
		})
	}
}

func TestNestedIdsTheNearerOneWins(t *testing.T) {
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			outer := w.write(context.Background(), "outer")
			inner := w.write(outer, "inner")
			if got := w.read(requestid.From(inner)); got != "inner" {
				t.Errorf("inner context holds %q, want inner", got)
			}
			if got := w.read(requestid.From(outer)); got != "outer" {
				t.Errorf("writing the inner id changed the outer context to %q", got)
			}
		})
	}
}

func TestNestedIdsThroughThreeLevelsAndOtherContextKinds(t *testing.T) {
	ctx := requestid.With(context.Background(), "one")
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	ctx = requestid.With(ctx, "two")
	ctx = context.WithValue(ctx, struct{}{}, "unrelated")
	ctx = requestid.With(ctx, "three")
	if got := requestid.From(ctx).Request; got != "three" {
		t.Errorf("nearest of three = %q, want three", got)
	}
}

func TestARefusedIdDoesNotHideTheOuterOne(t *testing.T) {
	for _, w := range writers {
		for _, refused := range []string{"", "   ", "has space", strings.Repeat("z", 5000), "nul\x00"} {
			outer := w.write(context.Background(), "outer")
			inner := w.write(outer, refused)
			if got := w.read(requestid.From(inner)); got != "outer" {
				t.Errorf("%s(outer, %.20q): context holds %q, want the outer id", w.name, refused, got)
			}
		}
	}
}

func TestRequestAndCorrelationIdsAreIndependent(t *testing.T) {
	ctx := requestid.With(context.Background(), "request-1")
	ctx = requestid.WithCorrelation(ctx, "job-9")
	ctx = requestid.With(ctx, "request-2")
	got := requestid.From(ctx)
	if got != (requestid.IDs{Request: "request-2", Correlation: "job-9"}) {
		t.Errorf("From = %+v, want request-2 and job-9", got)
	}
}

func TestWritersPanicOnANilContextLikeContextWithValue(t *testing.T) {
	for _, w := range writers {
		for _, id := range []string{"order-7", "", strings.Repeat("a", 5000)} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s(nil, %.10q) did not panic", w.name, id)
					}
				}()
				var nilContext context.Context
				w.write(nilContext, id)
			}()
		}
	}
}

func TestFromReadsWhatTheContextCarries(t *testing.T) {
	var nilContext context.Context
	type cancelKey struct{}
	cancelled, cancel := context.WithCancel(requestid.WithCorrelation(requestid.With(context.Background(), "r"), "c"))
	cancel()
	deadline, stop := context.WithTimeout(requestid.With(context.Background(), "r"), time.Hour)
	defer stop()

	cases := []struct {
		name string
		ctx  context.Context
		want requestid.IDs
	}{
		{"a background context carries none", context.Background(), requestid.IDs{}},
		{"a TODO context carries none", context.TODO(), requestid.IDs{}},
		{"a nil context carries none", nilContext, requestid.IDs{}},
		{"a request id alone", requestid.With(context.Background(), "r"), requestid.IDs{Request: "r"}},
		{"a correlation id alone", requestid.WithCorrelation(context.Background(), "c"), requestid.IDs{Correlation: "c"}},
		{"both ids", requestid.WithCorrelation(requestid.With(context.Background(), "r"), "c"), requestid.IDs{Request: "r", Correlation: "c"}},
		{"both ids written in the other order", requestid.With(requestid.WithCorrelation(context.Background(), "c"), "r"), requestid.IDs{Request: "r", Correlation: "c"}},
		{"ids survive a cancelled context", cancelled, requestid.IDs{Request: "r", Correlation: "c"}},
		{"ids survive a deadline", deadline, requestid.IDs{Request: "r"}},
		{"ids survive context.WithoutCancel", context.WithoutCancel(cancelled), requestid.IDs{Request: "r", Correlation: "c"}},
		{"unrelated values do not look like ids", context.WithValue(context.Background(), cancelKey{}, "r"), requestid.IDs{}},
		{"a refused id leaves nothing", requestid.With(context.Background(), "has space"), requestid.IDs{}},
		{"ids put on by the tracking code are read", requesttracking.WithMetadata(context.Background(), requesttracking.Metadata{RequestID: "r", CorrelationID: "c"}), requestid.IDs{Request: "r", Correlation: "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := requestid.From(c.ctx); got != c.want {
				t.Errorf("From = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestFromReturnsACopyTheCallerCanChange(t *testing.T) {
	ctx := requestid.With(context.Background(), "r")
	ids := requestid.From(ctx)
	ids.Request = "changed"
	if got := requestid.From(ctx).Request; got != "r" {
		t.Errorf("changing the returned IDs changed the context to %q", got)
	}
}

func TestEveryContextThatCarriesAnIdAlsoReachesTheTrackingCode(t *testing.T) {
	// The library resolves a call's ids with requesttracking.Ensure on the context
	// it is given, so an id put on a context here must come out of Ensure as it
	// went in, whatever strategy is configured.
	restore := mustConfigure(t, requestid.Config{Strategy: requestid.StrategyUUID, Correlation: requestid.CorrelationGenerate})
	defer restore()
	ctx := requestid.With(context.Background(), "mine")
	_, got := requesttracking.Ensure(ctx, "", "")
	if got.RequestID != "mine" {
		t.Errorf("Ensure resolved the request id %q, want mine", got.RequestID)
	}
	if !strings.HasPrefix(got.CorrelationID, "corr_") {
		t.Errorf("Ensure resolved the correlation id %q, want a generated one", got.CorrelationID)
	}
}
