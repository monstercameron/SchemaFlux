package parity

import (
	"context"
	"reflect"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/redact"
)

const (
	redactParityText   = "Ada Lovelace wrote ada@example.com or 555-123-4567"
	redactParityAnswer = `{"spans":[]}`
)

// redactTextParityCase is one pair of calls on the same text: the old root's
// RedactLLM and redact.Text, which have to send the same request in every state.
func redactTextParityCase(name string, old oldroot.RedactLLMOptions, v2 redact.TextOptions) parityCase {
	return parityCase{
		name: "redact.Text/" + name, op: "redact.text", replies: []string{redactParityAnswer},
		floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.RedactLLM(redactParityText, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := redact.Text(ctx, redactParityText, v2)
			return err
		},
	}
}

func TestRedactTextSendsTheRequestTheOldRootSends(t *testing.T) {
	base := func() oldroot.RedactLLMOptions { return oldroot.NewRedactLLMOptions() }
	with := func(set func(o *oldroot.RedactLLMOptions)) oldroot.RedactLLMOptions {
		o := base()
		set(&o)
		return o
	}
	cases := []parityCase{
		redactTextParityCase("the default", base(), redact.TextOptions{}),
		redactTextParityCase("one category", with(func(o *oldroot.RedactLLMOptions) { o.Categories = []string{"email"} }), redact.TextOptions{Categories: []string{"email"}}),
		redactTextParityCase("categories of the caller's own", with(func(o *oldroot.RedactLLMOptions) { o.Categories = []string{"email", "project"} }),
			redact.TextOptions{Categories: []string{"email", "project"}}),
		redactTextParityCase("a mask character", with(func(o *oldroot.RedactLLMOptions) { o.MaskChar = '#' }), redact.TextOptions{MaskChar: '#'}),
		redactTextParityCase("ShowFirst", with(func(o *oldroot.RedactLLMOptions) { o.ShowFirst = 2 }), redact.TextOptions{ShowFirst: 2}),
		redactTextParityCase("ShowLast", with(func(o *oldroot.RedactLLMOptions) { o.ShowLast = 3 }), redact.TextOptions{ShowLast: 3}),
		redactTextParityCase("MinMask", with(func(o *oldroot.RedactLLMOptions) { o.MinMask = 5 }), redact.TextOptions{MinMask: 5}),
		redactTextParityCase("every field", with(func(o *oldroot.RedactLLMOptions) {
			o.Categories, o.MaskChar, o.ShowFirst, o.ShowLast, o.MinMask = []string{"name", "email"}, '#', 1, 2, 4
		}), redact.TextOptions{Categories: []string{"name", "email"}, MaskChar: '#', ShowFirst: 1, ShowLast: 2, MinMask: 4}),
	}
	for _, c := range commonCases {
		old := base()
		c.decision(&old.OpOptions)
		cases = append(cases, redactTextParityCase(c.name, old, redact.TextOptions{Options: c.v2}))
	}
	runParity(t, cases)
}

// redactCustomer holds a value for each way Value finds something.
type redactCustomer struct {
	Name     string
	Email    string
	Password string
	Note     string
	Plan     string
	Token    string `redact:"yes"`
	Phone    *string
	Orders   []string
	Extra    map[string]string
}

func newRedactCustomer() redactCustomer {
	phone := "555-123-4567"
	return redactCustomer{
		Name: "Ada Lovelace", Email: "ada@example.com", Password: "hunter2",
		Note: "mail ada@example.com, call 555-123-4567, iban GB82WEST12345698765432, password: hunter2, ORDER-77, card 4111 1111 1111 1111",
		Plan: "gold", Token: "tok-123", Phone: &phone, Orders: []string{"ORDER-1", "x@y.co"}, Extra: map[string]string{"ssn": "123-45-6789", "city": "Oslo"},
	}
}

