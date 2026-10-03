package ops

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A field a caller tags `json:"-"` has been declared as not serialised, and
// what is not serialised is not sent to a provider: not its value and not its
// name. Most operations send the caller's data through encoding/json and so
// leave it out by construction. These are the ones that render it by their own
// code, a walk over the fields (CompleteField's context, Diff's list of
// changes) or a fallback to fmt for a value JSON cannot write, which prints
// every field of a struct, tagged or not. Each is given a value with a canary in
// a field tagged out of JSON and the request it sends is searched for it.

const (
	unsentValue    = "CANARY-VALUE-5c1e9a"
	unsentOldValue = "CANARY-OLD-VALUE-77d2"
	unsentName     = "Passphrase"
	unsentVaultKey = "Vault"
)

// unsentSecret is the part of a struct that is left out of JSON.
type unsentSecret struct {
	Visible string `json:"visible"`
	Vault   string `json:"-"`
}

// unsentRecord has a canary in a field tagged out of JSON, one inside an
// embedded struct, and one inside a struct held by a field.
type unsentRecord struct {
	Body       string `json:"body"`
	Note       string `json:"note"`
	Passphrase string `json:"-"`
	unsentSecret
	Detail unsentSecret `json:"detail"`
}

func newUnsentRecord(passphrase string) unsentRecord {
	return unsentRecord{
		Body: "Once upon", Note: "a note", Passphrase: passphrase,
		unsentSecret: unsentSecret{Visible: "promoted", Vault: unsentValue},
		Detail:       unsentSecret{Visible: "nested", Vault: unsentValue},
	}
}

// unsentNaN is a struct JSON cannot write, because of the number, with the
// canary in a field JSON leaves out.
type unsentNaN struct {
	Ratio      float64 `json:"ratio"`
	Passphrase string  `json:"-"`
	hidden     string
}

func newUnsentNaN() unsentNaN {
	return unsentNaN{Ratio: math.NaN(), Passphrase: unsentValue, hidden: unsentValue}
}

func TestAFieldTaggedOutOfJSONIsNotSentToAProvider(t *testing.T) {
	cases := []struct {
		name string
		call func() error
		// calls is how many requests were to be made; a value JSON cannot write
		// is refused before one.
		calls int
		// refused is whether the call is an invalid request.
		refused bool
		// shown is text the request must still carry, so that a request that
		// carried nothing at all does not pass.
		shown []string
	}{
		{name: "CompleteField", calls: 1, shown: []string{"note: a note", "visible: promoted", `detail: {\"visible\":\"nested\"}`}, call: func() error {
			_, err := CompleteField(context.Background(), newUnsentRecord(unsentValue), NewCompleteFieldOptions("Body"))
			return err
		}},
		{name: "CompleteField given a pointer", calls: 1, shown: []string{"note: a note"}, call: func() error {
			record := newUnsentRecord(unsentValue)
			_, err := CompleteField(context.Background(), &record, NewCompleteFieldOptions("Body"))
			return err
		}},
		{name: "CompleteField of a promoted field", calls: 1, shown: []string{"note: a note"}, call: func() error {
			_, err := CompleteField(context.Background(), newUnsentRecord(unsentValue), NewCompleteFieldOptions("Visible"))
			return err
		}},
		{name: "Diff of a value that changed beside the secret", calls: 1, shown: []string{"Note"}, call: func() error {
			before, after := newUnsentRecord(unsentOldValue), newUnsentRecord(unsentValue)
			after.Note = "another note"
			result, err := Diff(before, after, NewDiffOptions().WithIntelligence(types.Smart))
			if err == nil && result.SummaryError != nil {
				err = result.SummaryError
			}
			return err
		}},
		{name: "Diff of a value whose only change is the secret", calls: 0, call: func() error {
			result, err := Diff(newUnsentRecord(unsentOldValue), newUnsentRecord(unsentValue), NewDiffOptions().WithIntelligence(types.Smart))
			if err == nil && len(result.Modified) != 1 {
				t.Errorf("the local comparison lost the change: %+v", result)
			}
			return err
		}},
		{name: "Format", calls: 0, refused: true, call: func() error { _, err := Format(newUnsentNaN(), "a table"); return err }},
		{name: "FormatWithMetadata", calls: 0, refused: true, call: func() error {
			_, err := FormatWithMetadata(newUnsentNaN(), "a table")
			return err
		}},
		{name: "QuestionLegacy", calls: 0, refused: true, call: func() error {
			_, err := QuestionLegacy(newUnsentNaN(), "what is it?")
			return err
		}},
		{name: "Extract", calls: 0, refused: true, call: func() error {
			_, err := Extract[coreTarget](newUnsentNaN(), NewExtractOptions())
			return err
		}},
		{name: "Annotate", calls: 0, refused: true, call: func() error {
			_, err := Annotate(newUnsentNaN(), NewAnnotateOptions())
			return err
		}},
		{name: "Critique", calls: 0, refused: true, call: func() error {
			_, err := Critique(newUnsentNaN(), NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			return err
		}},
		{name: "Compress", calls: 0, refused: true, call: func() error {
			_, err := Compress(newUnsentNaN(), NewCompressOptions())
			return err
		}},
		{name: "Parse", calls: 0, refused: true, call: func() error {
			_, err := Parse[coreTarget](newUnsentNaN(), NewParseOptions())
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := coreAnswer(t, "more words")
			err := tc.call()
			if tc.refused && types.KindOf(err) != types.KindInvalidRequest {
				t.Errorf("the error is %v, want an invalid request", err)
			}
			if got := model.count(); got != tc.calls {
				t.Errorf("%d requests were made, want %d", got, tc.calls)
			}
			for _, call := range model.calls {
				sent := call.system + "\n" + call.user
				for _, banned := range []string{unsentValue, unsentOldValue, unsentName, unsentVaultKey} {
					if strings.Contains(sent, banned) {
						t.Errorf("the request carries %q:\n%s", banned, sent)
					}
				}
				for _, wanted := range tc.shown {
					if !strings.Contains(sent, wanted) {
						t.Errorf("the request lacks %q:\n%s", wanted, sent)
					}
				}
			}
		})
	}
}
