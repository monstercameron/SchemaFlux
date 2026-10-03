package requestid_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/requestid"
)

// configure sets the process-wide configuration for one test and puts back what
// was there when the test ends. Tests that call it do not run in parallel.
func configure(t *testing.T, c requestid.Config) {
	t.Helper()
	t.Cleanup(mustConfigure(t, c))
}

// mustConfigure is Configure for a Config the test knows to be good. It reports
// an error with t.Errorf, which a goroutine may call, and returns a function
// that does nothing.
func mustConfigure(t testing.TB, c requestid.Config) func() {
	t.Helper()
	restore, err := requestid.Configure(c)
	if err != nil {
		t.Errorf("Configure(%+v): %v", c, err)
		return func() {}
	}
	return restore
}

func withIDs(request, correlation string) context.Context {
	return requestid.WithCorrelation(requestid.With(context.Background(), request), correlation)
}

func TestInjectWritesTheIdsUnderTheDefaultHeaderNames(t *testing.T) {
	h := http.Header{}
	requestid.Inject(withIDs("req-1", "corr-1"), h)
	if got := h.Get("X-Request-ID"); got != "req-1" {
		t.Errorf("X-Request-ID = %q, want req-1", got)
	}
	if got := h.Get("X-Correlation-ID"); got != "corr-1" {
		t.Errorf("X-Correlation-ID = %q, want corr-1", got)
	}
	if len(h) != 2 {
		t.Errorf("header holds %v, want exactly the two ids", h)
	}
}

func TestInjectWritesTheCanonicalSpellingOfTheNames(t *testing.T) {
	h := http.Header{}
	requestid.Inject(withIDs("req-1", "corr-1"), h)
	if _, ok := h["X-Request-Id"]; !ok {
		t.Errorf("no canonical key X-Request-Id in %v", h)
	}
	if _, ok := h["X-Correlation-Id"]; !ok {
		t.Errorf("no canonical key X-Correlation-Id in %v", h)
	}
}

func TestInjectWritesOnlyTheIdsTheContextCarries(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want http.Header
	}{
		{"a context with a request id only", requestid.With(context.Background(), "req-1"), http.Header{"X-Request-Id": {"req-1"}}},
		{"a context with a correlation id only", requestid.WithCorrelation(context.Background(), "corr-1"), http.Header{"X-Correlation-Id": {"corr-1"}}},
		{"a context with neither writes nothing", context.Background(), http.Header{}},
		{"a nil context writes nothing", nil, http.Header{}},
		{"a refused id writes nothing", requestid.With(context.Background(), "has space"), http.Header{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			requestid.Inject(c.ctx, h)
			assertHeader(t, h, c.want)
		})
	}
}

func TestInjectLeavesOtherHeadersAndMakesNoId(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer x"}, "Accept": {"a", "b"}}
	requestid.Inject(context.Background(), h)
	assertHeader(t, h, http.Header{"Authorization": {"Bearer x"}, "Accept": {"a", "b"}})
}

func TestInjectReplacesWhatTheHeaderAlreadyHolds(t *testing.T) {
	h := http.Header{"X-Request-Id": {"old-1", "old-2"}, "X-Correlation-Id": {"old"}}
	requestid.Inject(withIDs("new-r", "new-c"), h)
	assertHeader(t, h, http.Header{"X-Request-Id": {"new-r"}, "X-Correlation-Id": {"new-c"}})
}

func TestInjectKeepsTheHeaderTheContextHasNothingFor(t *testing.T) {
	h := http.Header{"X-Correlation-Id": {"from-the-caller"}}
	requestid.Inject(requestid.With(context.Background(), "req-1"), h)
	assertHeader(t, h, http.Header{"X-Request-Id": {"req-1"}, "X-Correlation-Id": {"from-the-caller"}})
}

func TestInjectIgnoresANilHeaderAndLeavesTheContextAndTheNextCallAsTheyWere(t *testing.T) {
	ctx := withIDs("req-1", "corr-1")
	var nilHeader http.Header
	requestid.Inject(ctx, nilHeader)
	if nilHeader != nil {
		t.Errorf("a nil header became %v", nilHeader)
	}
	if got := requestid.From(ctx); got != (requestid.IDs{Request: "req-1", Correlation: "corr-1"}) {
		t.Errorf("the context now carries %+v", got)
	}
	h := http.Header{}
	requestid.Inject(ctx, h)
	assertHeader(t, h, http.Header{"X-Request-Id": {"req-1"}, "X-Correlation-Id": {"corr-1"}})
}

