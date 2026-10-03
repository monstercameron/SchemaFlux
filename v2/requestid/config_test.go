package requestid_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/requestid"
)

// defaultInternal is what the zero Config means to the tracking code.
var defaultInternal = requesttracking.Config{
	Enabled:               true,
	RequestIDStrategy:     requesttracking.IDStrategyAuto,
	CorrelationIDStrategy: requesttracking.CorrelationStrategyInherit,
	RequestIDHeader:       "X-Request-ID",
	CorrelationIDHeader:   "X-Correlation-ID",
}

// made resolves the ids of one call that supplied none, the way a v2
// operation asks for them. It is how a test sees what a strategy produces, since
// the package exports no function that makes an id.
func made(ctx context.Context) requesttracking.Metadata {
	_, ids := requesttracking.Ensure(ctx, "", "")
	return ids
}

var (
	autoForm      = regexp.MustCompile(`^req_[0-9a-f]{16}$`)
	uuidForm      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	timestampForm = regexp.MustCompile(`^req_[0-9]{15,}_[0-9]+_[0-9]+$`)
	generatedForm = regexp.MustCompile(`^corr_[0-9a-f]{16}$`)
)

func TestAZeroConfigIsTheDefaultBehaviour(t *testing.T) {
	configure(t, requestid.Config{})
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("the zero Config gave %+v, want %+v", got, defaultInternal)
	}
	ids := made(context.Background())
	if !autoForm.MatchString(ids.RequestID) {
		t.Errorf("request id %q, want req_ and sixteen hex digits", ids.RequestID)
	}
	if ids.CorrelationID != ids.RequestID {
		t.Errorf("correlation id %q, want the request id %q", ids.CorrelationID, ids.RequestID)
	}
}

func TestTheConfigurationBeforeAnyConfigureIsTheDefault(t *testing.T) {
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("before Configure the configuration is %+v, want %+v", got, defaultInternal)
	}
}

func TestEachStrategyMakesIdsOfItsDocumentedForm(t *testing.T) {
	trace := "4bf92f3577b34da6a3ce929d0e0e4736"
	cases := []struct {
		name     string
		strategy requestid.Strategy
		source   func(context.Context) string
		check    func(string) bool
	}{
		{"auto", requestid.StrategyAuto, nil, autoForm.MatchString},
		{"uuid", requestid.StrategyUUID, nil, uuidForm.MatchString},
		{"timestamp", requestid.StrategyTimestamp, nil, timestampForm.MatchString},
		{"trace with a span on the context", requestid.StrategyTrace, func(context.Context) string { return trace }, func(id string) bool { return id == trace }},
		{"trace with no span makes an auto id", requestid.StrategyTrace, func(context.Context) string { return "" }, autoForm.MatchString},
		{"trace with no source installed makes an auto id", requestid.StrategyTrace, nil, autoForm.MatchString},
		{"none makes no id", requestid.StrategyNone, nil, func(id string) bool { return id == "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.source != nil {
				t.Cleanup(requesttracking.SetTraceIDSource(c.source))
			}
			configure(t, requestid.Config{Strategy: c.strategy, Correlation: requestid.CorrelationNone})
			for i := 0; i < 20; i++ {
				if id := made(context.Background()).RequestID; !c.check(id) {
					t.Fatalf("strategy %s made %q", c.name, id)
				}
			}
		})
	}
}

func TestEachStrategyMakesUniqueIdsAcrossFiftyGoroutines(t *testing.T) {
	const goroutines, each = 50, 200
	for _, c := range []struct {
		name     string
		strategy requestid.Strategy
	}{
		{"auto", requestid.StrategyAuto},
		{"uuid", requestid.StrategyUUID},
		{"timestamp", requestid.StrategyTimestamp},
		{"trace falling back to auto", requestid.StrategyTrace},
	} {
		t.Run(c.name, func(t *testing.T) {
			configure(t, requestid.Config{Strategy: c.strategy})
			assertUnique(t, goroutines, each, func() string { return made(context.Background()).RequestID })
		})
	}
	t.Run("generated correlation ids", func(t *testing.T) {
		configure(t, requestid.Config{Correlation: requestid.CorrelationGenerate})
		assertUnique(t, goroutines, each, func() string { return made(context.Background()).CorrelationID })
	})
}

