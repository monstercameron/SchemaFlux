package schemaflux

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// limitsInvoice is what the edge tests of the numeric settings extract.
type limitsInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

// limitsModel is priced, so that a client held to a budget can send to it.
const limitsModel = "limits-edge-model"

func init() { registerPriceForTest(limitsModel, 1, 1) }

// limitsField is one numeric setting of a client or of its options: how to set
// it to a value, and the field's name as the configuration error names it.
type limitsField struct {
	name  string
	set   func(c *Client, o *Options, v int64)
	field string

	// negativeIsAnError is true for a setting whose negative value cannot be
	// used. A negative Retries or Repairs means never, and is a setting.
	negativeIsAnError bool
}

var limitsFields = []limitsField{
	{"Timeout", func(c *Client, _ *Options, v int64) { c.Timeout = time.Duration(v) }, "Client.Timeout", true},
	{"Retries", func(c *Client, _ *Options, v int64) { c.Retries = int(v) }, "Client.Retries", false},
	{"Repairs", func(c *Client, _ *Options, v int64) { c.Repairs = int(v) }, "Client.Repairs", false},
	{"MaxOutputTokens", func(_ *Client, o *Options, v int64) { o.MaxOutputTokens = int(v) }, "MaxOutputTokens", true},
	{"MaxOutputTokens under a scheduler that bounds tokens", func(c *Client, o *Options, v int64) {
		c.Scheduler = &Scheduler{MaxTokens: math.MaxInt64}
		o.MaxOutputTokens = int(v)
	}, "MaxOutputTokens", true},
	{"MaxOutputTokens under a scheduler that bounds cost", func(c *Client, o *Options, v int64) {
		c.Scheduler = &Scheduler{MaxCost: 1e15}
		o.MaxOutputTokens = int(v)
	}, "MaxOutputTokens", true},
	{"Scheduler.MaxConcurrent", func(c *Client, _ *Options, v int64) { c.Scheduler = &Scheduler{MaxConcurrent: int(v)} }, "Scheduler.MaxConcurrent", true},
	{"Scheduler.MaxQueued", func(c *Client, _ *Options, v int64) { c.Scheduler = &Scheduler{MaxQueued: int(v)} }, "Scheduler.MaxQueued", true},
	{"Scheduler.MaxTokens", func(c *Client, _ *Options, v int64) { c.Scheduler = &Scheduler{MaxTokens: v} }, "Scheduler.MaxTokens", true},
	{"Scheduler.MaxPerProvider", func(c *Client, _ *Options, v int64) { c.Scheduler = &Scheduler{MaxPerProvider: int(v)} }, "Scheduler.MaxPerProvider", true},
	{"DataPolicy.MaxRetentionDays", func(c *Client, _ *Options, v int64) {
		c.Policy = &DataPolicy{Providers: []string{"limits-edge"}, Regions: []string{"eu"}, MaxRetentionDays: int(v)}
	}, "DataPolicy.MaxRetentionDays", true},
	{"Recorder.MaxBody", func(c *Client, _ *Options, v int64) { c.Recorder = &Recorder{Bodies: true, MaxBody: int(v)} }, "Recorder.MaxBody", true},
}

// limitsEdges are the values a numeric setting is tried at.
var limitsEdges = []struct {
	name  string
	value int64
}{
	{"MaxInt", math.MaxInt64},
	{"MaxInt minus one", math.MaxInt64 - 1},
	{"MaxInt32 plus one", math.MaxInt32 + 1},
	{"two to the 40th", 1 << 40},
	{"two to the 16th plus one", 1<<16 + 1},
	{"one", 1},
	{"minus one", -1},
	{"MinInt plus one", math.MinInt64 + 1},
	{"MinInt", math.MinInt64},
}