func TestInjectUsesTheConfiguredHeaderNames(t *testing.T) {
	configure(t, requestid.Config{Header: "traceparent-id", CorrelationHeader: "X_Job"})
	h := http.Header{}
	requestid.Inject(withIDs("req-1", "corr-1"), h)
	assertHeader(t, h, http.Header{"Traceparent-Id": {"req-1"}, "X_job": {"corr-1"}})
	if got := h.Get("traceparent-id"); got != "req-1" {
		t.Errorf("Get(traceparent-id) = %q, want req-1", got)
	}
}

func TestInjectUsesOneConfiguredNameAndTheDefaultOfTheOther(t *testing.T) {
	configure(t, requestid.Config{Header: "Request-Id"})
	h := http.Header{}
	requestid.Inject(withIDs("req-1", "corr-1"), h)
	assertHeader(t, h, http.Header{"Request-Id": {"req-1"}, "X-Correlation-Id": {"corr-1"}})
}

func TestInjectStillWritesTheIdsOfAContextWhenIdsAreNotMade(t *testing.T) {
	configure(t, requestid.Config{Disabled: true})
	h := http.Header{}
	requestid.Inject(withIDs("req-1", "corr-1"), h)
	assertHeader(t, h, http.Header{"X-Request-Id": {"req-1"}, "X-Correlation-Id": {"corr-1"}})
}

func TestInjectDoesNotWriteAnIdThatBreaksTheBound(t *testing.T) {
	// A context can be filled by code that is not this package. What goes out on
	// the wire is held to the rule whoever stored it.
	ctx := requesttracking.WithMetadata(context.Background(), requesttracking.Metadata{
		RequestID:     "req\r\nX-Evil: 1",
		CorrelationID: strings.Repeat("c", 5000),
	})
	h := http.Header{}
	requestid.Inject(ctx, h)
	assertHeader(t, h, http.Header{})
}