func assertUnique(t *testing.T, goroutines, each int, produce func() string) {
	t.Helper()
	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			ids := make([]string, each)
			for i := range ids {
				ids[i] = produce()
			}
			results[g] = ids
		})
	}
	wg.Wait()
	seen := map[string]struct{}{}
	for _, ids := range results {
		for _, id := range ids {
			if id == "" {
				t.Fatal("an id was empty")
			}
			if _, dup := seen[id]; dup {
				t.Fatalf("id %q was made twice", id)
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != goroutines*each {
		t.Fatalf("%d distinct ids, want %d", len(seen), goroutines*each)
	}
}

func TestEachCorrelationStrategyMakesItsDocumentedCorrelationId(t *testing.T) {
	cases := []struct {
		name string
		c    requestid.Correlation
		ctx  context.Context
		want func(requesttracking.Metadata) bool
	}{
		{"inherit with none on the context uses the request id", requestid.CorrelationInherit, context.Background(), func(m requesttracking.Metadata) bool { return m.CorrelationID == m.RequestID && m.RequestID != "" }},
		{"inherit keeps the one on the context", requestid.CorrelationInherit, requestid.WithCorrelation(context.Background(), "job-9"), func(m requesttracking.Metadata) bool {
			return m.CorrelationID == "job-9" && autoForm.MatchString(m.RequestID)
		}},
		{"inherit with the request id on the context uses it", requestid.CorrelationInherit, requestid.With(context.Background(), "r-1"), func(m requesttracking.Metadata) bool { return m.RequestID == "r-1" && m.CorrelationID == "r-1" }},
		{"generate makes a new corr id", requestid.CorrelationGenerate, context.Background(), func(m requesttracking.Metadata) bool {
			return generatedForm.MatchString(m.CorrelationID) && m.CorrelationID != m.RequestID
		}},
		{"generate keeps one on the context", requestid.CorrelationGenerate, requestid.WithCorrelation(context.Background(), "job-9"), func(m requesttracking.Metadata) bool { return m.CorrelationID == "job-9" }},
		{"none makes no correlation id", requestid.CorrelationNone, context.Background(), func(m requesttracking.Metadata) bool { return m.CorrelationID == "" && m.RequestID != "" }},
		{"none keeps one on the context", requestid.CorrelationNone, requestid.WithCorrelation(context.Background(), "job-9"), func(m requesttracking.Metadata) bool { return m.CorrelationID == "job-9" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			configure(t, requestid.Config{Correlation: c.c})
			if m := made(c.ctx); !c.want(m) {
				t.Errorf("ids = %+v", m)
			}
		})
	}
}

func TestInheritWithNoRequestIdFallsBackToAGeneratedCorrelationId(t *testing.T) {
	configure(t, requestid.Config{Strategy: requestid.StrategyNone, Correlation: requestid.CorrelationInherit})
	m := made(context.Background())
	if m.RequestID != "" || !generatedForm.MatchString(m.CorrelationID) {
		t.Errorf("with no request ids: %+v, want no request id and a generated correlation id", m)
	}
}

func TestIdsOfTheCallerAreNeverReplacedByAStrategy(t *testing.T) {
	for _, s := range []requestid.Strategy{requestid.StrategyAuto, requestid.StrategyUUID, requestid.StrategyTimestamp, requestid.StrategyTrace, requestid.StrategyNone} {
		for _, c := range []requestid.Correlation{requestid.CorrelationInherit, requestid.CorrelationGenerate, requestid.CorrelationNone} {
			configure(t, requestid.Config{Strategy: s, Correlation: c})
			m := made(withIDs("mine", "ours"))
			if m.RequestID != "mine" || m.CorrelationID != "ours" {
				t.Errorf("strategy %d, correlation %d replaced the caller's ids: %+v", s, c, m)
			}
		}
	}
}

func TestDisabledMakesNoIdAndKeepsTheCallersIds(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want requesttracking.Metadata
	}{
		{"nothing on the context", context.Background(), requesttracking.Metadata{}},
		{"a request id on the context", requestid.With(context.Background(), "r"), requesttracking.Metadata{RequestID: "r"}},
		{"a correlation id on the context", requestid.WithCorrelation(context.Background(), "c"), requesttracking.Metadata{CorrelationID: "c"}},
		{"both on the context", withIDs("r", "c"), requesttracking.Metadata{RequestID: "r", CorrelationID: "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			configure(t, requestid.Config{Disabled: true, Strategy: requestid.StrategyUUID, Correlation: requestid.CorrelationGenerate})
			if got := made(c.ctx); got != c.want {
				t.Errorf("ids = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestConfigureReachesTheTrackingCodeWithEveryField(t *testing.T) {
	configure(t, requestid.Config{
		Strategy:          requestid.StrategyUUID,
		Correlation:       requestid.CorrelationGenerate,
		Header:            "Request-Id",
		CorrelationHeader: "Job-Id",
	})
	want := requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyUUID,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyGenerate,
		RequestIDHeader:       "Request-Id",
		CorrelationIDHeader:   "Job-Id",
	}
	if got := requesttracking.GetConfig(); got != want {
		t.Errorf("configuration = %+v, want %+v", got, want)
	}
}

func TestConfigureMapsEveryStrategyAndCorrelation(t *testing.T) {
	strategies := map[requestid.Strategy]requesttracking.IDStrategy{
		requestid.StrategyAuto:      requesttracking.IDStrategyAuto,
		requestid.StrategyUUID:      requesttracking.IDStrategyUUID,
		requestid.StrategyTimestamp: requesttracking.IDStrategyTimestamp,
		requestid.StrategyTrace:     requesttracking.IDStrategyTrace,
		requestid.StrategyNone:      requesttracking.IDStrategyNone,
	}
	for in, want := range strategies {
		configure(t, requestid.Config{Strategy: in})
		if got := requesttracking.GetConfig().RequestIDStrategy; got != want {
			t.Errorf("strategy %d reached the tracking code as %q, want %q", in, got, want)
		}
	}
	correlations := map[requestid.Correlation]requesttracking.CorrelationStrategy{
		requestid.CorrelationInherit:  requesttracking.CorrelationStrategyInherit,
		requestid.CorrelationGenerate: requesttracking.CorrelationStrategyGenerate,
		requestid.CorrelationNone:     requesttracking.CorrelationStrategyNone,
	}
	for in, want := range correlations {
		configure(t, requestid.Config{Correlation: in})
		if got := requesttracking.GetConfig().CorrelationIDStrategy; got != want {
			t.Errorf("correlation %d reached the tracking code as %q, want %q", in, got, want)
		}
	}
}

func TestTheZeroValuesAreTheDefaultStrategies(t *testing.T) {
	var s requestid.Strategy
	var c requestid.Correlation
	if s != requestid.StrategyAuto {
		t.Errorf("the zero Strategy is not StrategyAuto: %d", s)
	}
	if c != requestid.CorrelationInherit {
		t.Errorf("the zero Correlation is not CorrelationInherit: %d", c)
	}
}

func TestConfigureReturnsAFunctionThatRestoresTheDefault(t *testing.T) {
	restore := mustConfigure(t, requestid.Config{Disabled: true, Strategy: requestid.StrategyNone, Header: "X-A", CorrelationHeader: "X-B"})
	if requesttracking.GetConfig() == defaultInternal {
		t.Fatal("Configure changed nothing")
	}
	restore()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("after restore the configuration is %+v, want the default", got)
	}
}

func TestRestoreIsASafeToCallTwice(t *testing.T) {
	first := mustConfigure(t, requestid.Config{Header: "X-First"})
	second := mustConfigure(t, requestid.Config{Header: "X-Second"})
	second()
	second() // a second call must not undo the first install, nor the default
	if got := requesttracking.GetConfig().RequestIDHeader; got != "X-First" {
		t.Errorf("after restoring the newer twice the header is %q, want X-First", got)
	}
	first()
	first()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("after both restores the configuration is %+v", got)
	}
}

func TestRestoreAfterANewerInstallWasRestoredDoesNotBringItBack(t *testing.T) {
	first := mustConfigure(t, requestid.Config{Header: "X-First"})
	second := mustConfigure(t, requestid.Config{Header: "X-Second"})
	second()
	third := mustConfigure(t, requestid.Config{Header: "X-Third"})
	second() // already restored: must not bring anything back
	if got := requesttracking.GetConfig().RequestIDHeader; got != "X-Third" {
		t.Errorf("header = %q, want X-Third", got)
	}
	third()
	if got := requesttracking.GetConfig().RequestIDHeader; got != "X-First" {
		t.Errorf("header = %q, want X-First", got)
	}
	first()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("configuration = %+v, want the default", got)
	}
}

func TestRestoringAnOlderInstallLeavesTheNewestInForce(t *testing.T) {
	older := mustConfigure(t, requestid.Config{Header: "X-Older"})
	newer := mustConfigure(t, requestid.Config{Header: "X-Newer"})
	older()
	if got := requesttracking.GetConfig().RequestIDHeader; got != "X-Newer" {
		t.Errorf("header = %q, want X-Newer", got)
	}
	newer()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("configuration = %+v, want the default (the older install was already restored)", got)
	}
}

// permutations calls f with every ordering of 0..n-1.
func permutations(n int, f func([]int)) {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			f(append([]int(nil), order...))
			return
		}
		for i := k; i < n; i++ {
			order[k], order[i] = order[i], order[k]
			rec(k + 1)
			order[k], order[i] = order[i], order[k]
		}
	}
	rec(0)
}

