package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// earlyRefusal is one way an operation turns a call away before it asks a model:
// an option it cannot use, an input it cannot read, a combination it rejects.
type earlyRefusal struct {
	name string
	run  func() error
	// text is the message the refusal has always had. It is written out in full
	// from the message before the refusal was given a kind, so a change to the
	// words fails the table.
	text string
	kind types.ErrorKind
	// matches are the targets errors.Is matched before the refusal had a kind
	// and must still match, with the sentinel of its kind beside them.
	matches []error
	// chain, when set, says whether the refusal still reaches the error it
	// wrapped: a decoder's failure, say, which is a type and not a sentinel.
	chain func(error) bool
}

// checkEarlyRefusals runs every refusal and requires of each the same four
// things: it is an error, its text is unchanged, types.KindOf reports the kind,
// and errors.Is reaches every target it should. No refusal may reach a model.
func checkEarlyRefusals(t *testing.T, cases []earlyRefusal) {
	t.Helper()
	swapLLMCaller(t, func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		t.Error("a refused call reached the model")
		return "", errors.New("the model was not to be asked")
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("the call was not refused")
			}
			if got := err.Error(); got != tc.text {
				t.Errorf("the text changed:\n got  %q\n want %q", got, tc.text)
			}
			if got := types.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v", got, tc.kind)
			}
			if tc.chain != nil && !tc.chain(err) {
				t.Error("the refusal no longer reaches the error it wrapped")
			}
			for _, target := range tc.matches {
				if !errors.Is(err, target) {
					t.Errorf("errors.Is(err, %v) is false", target)
				}
			}
		})
	}
}