func TestInjectSendsTheIdsOnARealRequest(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer server.Close()
	req, err := http.NewRequestWithContext(withIDs("req-77", "corr-77"), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestid.Inject(req.Context(), req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Get("X-Request-ID") != "req-77" || got.Get("X-Correlation-ID") != "corr-77" {
		t.Errorf("server saw %v", got)
	}
}

func TestExtractReadsTheIdsFromTheDefaultHeaderNames(t *testing.T) {
	h := http.Header{}
	h.Set("X-Request-ID", "req-1")
	h.Set("X-Correlation-ID", "corr-1")
	got := requestid.From(requestid.Extract(context.Background(), h))
	if got != (requestid.IDs{Request: "req-1", Correlation: "corr-1"}) {
		t.Errorf("Extract gave %+v", got)
	}
}

func TestExtractCases(t *testing.T) {
	long := strings.Repeat("a", 129)
	cases := []struct {
		name   string
		header http.Header
		want   requestid.IDs
	}{
		{"both headers", http.Header{"X-Request-Id": {"r"}, "X-Correlation-Id": {"c"}}, requestid.IDs{Request: "r", Correlation: "c"}},
		{"the request header alone", http.Header{"X-Request-Id": {"r"}}, requestid.IDs{Request: "r"}},
		{"the correlation header alone", http.Header{"X-Correlation-Id": {"c"}}, requestid.IDs{Correlation: "c"}},
		{"an empty header map", http.Header{}, requestid.IDs{}},
		{"unrelated headers only", http.Header{"Accept": {"*/*"}, "X-Request": {"r"}}, requestid.IDs{}},
		{"a header with no values", http.Header{"X-Request-Id": {}}, requestid.IDs{}},
		{"a header with one empty value", http.Header{"X-Request-Id": {""}}, requestid.IDs{}},
		{"a blank value", http.Header{"X-Request-Id": {"  \t"}}, requestid.IDs{}},
		{"a value with surrounding blanks is trimmed", http.Header{"X-Request-Id": {" r \t"}}, requestid.IDs{Request: "r"}},
		{"a value of 128 bytes is kept", http.Header{"X-Request-Id": {strings.Repeat("a", 128)}}, requestid.IDs{Request: strings.Repeat("a", 128)}},
		{"a value of 129 bytes is refused", http.Header{"X-Request-Id": {long}}, requestid.IDs{}},
		{"a value of 5000 bytes with a newline and a NUL is refused", http.Header{"X-Request-Id": {strings.Repeat("a", 2500) + "\n\x00" + strings.Repeat("b", 2498)}}, requestid.IDs{}},
		{"a value with a space inside is refused", http.Header{"X-Request-Id": {"a b"}}, requestid.IDs{}},
		{"a value with a NUL is refused", http.Header{"X-Request-Id": {"a\x00b"}}, requestid.IDs{}},
		{"a value with a non-ASCII letter is refused", http.Header{"X-Request-Id": {"café"}}, requestid.IDs{}},
		{"a refused request id leaves the correlation id", http.Header{"X-Request-Id": {long}, "X-Correlation-Id": {"c"}}, requestid.IDs{Correlation: "c"}},
		{"a refused correlation id leaves the request id", http.Header{"X-Request-Id": {"r"}, "X-Correlation-Id": {"bad id"}}, requestid.IDs{Request: "r"}},
		{"a comma separated list of ids is refused", http.Header{"X-Request-Id": {"a, b"}}, requestid.IDs{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requestid.From(requestid.Extract(context.Background(), c.header))
			if got != c.want {
				t.Errorf("Extract gave %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestExtractMatchesHeaderNamesWithoutRegardToCase(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   requestid.IDs
	}{
		{"all lower case keys in a hand built map", http.Header{"x-request-id": {"r"}, "x-correlation-id": {"c"}}, requestid.IDs{Request: "r", Correlation: "c"}},
		{"all upper case keys", http.Header{"X-REQUEST-ID": {"r"}, "X-CORRELATION-ID": {"c"}}, requestid.IDs{Request: "r", Correlation: "c"}},
		{"mixed case keys", http.Header{"x-ReQuEsT-iD": {"r"}}, requestid.IDs{Request: "r"}},
		{"the spelling in the configuration", http.Header{"X-Request-ID": {"r"}}, requestid.IDs{Request: "r"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requestid.From(requestid.Extract(context.Background(), c.header))
			if got != c.want {
				t.Errorf("Extract gave %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestExtractChoosesTheSameValueWhenKeysDifferOnlyInCase(t *testing.T) {
	// A hand built map can hold the same name in two spellings. Map order is
	// random, so the choice has to come from the keys: the canonical spelling
	// first, then the smallest.
	for i := 0; i < 50; i++ {
		h := http.Header{"x-request-id": {"lower"}, "X-REQUEST-ID": {"upper"}, "X-Request-Id": {"canonical"}}
		if got := requestid.From(requestid.Extract(context.Background(), h)).Request; got != "canonical" {
			t.Fatalf("round %d: chose %q, want the canonical spelling", i, got)
		}
		delete(h, "X-Request-Id")
		if got := requestid.From(requestid.Extract(context.Background(), h)).Request; got != "upper" {
			t.Fatalf("round %d: chose %q, want the smallest key (X-REQUEST-ID)", i, got)
		}
	}
}

func TestExtractTakesTheFirstOfSeveralValues(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   requestid.IDs
	}{
		{"two valid values", http.Header{"X-Request-Id": {"first", "second"}}, requestid.IDs{Request: "first"}},
		{"a refused first value is not replaced by a later one", http.Header{"X-Request-Id": {"bad id", "second"}}, requestid.IDs{}},
		{"an empty first value is not replaced by a later one", http.Header{"X-Request-Id": {"", "second"}}, requestid.IDs{}},
		{"each header takes its own first value", http.Header{"X-Request-Id": {"r1", "r2"}, "X-Correlation-Id": {"c1", "c2"}}, requestid.IDs{Request: "r1", Correlation: "c1"}},
		{"three values", http.Header{"X-Correlation-Id": {"a", "b", "c"}}, requestid.IDs{Correlation: "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requestid.From(requestid.Extract(context.Background(), c.header))
			if got != c.want {
				t.Errorf("Extract gave %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestExtractUsesTheConfiguredHeaderNames(t *testing.T) {
	configure(t, requestid.Config{Header: "Request-Id", CorrelationHeader: "x-job"})
	h := http.Header{"Request-Id": {"r"}, "X-Job": {"j"}, "X-Request-Id": {"default-r"}, "X-Correlation-Id": {"default-c"}}
	got := requestid.From(requestid.Extract(context.Background(), h))
	if got != (requestid.IDs{Request: "r", Correlation: "j"}) {
		t.Errorf("Extract gave %+v, want the ids under the configured names", got)
	}
}

func TestExtractReadsTheDefaultNameForTheNameNotConfigured(t *testing.T) {
	configure(t, requestid.Config{CorrelationHeader: "X-Job"})
	h := http.Header{"X-Request-Id": {"r"}, "X-Job": {"j"}, "X-Correlation-Id": {"ignored"}}
	got := requestid.From(requestid.Extract(context.Background(), h))
	if got != (requestid.IDs{Request: "r", Correlation: "j"}) {
		t.Errorf("Extract gave %+v", got)
	}
}

func TestExtractKeepsWhatTheContextHasWhenTheHeaderHasNothingUsable(t *testing.T) {
	base := withIDs("outer-r", "outer-c")
	for name, h := range map[string]http.Header{
		"nil header":        nil,
		"empty header":      {},
		"unrelated headers": {"Accept": {"*/*"}},
		"refused values":    {"X-Request-Id": {"bad id"}, "X-Correlation-Id": {strings.Repeat("x", 500)}},
		"empty values":      {"X-Request-Id": {""}, "X-Correlation-Id": {" "}},
	} {
		t.Run(name, func(t *testing.T) {
			got := requestid.Extract(base, h)
			if ids := requestid.From(got); ids != (requestid.IDs{Request: "outer-r", Correlation: "outer-c"}) {
				t.Errorf("Extract changed the ids to %+v", ids)
			}
		})
	}
}

func TestExtractNilHeaderReturnsTheContextItself(t *testing.T) {
	base := context.Background()
	if got := requestid.Extract(base, nil); got != base {
		t.Error("Extract with a nil header returned a different context")
	}
}

func TestExtractedIdsAreNearerThanTheOnesAlreadyOnTheContext(t *testing.T) {
	base := withIDs("outer-r", "outer-c")
	h := http.Header{"X-Request-Id": {"inner-r"}}
	got := requestid.From(requestid.Extract(base, h))
	if got != (requestid.IDs{Request: "inner-r", Correlation: "outer-c"}) {
		t.Errorf("Extract gave %+v, want the header's request id over the context's, and the context's correlation id kept", got)
	}
}

func TestExtractPanicsOnANilContext(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Extract with a nil context did not panic")
		}
	}()
	var nilContext context.Context
	requestid.Extract(nilContext, http.Header{"X-Request-Id": {"r"}})
}

func TestInjectThenExtractRoundTrips(t *testing.T) {
	for _, names := range []requestid.Config{
		{},
		{Header: "Request-Id", CorrelationHeader: "Job-Id"},
		{Header: "x-lower", CorrelationHeader: "X-UPPER"},
	} {
		t.Run(names.Header+"/"+names.CorrelationHeader, func(t *testing.T) {
			configure(t, names)
			h := http.Header{}
			requestid.Inject(withIDs("r-1", "c-1"), h)
			got := requestid.From(requestid.Extract(context.Background(), h))
			if got != (requestid.IDs{Request: "r-1", Correlation: "c-1"}) {
				t.Errorf("round trip gave %+v", got)
			}
		})
	}
}

func TestExtractReadsAHeaderThatTheWireDelivered(t *testing.T) {
	var got requestid.IDs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requestid.From(requestid.Extract(r.Context(), r.Header))
	}))
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header["x-request-id"] = []string{"wire-r"} // written as a client may, not canonical
	req.Header.Set("X-Correlation-ID", "wire-c")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != (requestid.IDs{Request: "wire-r", Correlation: "wire-c"}) {
		t.Errorf("handler read %+v", got)
	}
}

func assertHeader(t *testing.T, got, want http.Header) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("header = %v, want %v", got, want)
		return
	}
	for key, values := range want {
		have := got[key]
		if len(have) != len(values) {
			t.Errorf("header[%q] = %v, want %v", key, have, values)
			continue
		}
		for i := range values {
			if have[i] != values[i] {
				t.Errorf("header[%q] = %v, want %v", key, have, values)
			}
		}
	}
}