func TestRestoresInAnyOrderLeaveTheNewestInstallNotYetRestored(t *testing.T) {
	const n = 4
	count := 0
	permutations(n, func(order []int) {
		count++
		restores := make([]func(), n)
		for i := range restores {
			restores[i] = mustConfigure(t, requestid.Config{Header: fmt.Sprintf("X-Install-%d", i)})
		}
		undone := make([]bool, n)
		for step, which := range order {
			restores[which]()
			undone[which] = true
			want := "X-Request-ID"
			for i := n - 1; i >= 0; i-- {
				if !undone[i] {
					want = fmt.Sprintf("X-Install-%d", i)
					break
				}
			}
			if got := requesttracking.GetConfig().RequestIDHeader; got != want {
				t.Fatalf("order %v, after restoring %d (step %d): header %q, want %q", order, which, step, got, want)
			}
		}
		if got := requesttracking.GetConfig(); got != defaultInternal {
			t.Fatalf("order %v: final configuration %+v, want the default", order, got)
		}
	})
	if count != 24 {
		t.Fatalf("tried %d orders, want 24", count)
	}
}

func TestConfigureRefusesAConfigThatCannotWork(t *testing.T) {
	cases := []struct {
		name  string
		c     requestid.Config
		field string // what the error must name
	}{
		{"a header name with a space", requestid.Config{Header: "X Request"}, "Header"},
		{"a header name with a newline", requestid.Config{Header: "X-Request\n"}, "Header"},
		{"a header name with a colon", requestid.Config{Header: "X-Request:"}, "Header"},
		{"a header name with a NUL", requestid.Config{Header: "X-Req\x00"}, "Header"},
		{"a header name with a non-ASCII letter", requestid.Config{Header: "X-R\u00e9q"}, "Header"},
		{"a header name with parentheses", requestid.Config{Header: "(request)"}, "Header"},
		{"a blank header name", requestid.Config{Header: "  "}, "Header"},
		{"a header name of 5000 bytes with a slash", requestid.Config{Header: strings.Repeat("h", 2500) + "/" + strings.Repeat("h", 2500)}, "Header"},
		{"a correlation header name with a slash", requestid.Config{CorrelationHeader: "X/Job"}, "CorrelationHeader"},
		{"a blank correlation header name", requestid.Config{CorrelationHeader: "\t"}, "CorrelationHeader"},
		{"both headers the same name", requestid.Config{Header: "X-Id", CorrelationHeader: "X-Id"}, "share"},
		{"both headers the same name in two cases", requestid.Config{Header: "x-id", CorrelationHeader: "X-ID"}, "share"},
		{"the request header taking the default correlation name", requestid.Config{Header: "x-correlation-id"}, "share"},
		{"the correlation header taking the default request name", requestid.Config{CorrelationHeader: "X-Request-ID"}, "share"},
		{"a strategy that does not exist", requestid.Config{Strategy: requestid.Strategy(5)}, "Strategy"},
		{"the largest strategy value", requestid.Config{Strategy: requestid.Strategy(255)}, "Strategy"},
		{"a correlation that does not exist", requestid.Config{Correlation: requestid.Correlation(4)}, "Correlation"},
		{"the largest correlation value", requestid.Config{Correlation: requestid.Correlation(255)}, "Correlation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := requesttracking.GetConfig()
			restore, err := requestid.Configure(c.c)
			if err == nil {
				restore()
				t.Fatal("Configure returned no error")
			}
			if restore != nil {
				t.Error("Configure returned a restore function beside an error")
			}
			msg := err.Error()
			var e *schemaflux.Error
			if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || e.Op != "requestid.configure" {
				t.Errorf("error %v (%T) is not an *Error of kind configuration with the operation requestid.configure", err, err)
			}
			if !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Errorf("errors.Is(%v, ErrConfiguration) is false", err)
			}
			if !strings.Contains(msg, "requestid.configure") {
				t.Errorf("error %q does not name the operation", msg)
			}
			if !strings.Contains(msg, c.field) {
				t.Errorf("error %q does not name %q", msg, c.field)
			}
			for _, passed := range []string{c.c.Header, c.c.CorrelationHeader} {
				if strings.TrimSpace(passed) != "" && strings.Contains(msg, passed) {
					t.Errorf("error repeats the value the caller passed: %.60q", msg)
				}
			}
			if len(msg) > 200 {
				t.Errorf("error is %d bytes long", len(msg))
			}
			if got := requesttracking.GetConfig(); got != before {
				t.Errorf("a refused Config changed the configuration to %+v", got)
			}
		})
	}
}

