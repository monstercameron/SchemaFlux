package redact_test

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/redact"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// customer is a value with every way Value finds something: a field named for
// sensitive data, a field with a tag, a value that matches a pattern in free
// text, and fields that hold nothing sensitive.
type customer struct {
	Name     string
	Email    string
	Password string
	Note     string
	Plan     string
	Token    string `redact:"yes"`
	Phone    *string
	Filename string
	Username string
}

func newCustomer() customer {
	phone := "555-123-4567"
	return customer{
		Name: "Ada", Email: "ada@example.com", Password: "hunter2",
		Note: "mail ada@example.com or call 555-123-4567 today", Plan: "gold", Token: "tok-123",
		Phone: &phone, Filename: "report.pdf", Username: "ada_l",
	}
}

func TestValueReplacesWhatItFindsAndLeavesTheRest(t *testing.T) {
	t.Parallel()
	input := newCustomer()
	out, err := redact.Value(input)
	if err != nil {
		t.Fatal(err)
	}
	stars := "***"
	want := customer{
		Name: "***", Email: "***", Password: "***", Note: "mail *** or call *** today", Plan: "gold", Token: "***",
		Phone: &stars, Filename: "report.pdf", Username: "ada_l",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %+v (phone %q), want %+v", out, *out.Phone, want)
	}
}

func TestValueLeavesTheInputAsItWas(t *testing.T) {
	t.Parallel()
	input := newCustomer()
	before := newCustomer()
	mapInput := map[string]any{"ssn": "123-45-6789", "list": []string{"a@b.co", "x"}, "nested": map[string]string{"password": "p"}}
	if _, err := redact.Value(input); err != nil {
		t.Fatal(err)
	}
	if _, err := redact.Value(mapInput); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, before) || *input.Phone != "555-123-4567" {
		t.Errorf("the input became %+v", input)
	}
	if mapInput["ssn"] != "123-45-6789" || mapInput["list"].([]string)[0] != "a@b.co" || mapInput["nested"].(map[string]string)["password"] != "p" {
		t.Errorf("the map input became %v", mapInput)
	}
}

func TestValueFindsSensitiveDataByNameByTagAndByPattern(t *testing.T) {
	t.Parallel()
	type tagged struct {
		Reference string `redact:"true"`
		Total     int    `redact:"true"`
		Open      string
	}
	cases := []struct {
		name  string
		call  func() (any, error)
		check func(t *testing.T, got any)
	}{
		{"a field named for sensitive data", func() (any, error) { return redact.Value(struct{ Password string }{"hunter2"}) }, func(t *testing.T, got any) {
			if got.(struct{ Password string }).Password != "***" {
				t.Errorf("got %+v", got)
			}
		}},
		{"a map key named for sensitive data", func() (any, error) { return redact.Value(map[string]string{"ssn": "123-45-6789", "city": "Oslo"}) }, func(t *testing.T, got any) {
			if !reflect.DeepEqual(got, map[string]string{"ssn": "***", "city": "Oslo"}) {
				t.Errorf("got %v", got)
			}
		}},
		{"a tagged field of any type", func() (any, error) { return redact.Value(tagged{"R-7", 42, "yes"}) }, func(t *testing.T, got any) {
			if !reflect.DeepEqual(got, tagged{"***", 0, "yes"}) {
				t.Errorf("got %+v: a tagged string is masked and any other value is set to its zero value", got)
			}
		}},
		{"a pattern in a string of a slice", func() (any, error) { return redact.Value([]string{"mail ada@example.com", "plain"}) }, func(t *testing.T, got any) {
			if !reflect.DeepEqual(got, []string{"mail ***", "plain"}) {
				t.Errorf("got %v", got)
			}
		}},
		{"a pattern in a string through pointers", func() (any, error) {
			s := "mail ada@example.com"
			p := &s
			return redact.Value(&p)
		}, func(t *testing.T, got any) {
			if **(got.(**string)) != "mail ***" {
				t.Errorf("got %q", **(got.(**string)))
			}
		}},
		{"a string that is only a value", func() (any, error) { return redact.Value("ada@example.com") }, func(t *testing.T, got any) {
			if got != "***" {
				t.Errorf("got %q", got)
			}
		}},
		{"a name that only contains a sensitive word is not one", func() (any, error) {
			return redact.Value(struct{ Filename, Keywords, KeyMetrics, CardCount string }{"a", "b", "c", "d"})
		}, func(t *testing.T, got any) {
			if got != (struct{ Filename, Keywords, KeyMetrics, CardCount string }{"a", "b", "c", "d"}) {
				t.Errorf("got %+v", got)
			}
		}},
		{"a card number by its check digit and not by its shape", func() (any, error) {
			return redact.Value([]string{"card 4111 1111 1111 1111 ok", "order 1234 5678 9012 3456 ok"})
		}, func(t *testing.T, got any) {
			list := got.([]string)
			if strings.Contains(list[0], "4111") || !strings.Contains(list[1], "1234 5678 9012 3456") {
				t.Errorf("got %v", list)
			}
		}},
		{"a value that is not text and is not named", func() (any, error) {
			return redact.Value(struct {
				N int
				B bool
				T time.Time
			}{7, true, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)})
		}, func(t *testing.T, got any) {
			g := got.(struct {
				N int
				B bool
				T time.Time
			})
			if g.N != 7 || !g.B || !g.T.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("got %+v", g)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
		})
	}
}

func TestValueEachOptionChangesTheResultAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	const email = "ada@example.com"
	cases := []struct {
		name string
		opts redact.ValueOptions
		want string
	}{
		{"the default mask is three stars", redact.ValueOptions{}, "***"},
		{"MaskText replaces the value whole", redact.ValueOptions{MaskText: "[hidden]"}, "[hidden]"},
		{"MaskText overrides MaskChar and MaskLength", redact.ValueOptions{MaskText: "[hidden]", MaskChar: '#', MaskLength: 9}, "[hidden]"},
		{"MaskChar", redact.ValueOptions{MaskChar: '#'}, "###"},
		{"MaskChar that is not ASCII", redact.ValueOptions{MaskChar: '•'}, "•••"},
		{"MaskLength", redact.ValueOptions{MaskLength: 5}, "*****"},
		{"MaskLength of the greatest length", redact.ValueOptions{MaskLength: 65536}, strings.Repeat("*", 65536)},
		{"MaskLength of minus one is the length of the value", redact.ValueOptions{MaskLength: -1}, strings.Repeat("*", len(email))},
		{"MaskChar and MaskLength", redact.ValueOptions{MaskChar: '#', MaskLength: 4}, "####"},
		{"Strategy Mask", redact.ValueOptions{Strategy: redact.Mask}, "***"},
		{"Strategy Nil", redact.ValueOptions{Strategy: redact.Nil}, ""},
		{"Strategy Remove", redact.ValueOptions{Strategy: redact.Remove}, ""},
		{"Categories pii", redact.ValueOptions{Categories: []string{"pii"}}, "***"},
		{"Categories in capitals and with space", redact.ValueOptions{Categories: []string{" PII "}}, "***"},
		{"Categories of several", redact.ValueOptions{Categories: []string{"secrets", "pii"}}, "***"},
		{"CustomPatterns", redact.ValueOptions{Categories: []string{"financial"}, CustomPatterns: []string{`ada@example\.com`}}, "***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := redact.Value([]string{"mail " + email}, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if want := "mail " + tc.want; out[0] != want {
				t.Errorf("got %q, want %q", excerpt(out[0]), excerpt(want))
			}
		})
	}
}