// TestEveryNumericSettingTakesItsEdges sets each integer and duration setting of
// a client and of the options to the edges of its range, and makes a call that
// the provider would answer. The call does not panic and does not hang, and it
// either fails with a configuration error that names the field and does not
// print the value, or it is sent once and returns the answer: a value that
// overflowed an internal sum used to send nothing and report an answer that did
// not parse.
func TestEveryNumericSettingTakesItsEdges(t *testing.T) {
	declareRouteForTest("limits-edge", limitsModel, []string{"eu"}, 0)
	for _, field := range limitsFields {
		for _, edge := range limitsEdges {
			t.Run(field.name+"/"+edge.name, func(t *testing.T) {
				model := &answerer{name: "limits-edge", answer: func(int, Request) (Response, error) {
					return Response{Content: `{"number":"A","total":1}`, Model: limitsModel, FinishReason: "stop",
						Usage: Usage{Input: 3, Output: 2, Total: 5}}, nil
				}}
				client := Client{Provider: model, Model: limitsModel}
				var opts Options
				field.set(&client, &opts, edge.value)
				if client.Scheduler != nil {
					defer client.Scheduler.Close(context.Background())
				}

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				done := make(chan struct{})
				var got limitsInvoice
				var err error
				var panicked any
				go func() {
					defer close(done)
					defer func() { panicked = recover() }()
					got, err = Extract[limitsInvoice](WithClient(ctx, client), "invoice", ExtractOptions{Options: opts})
				}()
				select {
				case <-done:
				case <-time.After(25 * time.Second):
					t.Fatal("the call did not return")
				}
				if panicked != nil {
					t.Fatalf("the call panicked: %v", panicked)
				}

				if err != nil {
					var e *Error
					if errors.As(err, &e) && e.Kind == ErrBusy && field.name == "Scheduler.MaxTokens" && edge.value == 1 {
						// One token in flight is a ceiling the request alone is over: the
						// scheduler refuses it as busy, which is what the setting says.
						return
					}
					if !errors.As(err, &e) || e.Kind != ErrConfiguration {
						t.Fatalf("the call failed with %v, want a configuration error or the answer", err)
					}
					if !strings.Contains(e.Message, field.field) {
						t.Errorf("the configuration error %q does not name %s", e.Message, field.field)
					}
					for _, digits := range []string{"9223372036854775807", "9223372036854775808", "1099511627776", "2147483648"} {
						if strings.Contains(err.Error(), digits) {
							t.Errorf("the configuration error prints the value: %q", err.Error())
						}
					}
					if model.count() != 0 {
						t.Errorf("a refused setting still sent %d requests", model.count())
					}
					if edge.value > 0 {
						t.Errorf("a positive %s of %s was refused, and it is one a caller may mean: %v", field.name, edge.name, err)
					}
					return
				}
				if edge.value < 0 && field.negativeIsAnError {
					t.Errorf("a negative %s was accepted", field.name)
				}
				if got != (limitsInvoice{Number: "A", Total: 1}) {
					t.Errorf("got %+v, want the answer", got)
				}
				if model.count() != 1 {
					t.Errorf("%d requests were sent, want 1", model.count())
				}
			})
		}
	}
}

// TestABudgetLimitAtTheEdgesOfAFloat holds the one floating setting of a client
// to the same rule: no value changes what a call returns except by being
// refused for what it is.
func TestABudgetLimitAtTheEdgesOfAFloat(t *testing.T) {
	for _, limit := range []float64{math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.Inf(-1), math.NaN(), -math.MaxFloat64, 1e308} {
		model := &answerer{name: "limits-edge", answer: func(int, Request) (Response, error) {
			return Response{Content: `{"number":"A","total":1}`, Model: limitsModel, FinishReason: "stop", Usage: Usage{Input: 3, Output: 2, Total: 5}}, nil
		}}
		client := Client{Provider: model, Model: limitsModel, Budget: &Budget{Limit: limit}, Scheduler: &Scheduler{MaxCost: limit}}
		_, err := Extract[limitsInvoice](WithClient(context.Background(), client), "invoice")
		_ = client.Scheduler.Close(context.Background())
		bad := math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0
		switch {
		case bad && kindOfErr(err) != ErrConfiguration:
			t.Errorf("limit %v: got %v, want a configuration error", limit, err)
		case !bad && limit != math.SmallestNonzeroFloat64 && err != nil:
			t.Errorf("limit %v: %v", limit, err)
		}
	}
}

// kindOfErr is the kind of err when it is an *Error, and zero otherwise.
func kindOfErr(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// hugeRetrier is a provider whose own retry settings are at the top of the
// integers, which is what a provider that means "as often as it takes" writes.
type hugeRetrier struct{ *answerer }

func (hugeRetrier) RetryPolicy() (int, time.Duration) { return math.MaxInt, time.Millisecond }

func TestAProvidersOwnRetryCountAtTheTopOfTheIntegersStillSendsTheRequest(t *testing.T) {
	t.Parallel()
	model := hugeRetrier{&answerer{name: "limits-edge", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"number":"A","total":1}`, Model: limitsModel, FinishReason: "stop"}, nil
	}}}
	got, err := Extract[limitsInvoice](WithClient(context.Background(), Client{Provider: model, Model: limitsModel}), "invoice")
	if err != nil || got.Number != "A" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if model.count() != 1 {
		t.Errorf("%d requests were sent, want 1", model.count())
	}
}