func TestARefusedConfigureLeavesTheRestoreOfAnEarlierOneWorking(t *testing.T) {
	restore := mustConfigure(t, requestid.Config{Header: "X-Good"})
	if _, err := requestid.Configure(requestid.Config{Header: "bad name"}); err == nil {
		t.Fatal("a bad Config was accepted")
	}
	if got := requesttracking.GetConfig().RequestIDHeader; got != "X-Good" {
		t.Errorf("a refused Configure changed the header to %q", got)
	}
	later := mustConfigure(t, requestid.Config{Header: "X-Later"})
	if _, err := requestid.Configure(requestid.Config{Strategy: requestid.Strategy(99)}); err == nil {
		t.Fatal("a bad Config was accepted")
	}
	later()
	if got := requesttracking.GetConfig().RequestIDHeader; got != "X-Good" {
		t.Errorf("after restoring the later install the header is %q, want X-Good", got)
	}
	restore()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("after restoring the earlier install the configuration is %+v, want the default", got)
	}
}

func TestConfigureAcceptsUnusualButValidHeaderNames(t *testing.T) {
	for _, name := range []string{"traceparent", "x-amzn-trace-id", "Request-Id", "X_Request_ID", "X.Request~Id", "R", "X-Request-ID-2", "a1!#$%&'*+^`|"} {
		restore := mustConfigure(t, requestid.Config{Header: name})
		if got := requesttracking.GetConfig().RequestIDHeader; got != name {
			t.Errorf("header %q reached the tracking code as %q", name, got)
		}
		restore()
	}
}

