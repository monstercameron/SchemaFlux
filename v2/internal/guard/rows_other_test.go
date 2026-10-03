package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/chat"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/redact"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// invoice is what the descriptors of the op rows read an answer into.
type invoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

// invoiceDescriptor reads one input, a text, as an invoice. It has no name, so
// its Record is named "op.run".
func invoiceDescriptor() op.Descriptor[string, invoice] {
	return op.Descriptor[string, invoice]{
		BuildPrompt: func(text string, _ op.Options) (string, string) {
			return "Read the invoice. Return a JSON object with number and total.", text
		},
		Contract: op.Contract[invoice]{
			SchemaName: "invoice",
			Decode: func(body string) (invoice, error) {
				var v invoice
				if err := json.Unmarshal([]byte(body), &v); err != nil {
					return invoice{}, errors.New("the answer is not a JSON object with number and total")
				}
				if v.Number == "" {
					return invoice{}, errors.New("the answer has no number")
				}
				return v, nil
			},
		},
	}
}

// batchDescriptor is invoiceDescriptor with a batch on the id protocol.
func batchDescriptor() op.Descriptor[string, invoice] {
	d := invoiceDescriptor()
	d.Batch = op.Batch[string, invoice]{
		Class:        op.BatchItemwise,
		Kind:         op.AlgebraIndependent,
		Instructions: "Read each invoice. Answer with the id of each and an object with number and total.",
		Item: func(raw json.RawMessage) (invoice, error) {
			var v invoice
			if err := json.Unmarshal(raw, &v); err != nil {
				return invoice{}, errors.New("not an invoice")
			}
			return v, nil
		},
	}
	return d
}

// invoiceAnswering answers a request of one item with an invoice numbered by the
// item, and a batched request of the id protocol with one for every item.
func invoiceAnswering(m *schemafluxtest.Provider, _ in, _ bool) {
	m.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		body := fmt.Sprintf(`{"number":%s,"total":1}`, quote(req.User))
		if _, payload, ok := strings.Cut(req.User, "Process these items:\n"); ok {
			var tagged []struct {
				ID   string `json:"id"`
				Item string `json:"item"`
			}
			if err := json.Unmarshal([]byte(payload), &tagged); err != nil {
				body = "{}"
			} else {
				var parts []string
				for _, t := range tagged {
					parts = append(parts, fmt.Sprintf(`{"id":%q,"output":{"number":%s,"total":1}}`, t.ID, quote(t.Item)))
				}
				body = `{"items":[` + strings.Join(parts, ",") + `]}`
			}
		}
		return schemaflux.Response{Content: body, Model: req.Model, FinishReason: "stop"}, nil
	})
}

// stringDescriptor is a descriptor of a string to a string, which is what a stage
// of op.RunStages is. It has no name.
func stringDescriptor() op.Descriptor[string, string] {
	return op.Descriptor[string, string]{
		BuildPrompt: func(text string, _ op.Options) (string, string) {
			return "Tidy the text. Return a JSON object with text.", text
		},
		Contract: op.Contract[string]{
			SchemaName: "tidy",
			Decode: func(body string) (string, error) {
				var v struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal([]byte(body), &v); err != nil || v.Text == "" {
					return "", errors.New("the answer has no text")
				}
				return v.Text, nil
			},
		},
	}
}

// oneItemFailure returns the first item of a RunEach that failed as the call's own
// error, so that a row can be read like every other: the items carry their
// failures, and a call that returns nil has no error of its own.
func firstItemError[Out any](items []op.Item[Out], err error) (any, error) {
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if _, itemErr := item.Value(); itemErr != nil {
			return nil, itemErr
		}
	}
	return items, nil
}