func TestValueOnlyFindsTheCategoriesItWasAskedFor(t *testing.T) {
	t.Parallel()
	const note = "mail ada@example.com or iban GB82WEST12345698765432 or card 4111 1111 1111 1111 or password: hunter2"
	cases := []struct {
		name       string
		categories []string
		gone       []string
		kept       []string
	}{
		{"nil means pii", nil, []string{"ada@example.com", "4111 1111 1111 1111"}, []string{"GB82WEST12345698765432", "hunter2"}},
		{"pii", []string{"pii"}, []string{"ada@example.com", "4111 1111 1111 1111"}, []string{"GB82WEST12345698765432", "hunter2"}},
		{"financial", []string{"financial"}, []string{"GB82WEST12345698765432", "4111 1111 1111 1111"}, []string{"ada@example.com", "hunter2"}},
		{"secrets", []string{"secrets"}, []string{"hunter2"}, []string{"ada@example.com", "GB82WEST12345698765432", "4111 1111 1111 1111"}},
		{"all three", []string{"pii", "secrets", "financial"}, []string{"ada@example.com", "GB82WEST12345698765432", "4111 1111 1111 1111", "hunter2"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := redact.Value([]string{note}, redact.ValueOptions{Categories: tc.categories})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.gone {
				if strings.Contains(out[0], s) {
					t.Errorf("%q is still in %q", s, out[0])
				}
			}
			for _, s := range tc.kept {
				if !strings.Contains(out[0], s) {
					t.Errorf("%q was replaced in %q", s, out[0])
				}
			}
		})
	}
}

