package ops

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Direct tests of helpers round 6 of review found covered only through the
// operations that call them.

type decodesJSON string

func (d *decodesJSON) UnmarshalJSON([]byte) error { return nil }

type decodesText string

func (d *decodesText) UnmarshalText([]byte) error { return nil }

type decodesNothing string

type decodesByValue struct{ A int }

func (decodesByValue) MarshalJSON() ([]byte, error) { return []byte("1"), nil }

func TestDecodesItself(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"string", "", false},
		{"a named string", decodesNothing(""), false},
		{"a named string with UnmarshalJSON", decodesJSON(""), true},
		{"a named string with UnmarshalText", decodesText(""), true},
		{"int", 0, false},
		{"a struct", struct{ A int }{}, false},
		{"a type that only encodes itself", decodesByValue{}, false},
		{"time.Time", time.Time{}, true},
		{"json.RawMessage", json.RawMessage(nil), true},
		{"json.Number", json.Number(""), false},
		{"a slice of strings", []string(nil), false},
		{"a map", map[string]int(nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodesItself(reflect.TypeOf(tc.value)); got != tc.want {
				t.Errorf("decodesItself(%T) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestJoinCategories(t *testing.T) {
	cases := []struct {
		name, have, more, want string
	}{
		{"both empty", "", "", ""},
		{"the first", "", "email", "email"},
		{"a second", "email", "phone", "email,phone"},
		{"the same again", "email", "email", "email"},
		{"the same in another case", "email", "EMAIL", "email"},
		{"padding", " email ", "  phone", "email,phone"},
		{"a list added to a list", "email,phone", "phone,name", "email,phone,name"},
		{"a label that is itself a list", "", "email, phone", "email,phone"},
		{"empty parts are dropped", "email,,", ",phone,", "email,phone"},
		{"nothing added", "email,phone", "", "email,phone"},
		{"an unnormalised first argument", "a, b", "b ,c", "a,b,c"},
		{"order is the order met", "name", "email,name,phone", "name,email,phone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinCategories(tc.have, tc.more); got != tc.want {
				t.Errorf("joinCategories(%q, %q) = %q, want %q", tc.have, tc.more, got, tc.want)
			}
		})
	}
}

func TestDeduplicateAnswer(t *testing.T) {
	cases := []struct {
		name      string
		response  string
		total     int
		threshold float64
		want      []int
		wantKind  types.ErrorKind
	}{
		{"an empty list", `{"duplicates":[]}`, 3, 0.8, []int{-1, -1, -1}, types.KindUnknown},
		{"one link", `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9}]}`, 3, 0.8, []int{-1, -1, 0}, types.KindUnknown},
		{"a link under the threshold", `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.5}]}`, 3, 0.8, []int{-1, -1, -1}, types.KindUnknown},
		{"in a code fence", "```json\n{\"duplicates\":[]}\n```", 2, 0.8, []int{-1, -1}, types.KindUnknown},
		{"fields it does not use", `{"duplicates":[],"note":"none"}`, 2, 0.8, []int{-1, -1}, types.KindUnknown},
		{"prose", `no duplicates here`, 3, 0.8, nil, types.KindMalformedOutput},
		{"an empty object", `{}`, 3, 0.8, nil, types.KindSchemaViolation},
		{"another shape", `{"groups":[[0,1]]}`, 3, 0.8, nil, types.KindSchemaViolation},
		{"a null list", `{"duplicates":null}`, 3, 0.8, nil, types.KindSchemaViolation},
		{"a list of numbers", `{"duplicates":[1,2]}`, 3, 0.8, nil, types.KindSchemaViolation},
		{"an index outside the list", `{"duplicates":[{"index":3,"duplicate_of":0,"similarity":0.9}]}`, 3, 0.8, nil, types.KindInvariantViolation},
		{"a fractional index", `{"duplicates":[{"index":1.5,"duplicate_of":0,"similarity":0.9}]}`, 3, 0.8, nil, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deduplicateAnswer(tc.response, tc.total, tc.threshold)
			if types.KindOf(err) != tc.wantKind {
				t.Fatalf("err = %v (kind %v), want kind %v", err, types.KindOf(err), tc.wantKind)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("links = %v, want %v", got, tc.want)
			}
		})
	}
}

type streamContextMark struct{}