func TestConfigureThenRestoreWhileOtherGoroutinesReadLeavesTheDefault(t *testing.T) {
	const installers = 40
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range installers {
		wg.Go(func() {
			<-start
			restore := mustConfigure(t, requestid.Config{Header: fmt.Sprintf("X-A-%d", i), CorrelationHeader: fmt.Sprintf("X-B-%d", i)})
			restore()
		})
	}
	close(start)
	wg.Wait()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("after %d installs were all restored the configuration is %+v", installers, got)
	}
}

func TestConfigureRacingWithReadersNeverShowsAMixOfTwoConfigurations(t *testing.T) {
	// From reads only the context, so it cannot see a configuration at all.
	// Inject and Extract read the configuration once per call, so the pair of
	// header names they use is always one configuration's pair, never a request
	// name from one and a correlation name from another.
	const installers, rounds = 8, 300
	ctx := withIDs("r", "c")
	stop := make(chan struct{})
	var readers sync.WaitGroup
	failures := make(chan string, 64)
	fail := func(format string, args ...any) {
		select {
		case failures <- fmt.Sprintf(format, args...):
		default:
		}
	}
	pair := regexp.MustCompile(`^X-A-(\d+)$`)
	for range 6 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if ids := requestid.From(ctx); ids != (requestid.IDs{Request: "r", Correlation: "c"}) {
					fail("From returned %+v while the configuration changed", ids)
					return
				}
				h := map[string][]string{}
				requestid.Inject(ctx, h)
				if len(h) != 2 {
					fail("Inject wrote %v, want two headers", h)
					return
				}
				var requestName, correlationName string
				for name, values := range h {
					switch values[0] {
					case "r":
						requestName = name
					case "c":
						correlationName = name
					}
				}
				if requestName == "X-Request-Id" {
					if correlationName != "X-Correlation-Id" {
						fail("request name is the default but correlation name is %q", correlationName)
						return
					}
				} else if m := pair.FindStringSubmatch(requestName); m == nil || correlationName != "X-B-"+m[1] {
					fail("request name %q beside correlation name %q", requestName, correlationName)
					return
				}
				got := requestid.From(requestid.Extract(context.Background(), h))
				if got.Request != "" && got.Request != "r" {
					fail("Extract returned %+v", got)
					return
				}
			}
		})
	}
	var writers sync.WaitGroup
	for i := range installers {
		writers.Go(func() {
			for range rounds {
				restore := mustConfigure(t, requestid.Config{Header: fmt.Sprintf("X-A-%d", i), CorrelationHeader: fmt.Sprintf("X-B-%d", i)})
				restore()
			}
		})
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	close(failures)
	for f := range failures {
		t.Error(f)
	}
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("after the race the configuration is %+v, want the default", got)
	}
}