func TestValueJumbleStrategiesChangeAValueAsTheirModesSay(t *testing.T) {
	t.Parallel()
	runesOf := func(s string) string {
		r := []rune(s)
		sort.Slice(r, func(i, j int) bool { return r[i] < r[j] })
		return string(r)
	}
	jumbled := func(t *testing.T, value string, o redact.ValueOptions) string {
		t.Helper()
		out, err := redact.Value(map[string]string{"phone": value}, o)
		if err != nil {
			t.Fatal(err)
		}
		return out["phone"]
	}
	t.Run("a seed makes the result repeatable and another seed another result", func(t *testing.T) {
		t.Parallel()
		o := redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleBasic, JumbleSeed: 7}
		a, b := jumbled(t, "ada@example.com", o), jumbled(t, "ada@example.com", o)
		o.JumbleSeed = 8
		c := jumbled(t, "ada@example.com", o)
		if a != b || a == c {
			t.Errorf("seed 7 gave %q and %q, seed 8 gave %q", a, b, c)
		}
	})
	t.Run("a negative seed is a seed", func(t *testing.T) {
		t.Parallel()
		o := redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleBasic, JumbleSeed: -3}
		if a, b := jumbled(t, "ada@example.com", o), jumbled(t, "ada@example.com", o); a != b {
			t.Errorf("a negative seed gave %q and %q", a, b)
		}
	})
	t.Run("no seed gives a result that is not repeatable", func(t *testing.T) {
		t.Parallel()
		o := redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleBasic}
		seen := map[string]bool{}
		for range 8 {
			seen[jumbled(t, "abcdefghijklmnop", o)] = true
		}
		if len(seen) < 2 {
			t.Errorf("eight calls with no seed gave %d different results", len(seen))
		}
	})
	t.Run("basic puts the same characters in another order", func(t *testing.T) {
		t.Parallel()
		out := jumbled(t, "ada@example.com", redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleBasic, JumbleSeed: 7})
		if out == "ada@example.com" || runesOf(out) != runesOf("ada@example.com") {
			t.Errorf("got %q", out)
		}
	})
	t.Run("smart keeps digits as digits, vowels as vowels, case and everything else", func(t *testing.T) {
		t.Parallel()
		in := "Ada Lovelace 555-123, ok!"
		out := jumbled(t, in, redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleSmart, JumbleSeed: 5})
		if len(out) != len(in) {
			t.Fatalf("got %q for %q", out, in)
		}
		for i := range in {
			a, b := in[i], out[i]
			switch {
			case a >= '0' && a <= '9':
				if b < '0' || b > '9' {
					t.Errorf("digit %q became %q in %q", a, b, out)
				}
			case strings.ContainsRune("aeiouAEIOU", rune(a)):
				if !strings.ContainsRune("aeiouAEIOU", rune(b)) || (a >= 'A') != (b >= 'A') || (a <= 'Z') != (b <= 'Z') {
					t.Errorf("vowel %q became %q in %q", a, b, out)
				}
			case (a >= 'a' && a <= 'z') || (a >= 'A' && a <= 'Z'):
				if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')) || strings.ContainsRune("aeiouAEIOU", rune(b)) || (a <= 'Z') != (b <= 'Z') {
					t.Errorf("consonant %q became %q in %q", a, b, out)
				}
			default:
				if a != b {
					t.Errorf("%q became %q in %q", a, b, out)
				}
			}
		}
	})
	t.Run("type aware keeps the shape of an email address, a phone number and a name", func(t *testing.T) {
		t.Parallel()
		o := redact.ValueOptions{Strategy: redact.Jumble, JumbleSeed: 9}
		if out := jumbled(t, "ada.l@example.com", o); !strings.Contains(out, "@") || !strings.HasSuffix(out, ".com") || out == "ada.l@example.com" {
			t.Errorf("an email address became %q", out)
		}
		out := jumbled(t, "555-123-4567", o)
		if len(out) != 12 || out[3] != '-' || out[7] != '-' || runesOf(out) != runesOf("555-123-4567") {
			t.Errorf("a phone number became %q", out)
		}
		if out := jumbled(t, "Ada Lovelace", o); len(strings.Fields(out)) != 2 || runesOf(out) != runesOf("Ada Lovelace") {
			t.Errorf("a name became %q", out)
		}
	})
	t.Run("the default mode is type aware", func(t *testing.T) {
		t.Parallel()
		with := jumbled(t, "ada.l@example.com", redact.ValueOptions{Strategy: redact.Jumble, JumbleSeed: 9, JumbleMode: redact.JumbleTypeAware})
		without := jumbled(t, "ada.l@example.com", redact.ValueOptions{Strategy: redact.Jumble, JumbleSeed: 9})
		if with != without {
			t.Errorf("%q and %q", with, without)
		}
	})
	t.Run("scramble is jumble", func(t *testing.T) {
		t.Parallel()
		a := jumbled(t, "ada@example.com", redact.ValueOptions{Strategy: redact.Jumble, JumbleMode: redact.JumbleBasic, JumbleSeed: 4})
		b := jumbled(t, "ada@example.com", redact.ValueOptions{Strategy: redact.Scramble, JumbleMode: redact.JumbleBasic, JumbleSeed: 4})
		if a != b {
			t.Errorf("%q and %q", a, b)
		}
	})
}

func TestValueMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	value := func(opts ...redact.ValueOptions) string {
		out, err := redact.Value([]string{"ada@example.com"}, opts...)
		if err != nil {
			t.Fatal(err)
		}
		return out[0]
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"no value is the default", value(), "***"},
		{"one value is used as it is", value(redact.ValueOptions{MaskLength: 5}), "*****"},
		{"each value adds its fields", value(redact.ValueOptions{MaskLength: 5}, redact.ValueOptions{MaskChar: '#'}), "#####"},
		{"the later value wins", value(redact.ValueOptions{MaskLength: 5}, redact.ValueOptions{MaskChar: '#'}, redact.ValueOptions{MaskLength: 4}), "####"},
		{"a later zero value does not undo an earlier one", value(redact.ValueOptions{MaskLength: 5, MaskChar: '#'}, redact.ValueOptions{}), "#####"},
		{"a later strategy replaces the earlier", value(redact.ValueOptions{Strategy: redact.Nil}, redact.ValueOptions{Strategy: redact.Mask}), "***"},
		{"a later MaskText replaces the earlier", value(redact.ValueOptions{MaskText: "[a]"}, redact.ValueOptions{MaskText: "[b]"}), "[b]"},
		{"the earlier categories survive a nil later list", value(redact.ValueOptions{Categories: []string{"financial"}}, redact.ValueOptions{MaskChar: '#'}), "ada@example.com"},
		{"the later categories replace the earlier", value(redact.ValueOptions{Categories: []string{"financial"}}, redact.ValueOptions{Categories: []string{"pii"}}), "***"},
		{"custom patterns of the later value", value(redact.ValueOptions{Categories: []string{"financial"}}, redact.ValueOptions{CustomPatterns: []string{`example`}}), "ada@***.com"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestValueRefusesASettingNoCallCanUseAsAConfigurationError(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-8d17ac"
	cases := []struct {
		name string
		opts []redact.ValueOptions
		want string
	}{
		{"a strategy that does not exist", []redact.ValueOptions{{Strategy: "obliterate"}}, "Strategy"},
		{"a strategy that is the canary", []redact.ValueOptions{{Strategy: redact.Strategy(canary)}}, "Strategy"},
		{"a jumble mode that does not exist", []redact.ValueOptions{{JumbleMode: redact.JumbleMode(canary)}}, "JumbleMode"},
		{"an empty list of categories", []redact.ValueOptions{{Categories: []string{}}}, "Categories"},
		{"a category that does not exist", []redact.ValueOptions{{Categories: []string{"pii", canary}}}, "Categories[1]"},
		{"a category with two names", []redact.ValueOptions{{Categories: []string{"pii,secrets"}}}, "Categories[0]"},
		{"a blank category", []redact.ValueOptions{{Categories: []string{" "}}}, "Categories[0]"},
		{"a mask length below minus one", []redact.ValueOptions{{MaskLength: -2}}, "MaskLength"},
		{"the lowest mask length", []redact.ValueOptions{{MaskLength: math.MinInt}}, "MaskLength"},
		{"a mask length above 65536", []redact.ValueOptions{{MaskLength: 65537}}, "MaskLength"},
		{"the greatest mask length", []redact.ValueOptions{{MaskLength: math.MaxInt}}, "MaskLength"},
		{"a negative mask character", []redact.ValueOptions{{MaskChar: -1}}, "MaskChar"},
		{"a surrogate half as a mask character", []redact.ValueOptions{{MaskChar: 0xD800}}, "MaskChar"},
		{"a mask character above the last character", []redact.ValueOptions{{MaskChar: 0x110000}}, "MaskChar"},
		{"a pattern that does not compile", []redact.ValueOptions{{CustomPatterns: []string{canary + "("}}}, "CustomPatterns[0]"},
		{"a pattern that matches the empty string", []redact.ValueOptions{{CustomPatterns: []string{"ok", "(" + canary + ")?"}}}, "CustomPatterns[1]"},
		{"a bad setting in the later of two values", []redact.ValueOptions{{MaskLength: 5}, {MaskLength: -7}}, "MaskLength"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := redact.Value(map[string]string{"email": canary + "@example.com"}, tc.opts...)
			e := wantError(t, err, schemaflux.ErrConfiguration, "redact.value")
			if out != nil {
				t.Errorf("a refused call returned %v", out)
			}
			if !strings.Contains(e.Error(), tc.want) {
				t.Errorf("the error does not name %s: %v", tc.want, e)
			}
			if strings.Contains(e.Error(), "invalid options") {
				t.Errorf("the refusal is the internal call's and not the function's own: %v", e)
			}
			for _, text := range chainText(err) {
				if strings.Contains(text, canary) {
					t.Errorf("the chain says %q", text)
				}
			}
		})
	}
}