func TestStreamContext(t *testing.T) {
	mark := func(name string) context.Context {
		return context.WithValue(context.Background(), streamContextMark{}, name)
	}
	name := func(ctx context.Context) any {
		if ctx == nil {
			return nil
		}
		return ctx.Value(streamContextMark{})
	}
	cases := []struct {
		name     string
		resolved context.Context
		common   context.Context
		embedded context.Context
		want     any
	}{
		{"what the options resolved to", mark("resolved"), mark("common"), mark("embedded"), "resolved"},
		{"nothing resolved: CommonOptions", nil, mark("common"), mark("embedded"), "common"},
		{"nothing resolved, no CommonOptions: the embedded half", nil, nil, mark("embedded"), "embedded"},
		{"nothing anywhere: a background context", nil, nil, nil, nil},
		{"resolved alone", mark("resolved"), nil, nil, "resolved"},
		{"CommonOptions alone", nil, mark("common"), nil, "common"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := streamContext(types.OpOptions{Context: tc.resolved}, CommonOptions{Context: tc.common}, types.OpOptions{Context: tc.embedded})
			if got == nil {
				t.Fatal("no context at all")
			}
			if name(got) != tc.want {
				t.Errorf("the stream runs on %v, want %v", name(got), tc.want)
			}
		})
	}

	// Through the options, as a caller sets them: the four homes a stream's
	// context can come from, and the timeout.
	for _, tc := range []struct {
		name string
		set  func(*SummarizeOptions)
		want any
	}{
		{"CommonOptions.Context", func(o *SummarizeOptions) { o.CommonOptions.Context = mark("common") }, "common"},
		{"OpOptions.Context", func(o *SummarizeOptions) { o.OpOptions.Context = mark("embedded") }, "embedded"},
		{"both: CommonOptions wins", func(o *SummarizeOptions) {
			o.CommonOptions.Context, o.OpOptions.Context = mark("common"), mark("embedded")
		}, "common"},
		{"neither", func(*SummarizeOptions) {}, nil},
	} {
		t.Run("options/"+tc.name, func(t *testing.T) {
			opts := NewSummarizeOptions()
			tc.set(&opts)
			got := streamContext(opts.toOpOptions(), opts.CommonOptions, opts.OpOptions)
			if name(got) != tc.want {
				t.Errorf("the stream runs on %v, want %v", name(got), tc.want)
			}
		})
	}

	opts := NewSummarizeOptions()
	opts.CommonOptions.Timeout = 5 * time.Second
	deadline, bounded := streamContext(opts.toOpOptions(), opts.CommonOptions, opts.OpOptions).Deadline()
	if !bounded || time.Until(deadline) > 5*time.Second {
		t.Errorf("the caller's timeout did not reach the stream's context")
	}
}

// A value the model lists twice under two categories is one span that carries
// both, and each is counted.
func TestRedactLLMAValueListedTwiceCarriesBothCategories(t *testing.T) {
	const text = "Reach Dana Kim today"
	cases := []struct {
		name, response string
		category       string
		counts         map[string]int
	}{
		{"two categories", `{"spans":[{"text":"Dana Kim","category":"name"},{"text":"Dana Kim","category":"employee"}]}`, "name,employee", map[string]int{"name": 1, "employee": 1}},
		{"the same category twice", `{"spans":[{"text":"Dana Kim","category":"name"},{"text":"Dana Kim","category":"name"}]}`, "name", map[string]int{"name": 1}},
		{"in two cases", `{"spans":[{"text":"Dana Kim","category":"name"},{"text":"DANA KIM","category":"employee"}]}`, "name,employee", map[string]int{"name": 1, "employee": 1}},
		{"one of them unlabelled", `{"spans":[{"text":"Dana Kim","category":""},{"text":"Dana Kim","category":"name"}]}`, "name", map[string]int{"name": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := redactWith(t, text, tc.response, NewRedactLLMOptions())
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != "Reach ******** today" || len(result.Spans) != 1 {
				t.Fatalf("Text = %q, Spans = %+v", result.Text, result.Spans)
			}
			if result.Spans[0].Category != tc.category {
				t.Errorf("Category = %q, want %q", result.Spans[0].Category, tc.category)
			}
			if !reflect.DeepEqual(result.Categories, tc.counts) {
				t.Errorf("Categories = %v, want %v", result.Categories, tc.counts)
			}
		})
	}
}
