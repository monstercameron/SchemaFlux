package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type protocolAnswer struct {
	Label string `json:"label"`
}

// protocolCodec decodes an item's output into a struct, the way a caller's codec
// would. json.Unmarshal of null into a struct is not an error, which is the
// case these tests are about.
func protocolCodec() BatchItemCodec[string, protocolAnswer] {
	return BatchItemCodec[string, protocolAnswer]{
		DecodeItem: func(raw json.RawMessage) (protocolAnswer, error) {
			var answer protocolAnswer
			err := json.Unmarshal(raw, &answer)
			return answer, err
		},
	}
}

func protocolMerge(t *testing.T, body string, items int) ([]protocolAnswer, error) {
	t.Helper()
	algebra := NewIDBatchAlgebra[string, protocolAnswer](types.BatchItemwise, types.AlgebraIndependent, "system", protocolCodec())
	return algebra.Merge(body, make([]string, items))
}

func TestIDBatchMergeRefusesAnItemAnsweredWithNoOutput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"a null output", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"i-000002","output":null}]}`},
		{"the first item null", `{"items":[{"id":"i-000001","output":null},{"id":"i-000002","output":{"label":"b"}}]}`},
		{"both null", `{"items":[{"id":"i-000001","output":null},{"id":"i-000002","output":null}]}`},
		{"no output key", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"i-000002"}]}`},
		{"a null with surrounding space", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"i-000002","output":  null  }]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs, err := protocolMerge(t, tc.body, 2)
			if err == nil {
				t.Fatalf("an item with no output was accepted: %+v", outputs)
			}
			if outputs != nil {
				t.Errorf("outputs came back beside the error: %+v", outputs)
			}
			if types.KindOf(err) != types.KindBatchProtocolViolation {
				t.Errorf("kind = %v, want KindBatchProtocolViolation: %v", types.KindOf(err), err)
			}
		})
	}
}

// Merge is the path that must not return a partial batch, so it refuses the whole
// answer when any element is bad, as it always has: an element that leaves out its
// id or its output is a batch-protocol violation, one that is not an object is
// refused as it could not be read, and a response without a list of items is
// refused before any element is looked at. Nothing the model wrote is in a message.
func TestIDBatchMergeRefusesTheWholeAnswerWhenAnyElementIsBad(t *testing.T) {
	cases := []struct {
		name, body string
		kind       types.ErrorKind
		says       string
	}{
		{"no output key", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"i-000002"}]}`, types.KindBatchProtocolViolation, "item 2 was answered with no output (output is missing)"},
		{"no id key", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"output":{"label":"ZZMARKER"}}]}`, types.KindBatchProtocolViolation, "did not cover its items exactly"},
		{"an id that is null", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":null,"output":{"label":"ZZMARKER"}}]}`, types.KindBatchProtocolViolation, "did not cover its items exactly"},
		{"a null element", `{"items":[{"id":"i-000001","output":{"label":"a"}},null]}`, types.KindBatchProtocolViolation, "did not cover its items exactly"},
		{"a duplicate id", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"i-000001","output":{"label":"b"}}]}`, types.KindBatchProtocolViolation, "did not cover its items exactly"},
		{"an id nobody offered", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"ZZMARKER","output":{"label":"b"}}]}`, types.KindBatchProtocolViolation, "did not cover its items exactly"},
		{"an element that is not an object", `{"items":[{"id":"i-000001","output":{"label":"a"}},"ZZMARKER"]}`, types.KindMalformedOutput, "element 1"},
		{"an id that is a number", `{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":1,"output":{"label":"ZZMARKER"}}]}`, types.KindMalformedOutput, "element 1"},
		{"no items key", `{"other":[{"id":"i-000001","output":{"label":"ZZMARKER"}}]}`, types.KindSchemaViolation, "items is missing"},
		{"items that is null", `{"items":null}`, types.KindSchemaViolation, "items is null"},
		{"an empty object", `{}`, types.KindSchemaViolation, "items is missing"},
		{"an array", `[{"id":"i-000001","output":{"label":"ZZMARKER"}}]`, types.KindMalformedOutput, "an object was asked for"},
		{"null", `null`, types.KindMalformedOutput, "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs, err := protocolMerge(t, tc.body, 2)
			if err == nil {
				t.Fatalf("accepted: %+v", outputs)
			}
			if outputs != nil {
				t.Errorf("outputs came back beside the error: %+v", outputs)
			}
			if types.KindOf(err) != tc.kind {
				t.Errorf("kind = %v, want %v: %v", types.KindOf(err), tc.kind, err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
			if strings.Contains(err.Error(), "ZZMARKER") {
				t.Errorf("the refusal repeats what the model wrote: %v", err)
			}
		})
	}
}