func TestValueAcceptsTheEdgesOfWhatItRefuses(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]redact.ValueOptions{
		"a mask length of -1":         {MaskLength: -1},
		"a mask length of 65536":      {MaskLength: 65536},
		"every category":              {Categories: []string{"PII", "Secrets", " financial "}},
		"a valid unicode mask":        {MaskChar: 0x10FFFF},
		"a pattern of one character":  {CustomPatterns: []string{"x"}},
		"patterns that all match":     {CustomPatterns: []string{"a+", `\d`}},
		"every strategy and mode set": {Strategy: redact.Scramble, JumbleMode: redact.JumbleSmart, JumbleSeed: math.MinInt64},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := redact.Value(map[string]string{"email": "ada@example.com"}, o); err != nil {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestValueRefusesAnInputItCannotWalkAndNamesNothingOfIt(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5b02e9"
	type loop struct {
		Next *loop
		Note string
	}
	cyclic := &loop{Note: canary}
	cyclic.Next = cyclic
	type withHidden struct {
		Visible string
		hidden  string `redact:"yes"`
	}
	cases := []struct {
		name string
		call func() error
	}{
		{"a nil interface", func() error { _, err := redact.Value[any](nil); return err }},
		{"a value that refers back to itself", func() error { _, err := redact.Value(cyclic); return err }},
		{"an unexported field tagged for redaction", func() error { _, err := redact.Value(withHidden{canary, canary}); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.call()
			wantError(t, err, schemaflux.ErrInvalidRequest, "redact.value")
			for _, text := range chainText(err) {
				if strings.Contains(text, canary) {
					t.Errorf("the chain says %q", text)
				}
			}
		})
	}
}

func TestValueReturnsTheZeroValueBesideAnError(t *testing.T) {
	t.Parallel()
	got, err := redact.Value(newCustomer(), redact.ValueOptions{MaskLength: -9})
	wantError(t, err, schemaflux.ErrConfiguration, "redact.value")
	if !reflect.DeepEqual(got, customer{}) {
		t.Errorf("got %+v beside the error", got)
	}
	if str, err := redact.Value("x", redact.ValueOptions{Strategy: "?"}); err == nil || str != "" {
		t.Errorf("got %q, %v", str, err)
	}
}

func TestValueWorksOnEveryKindOfValue(t *testing.T) {
	t.Parallel()
	type named string
	cases := []struct {
		name string
		call func() (any, error)
		want any
	}{
		{"an int", func() (any, error) { return redact.Value(42) }, 42},
		{"a bool", func() (any, error) { return redact.Value(true) }, true},
		{"a named string", func() (any, error) { return redact.Value(named("ada@example.com")) }, named("***")},
		{"an array", func() (any, error) { return redact.Value([2]string{"ada@example.com", "x"}) }, [2]string{"***", "x"}},
		{"an empty slice", func() (any, error) { return redact.Value([]string{}) }, []string{}},
		{"a nil slice", func() (any, error) { return redact.Value([]string(nil)) }, []string(nil)},
		{"a nil map", func() (any, error) { return redact.Value(map[string]string(nil)) }, map[string]string(nil)},
		{"a nil pointer", func() (any, error) { return redact.Value((*string)(nil)) }, (*string)(nil)},
		{"an empty string", func() (any, error) { return redact.Value("") }, ""},
		{"a string with nothing in it", func() (any, error) { return redact.Value("hello") }, "hello"},
		{"a slice of any", func() (any, error) { return redact.Value([]any{"ada@example.com", 5, nil}) }, []any{"***", 5, nil}},
		{"a struct with an unexported field that is not tagged", func() (any, error) {
			return redact.Value(struct {
				Open   string
				hidden string
			}{"a@b.co", "h"})
		}, struct {
			Open   string
			hidden string
		}{"***", "h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.call()
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, %v, want %#v", got, err, tc.want)
			}
		})
	}
}

func TestValueIsSafeForConcurrentCallsThatShareOptions(t *testing.T) {
	t.Parallel()
	opts := redact.ValueOptions{Categories: []string{"pii"}, CustomPatterns: []string{`ORDER-\d+`}, MaskLength: 4}
	var wg sync.WaitGroup
	failures := make(chan string, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := redact.Value([]string{"ORDER-12 for ada@example.com"}, opts)
			if err != nil {
				failures <- err.Error()
			} else if out[0] != "**** for ****" {
				failures <- out[0]
			}
		}()
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Errorf("got %q", f)
	}
}