// otherRows are the functions of the subpackages that ask a model.
func otherRows() []row {
	rows := []row{
		{pkg: "exp", name: "Synthesize", op: "exp.synthesize", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"synthesized":{"body":"{{c}}","n":1},"facts":[],"insights":[],"conflicts":[],"source_coverage":{}}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Synthesize[note](ctx, []any{x.Text, map[string]string{x.Key: x.Text}}, exp.SynthesizeOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Arbitrate", op: "exp.arbitrate", level: schemaflux.ContractSchemaAndInvariantChecked, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, withCanary bool) {
				fill := "plain"
				if withCanary {
					fill = canary
				}
				m.Reply(`{"options":{"o1":{"r1":{"passed":true,"score":0.8,"reasoning":"good"}},"o2":{"r1":{"passed":true,"score":0.6,"reasoning":"less good ` + fill + `"}}},"reasoning":"compared","confidence":0.7}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Arbitrate(ctx, []string{x.Text, "second"}, []string{x.Label}, exp.ArbitrateOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Assemble", op: "exp.assemble", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"composed":{"body":"{{c}}","n":1},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":0.8}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Assemble[note](ctx, []any{x.Text, map[string]string{x.Key: x.Text}}, exp.AssembleOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Conform", op: "exp.conform", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, _ bool) {
				m.Reply(`{"conformed":{"body":"conformed","n":1},"adjustments":[],"violations":[],"compliance":1,"standard":` + quote(x.Label) + `}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Conform(ctx, note{Body: x.Text, N: 1}, x.Label, exp.ConformOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Decompose", op: "exp.decompose", level: schemaflux.ContractSchemaAndInvariantChecked, path: "recorded",
			answer: `{"parts":[{"id":"root","name":"Root","content":{"body":"{{c}}","n":1},"depth":1,"order":0}],"root_parts":["root"]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Decompose(ctx, note{Body: x.Text, N: 1}, exp.DecomposeOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "DecomposeToSlice", op: "exp.decomposetoslice", level: schemaflux.ContractJSONWellFormed, path: "recorded", answer: `[1,2]`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.DecomposeToSlice[int](ctx, x.Text, exp.DecomposeToSliceOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Derive", op: "exp.derive", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"derived":{"body":"{{c}}","n":1},"derivations":[],"field_confidence":{"body":0.9},"overall_confidence":0.9}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Derive[note](ctx, x.Text, exp.DeriveOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Enrich", op: "exp.enrich", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"enriched":{"body":"{{c}}","n":1},"added_fields":["n"],"confidence":{},"derivations":{"n":"from the text"}}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Enrich[note](ctx, x.Text, exp.EnrichOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "EnrichInPlace", op: "exp.enrichinplace", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, _ bool) {
				m.Reply(`{"body":` + quote(x.Text) + `,"n":2}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.EnrichInPlace(ctx, note{Body: x.Text}, exp.EnrichOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Interpolate", op: "exp.interpolate", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, _ bool) {
				m.Reply(`{"complete":[` + quote(x.Text) + `,"filled","third"],"filled":[{"index":1,"method":"linear","based_on":[0,2],"reasoning":"between","confidence":0.8}],"gap_count":1,"method":"linear","average_confidence":0.8}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Interpolate(ctx, []string{x.Text, "", "third"}, exp.InterpolateOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Project", op: "exp.project", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"projected":{"body":"{{c}}","n":1},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Project[string, note](ctx, x.Text, exp.ProjectOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Resolve", op: "exp.resolve", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"resolved":"merged {{c}}","conflicts":[],"source_contributions":{"0":["name"],"1":[]},"confidence":0.75}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Resolve(ctx, []string{x.Text, "second"}, exp.ResolveOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "Settle", op: "exp.settle", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"solution":{"body":"{{c}}","n":1},"satisfaction":{"time":0.8},"overall_satisfaction":0.8,"tradeoffs":[],"alternatives":[],"reasoning":"balanced","confidence":0.9}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.Settle[note](ctx, map[string]any{x.Key: x.Text}, exp.SettleOptions{Options: x.opts()}))
			}},
		{pkg: "exp", name: "SettleAdversarial", op: "exp.settleadversarial", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"deal":{"body":"{{c}}","n":1},"deal_reached":true,"term_movements":[],"who_conceded_more":"neither","our_satisfaction":0.8,"their_satisfaction":0.7,"reasoning":"balanced","confidence":0.9}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(exp.SettleAdversarial(ctx, exp.AdversarialContext[note]{
					Ours:   exp.AdversarialPosition[note]{Position: note{Body: x.Text, N: 1}, MustHaves: []string{x.Label}},
					Theirs: exp.AdversarialPosition[note]{Position: note{Body: "theirs", N: 2}},
				}, exp.SettleAdversarialOptions{Options: x.opts()}))
			}},
		{pkg: "chat", name: "Session.Send", op: "chat.send", level: schemaflux.ContractPromptOnly, path: "recorded", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				s := chat.Session{Instructions: x.Steer, MaxOutputTokens: x.Tokens}
				return ret(s.Send(ctx, x.Text))
			}},
		{pkg: "chat", name: "Session.SendResults", op: "chat.sendresults", level: schemaflux.ContractPromptOnly, path: "recorded", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				s := chat.Session{Instructions: x.Steer, MaxOutputTokens: x.Tokens,
					Tools: []schemaflux.Tool{{Name: "lookup", Description: "looks a thing up", Parameters: map[string]any{"type": "object"}}},
					History: []schemaflux.Message{{Role: "user", Content: x.Text},
						{Role: "assistant", ToolCalls: []schemaflux.ToolCall{{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}}}}
				return ret(s.SendResults(ctx, map[string]string{"c1": x.Text}))
			}},
		{pkg: "redact", name: "Text", op: "redact.text", level: schemaflux.ContractSchemaAndInvariantChecked, path: "twin",
			script: func(m *schemafluxtest.Provider, x in, _ bool) {
				t := quote(x.Text)
				m.Reply(`{"text":` + t + `,"original":` + t + `,"spans":[],"categories":{},"metadata":{"spans_from_detectors":0,"left_inside_longer_words":0}}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(redact.Text(ctx, x.Text, redact.TextOptions{Options: x.opts()}))
			}},
		{pkg: "flow", name: "Decide", op: "flow.decide", level: schemaflux.ContractSchemaAndInvariantChecked, floorAt: schemaflux.ContractSchemaConstrained, path: "recorded",
			answer: `{"selected":1,"explanation":"other","confidence":0.8,"alternatives":[]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(flow.Decide(ctx, x.Text, []flow.Decision[string]{{Value: "a", Description: x.Label}, {Value: "b", Description: "second"}}, x.opts()))
			}},
		{pkg: "flow", name: "Match", op: "flow.match", level: schemaflux.ContractPromptOnly, path: "recorded", answer: `true`, na: merged(textual, no("unusable option", "Match takes no options")),
			call: func(ctx context.Context, x in) (any, error) {
				return ret(flow.Match(ctx, x.Text, flow.When(x.Label, func() {}), flow.Otherwise(func() {})))
			}},
		{pkg: "op", name: "Run", op: "op.run", level: schemaflux.ContractJSONWellFormed, computed: true, path: "twin", script: invoiceAnswering,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(op.Run(ctx, invoiceDescriptor(), x.Text, op.Options{Options: x.opts()}))
			}},
		{pkg: "op", name: "RunBatch", op: "op.runbatch", level: schemaflux.ContractJSONWellFormed, computed: true, path: "twin", script: invoiceAnswering,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(op.RunBatch(ctx, batchDescriptor(), []string{x.Text, "second"}, op.Options{Options: x.opts()}))
			}},
		{pkg: "op", name: "RunEach", op: "op.runeach", level: schemaflux.ContractJSONWellFormed, computed: true, path: "twin", script: invoiceAnswering,
			na: eachReportsPerItem,
			call: func(ctx context.Context, x in) (any, error) {
				return firstItemError(op.RunEach(ctx, invoiceDescriptor(), []string{x.Text, "second"}, op.PlanOptions{Options: op.Options{Options: x.opts()}}))
			}},
		{pkg: "op", name: "RunStages", op: "op.runstages", level: schemaflux.ContractJSONWellFormed, computed: true, path: "twin",
			answer: `{"text":"tidy {{c}}"}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(op.RunStages(ctx, x.Text, []op.Stage[string]{{Descriptor: stringDescriptor()}}, op.Options{Options: x.opts()}))
			}},
		{pkg: "op", name: "RunMany", op: "op.runmany", level: schemaflux.ContractJSONWellFormed, computed: true, path: "twin", script: invoiceAnswering,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(op.RunMany(ctx, invoiceDescriptor(), []string{x.Text, "second"}, op.PlanOptions{Options: op.Options{Options: x.opts()}}))
			}},
	}
	return rows
}

// eachReportsPerItem exempts op.RunEach from the scenarios in which a request
// fails: it reports a failure on the items and returns a nil error for the call
// (ruling R29 Q12), which the row's call turns into an error only for
// the guards of what is leaked.
var eachReportsPerItem = func() map[string]string {
	const why = "RunEach reports a failed request on the items and returns a nil error (R29 Q12)"
	out := map[string]string{}
	for _, name := range []string{"provider unavailable", "provider rate limited", "provider unauthorized", "provider timeout",
		"provider too large", "provider unsupported", "provider bare kind", "provider raw error", "malformed answer", "empty answer",
		"refusal", "cancelled context", "deadline passed"} {
		out[name] = why
	}
	return out
}()

// allRows are the rows of every package.
func allRows() []row {
	return append(rootRows(), otherRows()...)
}