// TestRedactValueRedactsAsTheOldRootDoes holds each option of Value to the old
// root's Redact: the same value comes out when the old root is given the same
// setting, since Value makes no request and the output is all there is to compare.
func TestRedactValueRedactsAsTheOldRootDoes(t *testing.T) {
	base := func() ops.RedactOptions { return ops.NewRedactOptions() }
	cases := []struct {
		name string
		old  ops.RedactOptions
		v2   redact.ValueOptions
	}{
		{"the default", base(), redact.ValueOptions{}},
		{"MaskText", base().WithMaskText("[x]"), redact.ValueOptions{MaskText: "[x]"}},
		{"MaskChar", base().WithMaskChar('#'), redact.ValueOptions{MaskChar: '#'}},
		{"MaskLength", base().WithMaskLength(7), redact.ValueOptions{MaskLength: 7}},
		{"MaskLength of minus one", base().WithMaskLength(-1), redact.ValueOptions{MaskLength: -1}},
		{"MaskChar and MaskLength", base().WithMaskChar('#').WithMaskLength(5), redact.ValueOptions{MaskChar: '#', MaskLength: 5}},
		{"Strategy Mask", base().WithStrategy(ops.RedactMask), redact.ValueOptions{Strategy: redact.Mask}},
		{"Strategy Nil", base().WithStrategy(ops.RedactNil), redact.ValueOptions{Strategy: redact.Nil}},
		{"Strategy Remove", base().WithStrategy(ops.RedactRemove), redact.ValueOptions{Strategy: redact.Remove}},
		{"Categories pii", base().WithCategories([]string{"PII"}), redact.ValueOptions{Categories: []string{"pii"}}},
		{"Categories financial", base().WithCategories([]string{"financial"}), redact.ValueOptions{Categories: []string{"financial"}}},
		{"Categories secrets", base().WithCategories([]string{"secrets"}), redact.ValueOptions{Categories: []string{"secrets"}}},
		{"Categories of all three", base().WithCategories([]string{"pii", "secrets", "financial"}), redact.ValueOptions{Categories: []string{"pii", "secrets", "financial"}}},
		{"CustomPatterns", base().WithCustomPatterns([]string{`ORDER-\d+`}), redact.ValueOptions{CustomPatterns: []string{`ORDER-\d+`}}},
		{"jumble, basic", base().WithStrategy(ops.RedactJumble).WithJumbleMode(ops.JumbleBasic).WithJumbleSeed(7), redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleBasic, JumbleSeed: 7}},
		{"jumble, smart", base().WithStrategy(ops.RedactJumble).WithJumbleMode(ops.JumbleSmart).WithJumbleSeed(7), redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleSmart, JumbleSeed: 7}},
		{"jumble, type aware", base().WithStrategy(ops.RedactJumble).WithJumbleSeed(7), redact.ValueOptions{Strategy: redact.Jumble, JumbleSeed: 7}},
		{"scramble", base().WithStrategy(ops.RedactScramble).WithJumbleMode(ops.JumbleSmart).WithJumbleSeed(-4), redact.ValueOptions{Strategy: redact.Scramble, JumbleMode: redact.JumbleSmart, JumbleSeed: -4}},
		{"every field", base().WithCategories([]string{"pii", "secrets"}).WithMaskText("<->").WithCustomPatterns([]string{`ORDER-\d+`}).WithStrategy(ops.RedactMask),
			redact.ValueOptions{Categories: []string{"pii", "secrets"}, MaskText: "<->", CustomPatterns: []string{`ORDER-\d+`}, Strategy: redact.Mask}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := oldroot.Redact(newRedactCustomer(), tc.old)
			got, err := redact.Value(newRedactCustomer(), tc.v2)
			if wantErr != nil || err != nil {
				t.Fatalf("the old root: %v, version 2: %v", wantErr, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("version 2 gave %+v, the old root %+v", got, want)
			}
			if (got.Phone == nil) != (want.Phone == nil) || (got.Phone != nil && *got.Phone != *want.Phone) {
				t.Errorf("the phone: version 2 gave %v, the old root %v", got.Phone, want.Phone)
			}
		})
	}
}

// TestRedactValueRefusesWhatTheOldRootRefuses holds the settings no call can use:
// Value refuses each one the old root refuses.
func TestRedactValueRefusesWhatTheOldRootRefuses(t *testing.T) {
	base := func() ops.RedactOptions { return ops.NewRedactOptions() }
	cases := []struct {
		name string
		old  ops.RedactOptions
		v2   redact.ValueOptions
	}{
		{"a strategy that does not exist", base().WithStrategy("obliterate"), redact.ValueOptions{Strategy: "obliterate"}},
		{"a category that does not exist", base().WithCategories([]string{"email"}), redact.ValueOptions{Categories: []string{"email"}}},
		{"no category", base().WithCategories([]string{}), redact.ValueOptions{Categories: []string{}}},
		{"a mask length below minus one", base().WithMaskLength(-2), redact.ValueOptions{MaskLength: -2}},
		{"a mask length above the longest", base().WithMaskLength(65537), redact.ValueOptions{MaskLength: 65537}},
		{"a pattern that does not compile", base().WithCustomPatterns([]string{"("}), redact.ValueOptions{CustomPatterns: []string{"("}}},
		{"a pattern that matches the empty string", base().WithCustomPatterns([]string{"x*"}), redact.ValueOptions{CustomPatterns: []string{"x*"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := oldroot.Redact(newRedactCustomer(), tc.old); err == nil {
				t.Fatal("the old root accepts the setting")
			}
			if _, err := redact.Value(newRedactCustomer(), tc.v2); err == nil {
				t.Error("version 2 accepts the setting")
			}
		})
	}
}