func TestValueAsksNoModelAddsNoRecordAndIsHeldToNoFloor(t *testing.T) {
	// Not parallel: it installs the default client and an observer, which are
	// process state.
	model := namedModel(`{"spans":[]}`)
	underFloor(schemaflux.ContractPromptOnly)(new(schemaflux.Client)) // declares the route of the fake
	var recorder schemaflux.Recorder
	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{
		Provider: model, Recorder: &recorder,
		Policy: &schemaflux.DataPolicy{Providers: []string{"redact-floor"}, Regions: []string{"eu"}, MinContract: schemaflux.ContractFullyGoverned},
	})
	defer schemaflux.SetDefault(previous)
	var scopes []telemetry.Result
	remove := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) { scopes = append(scopes, r) }})
	defer remove()

	out, err := redact.Value(newCustomer())
	if err != nil || out.Name != "***" {
		t.Fatalf("got %+v, %v: a floor no function can meet must not refuse Value", out, err)
	}
	if model.Calls() != 0 || len(recorder.Records()) != 0 || len(scopes) != 0 {
		t.Errorf("%d requests, %d Records and %d finished scopes: Value asks no model and adds no Record", model.Calls(), len(recorder.Records()), len(scopes))
	}
	// A failing call adds nothing either.
	if _, err := redact.Value(newCustomer(), redact.ValueOptions{MaskLength: -5}); err == nil {
		t.Fatal("a bad setting was accepted")
	}
	if len(recorder.Records()) != 0 || len(scopes) != 0 {
		t.Errorf("a failing call added %d Records and %d finished scopes", len(recorder.Records()), len(scopes))
	}
}

func TestValueKeepsWhatItRedactsOutOfEveryError(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-31c7d4"
	input := map[string]any{"ssn": canary, "note": "mail " + canary + "@example.com", "ch": make(chan int)}
	for name, call := range map[string]func() error{
		"a refused setting": func() error { _, err := redact.Value(input, redact.ValueOptions{MaskLength: -2}); return err },
		"a refused strategy": func() error {
			_, err := redact.Value(input, redact.ValueOptions{Strategy: redact.Strategy(canary)})
			return err
		},
		"a refused pattern": func() error {
			_, err := redact.Value(input, redact.ValueOptions{CustomPatterns: []string{canary + "["}})
			return err
		},
		"a refused category": func() error {
			_, err := redact.Value(input, redact.ValueOptions{Categories: []string{canary}})
			return err
		},
		"a cycle holding it": func() error {
			type loop struct {
				Next *loop
				Note string
			}
			l := &loop{Note: canary}
			l.Next = l
			_, err := redact.Value(l)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := call()
			if err == nil {
				t.Fatal("no error")
			}
			requireNoLeak(t, canary, err, true, nil, nil)
		})
	}
}

func TestValueAndTextDoNotShareAnError(t *testing.T) {
	t.Parallel()
	_, valueErr := redact.Value("x", redact.ValueOptions{MaskLength: -2})
	_, textErr := redact.Text(clientOn(answeringWith(`{"spans":[]}`)), "x", redact.TextOptions{ShowFirst: -1})
	if wantError(t, valueErr, schemaflux.ErrConfiguration, "redact.value").Op == wantError(t, textErr, schemaflux.ErrConfiguration, "redact.text").Op {
		t.Error("the two functions name themselves alike in an error")
	}
}