func TestIDBatchMergeStillAcceptsAnEmptyObjectAndFullCoverage(t *testing.T) {
	outputs, err := protocolMerge(t, `{"items":[{"id":"i-000002","output":{"label":"b"}},{"id":"i-000001","output":{}}]}`, 2)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(outputs) != 2 || outputs[0].Label != "" || outputs[1].Label != "b" {
		t.Errorf("outputs = %+v, want them in item order", outputs)
	}
}

func TestIDBatchMergeCoverageErrorsAreClassifiedAndCarryNoIDs(t *testing.T) {
	for _, body := range []string{
		`{"items":[{"id":"i-000001","output":{"label":"a"}}]}`,
		`{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"i-000001","output":{"label":"a"}}]}`,
		`{"items":[{"id":"i-000001","output":{"label":"a"}},{"id":"secret-order-4417","output":{"label":"b"}}]}`,
	} {
		_, err := protocolMerge(t, body, 2)
		if err == nil {
			t.Fatalf("%s: accepted", body)
		}
		if types.KindOf(err) != types.KindBatchProtocolViolation {
			t.Errorf("%s: kind = %v", body, types.KindOf(err))
		}
		if strings.Contains(err.Error(), "secret-order-4417") {
			t.Errorf("the error carries an id the model invented: %v", err)
		}
	}
}

func TestClassifyBatchIDsOrdersAndCounts(t *testing.T) {
	cases := []struct {
		name                       string
		n                          int
		answered                   []string
		missing, duplicate, unknwn int
	}{
		{"exact", 3, []string{"i-000003", "i-000001", "i-000002"}, 0, 0, 0},
		{"one missing", 3, []string{"i-000001", "i-000003"}, 1, 0, 0},
		{"one duplicated", 3, []string{"i-000001", "i-000001", "i-000002", "i-000003"}, 0, 1, 0},
		{"one invented", 2, []string{"i-000001", "i-000002", "zzz"}, 0, 0, 1},
		{"an invented id twice counts once", 2, []string{"i-000001", "i-000002", "zzz", "zzz"}, 0, 0, 1},
		{"nothing answered", 2, nil, 2, 0, 0},
		{"nothing offered or answered", 0, nil, 0, 0, 0},
		{"everything wrong", 2, []string{"a", "b", "i-000001", "i-000001"}, 1, 1, 2},
		{"a domain id", 1, []string{"42"}, 1, 0, 1},
		{"a repeated id three times counts once", 1, []string{"i-000001", "i-000001", "i-000001"}, 0, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coverage := classifyBatchIDs(tc.n, tc.answered)
			if len(coverage.Missing) != tc.missing || len(coverage.Duplicate) != tc.duplicate || len(coverage.Unknown) != tc.unknwn {
				t.Errorf("coverage = %+v", coverage)
			}
			if coverage.OK() != (tc.missing+tc.duplicate+tc.unknwn == 0) {
				t.Errorf("OK() = %v for %+v", coverage.OK(), coverage)
			}
			if coverage.Offered != tc.n || coverage.Answered != len(tc.answered) {
				t.Errorf("Offered/Answered = %d/%d", coverage.Offered, coverage.Answered)
			}
		})
	}
}

// answerWithOfferedIDs rewrites a scripted answer written with the canonical ids
// ("i-000001", "i-000002", ...) to the ids the request offered, in order, so that a
// fixed script can answer a request whose ids are random and new for every send.
func answerWithOfferedIDs(user, body string) string {
	payload, found := strings.CutPrefix(user, batchPromptPrefix)
	if !found {
		return body
	}
	var tagged []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(payload), &tagged) != nil {
		return body
	}
	for place, element := range tagged {
		body = strings.ReplaceAll(body, fmt.Sprintf("%q", itemID(place)), fmt.Sprintf("%q", element.ID))
	}
	return body
}

// offeringIDsProvider answers as the provider it wraps does, with a scripted batch
// answer's ids rewritten to the ids the request offered.
type offeringIDsProvider struct{ llm.Provider }

func (p offeringIDsProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	response, err := p.Provider.Complete(ctx, req)
	response.Content = answerWithOfferedIDs(req.UserPrompt, response.Content)
	return response, err
}
