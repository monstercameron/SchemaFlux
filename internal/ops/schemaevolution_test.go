package ops

import (
	"reflect"
	"strings"
	"testing"
)

// S-011. A Go type is a contract with a model, a cache, and every result
// already stored. Changing it is a release decision, and the decision needs a
// name -- today both "added a field" and "changed a field's type" are "someone
// edited a struct".
func TestCompareSchemasClassifiesTheChange(t *testing.T) {
	type base struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	cases := []struct {
		name   string
		after  any
		want   SchemaChange
		expect string
	}{
		{
			"unchanged",
			struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			}{},
			SchemaUnchanged, "",
		},
		{
			"an optional field added",
			struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
				Note  string `json:"note,omitempty"`
			}{},
			SchemaCompatible, "note",
		},
		{
			"a required field added",
			struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
				Owner string `json:"owner"`
			}{},
			SchemaNewContract, "owner",
		},
		{
			"a field's type changed",
			struct {
				Name  string `json:"name"`
				Count string `json:"count"`
			}{},
			SchemaNewContract, "count",
		},
		{
			"an optional field made required",
			struct {
				Name  string `json:"name"`
				Count int    `json:"count" schemaflux:"required"`
			}{},
			SchemaUnchanged, "", // Count was already required
		},
		{
			"a field removed",
			struct {
				Name string `json:"name"`
			}{},
			SchemaBreaking, "count",
		},
		{
			"a json tag renamed",
			struct {
				Name  string `json:"name"`
				Total int    `json:"total"`
			}{},
			SchemaBreaking, "count",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := CompareSchemas(reflect.TypeOf(base{}), reflect.TypeOf(tc.after))
			if diff.Change != tc.want {
				t.Fatalf("change = %v, want %v (%s)", diff.Change, tc.want, diff.Summary())
			}
			if tc.expect != "" && !strings.Contains(diff.Summary(), tc.expect) {
				t.Errorf("the summary does not name %q: %s", tc.expect, diff.Summary())
			}
		})
	}
}

// A required field becoming optional is compatible, and worth naming because it
// changes what Strict mode accepts.
func TestLooseningIsCompatibleAndNamed(t *testing.T) {
	type before struct {
		Name string `json:"name"`
	}
	type after struct {
		Name string `json:"name,omitempty"`
	}

	diff := CompareSchemas(reflect.TypeOf(before{}), reflect.TypeOf(after{}))
	if diff.Change != SchemaCompatible {
		t.Errorf("change = %v, want compatible", diff.Change)
	}
	if !strings.Contains(diff.Summary(), "loosened name") {
		t.Errorf("the summary does not name the loosened field: %s", diff.Summary())
	}
}

// Two Go types that serialise the same way are the same contract, which is the
// same rule the schema hash follows.
func TestTheContractIsTheJSONShapeNotTheGoType(t *testing.T) {
	type before struct {
		Count int `json:"count"`
	}
	type after struct {
		Count int64 `json:"count"` // different Go type, same JSON contract
	}

	if diff := CompareSchemas(reflect.TypeOf(before{}), reflect.TypeOf(after{})); diff.Change != SchemaUnchanged {
		t.Errorf("change = %v (%s), want unchanged", diff.Change, diff.Summary())
	}
}

// The worst change wins, because a release note has to describe the release
// rather than its most flattering part.
func TestTheWorstChangeWins(t *testing.T) {
	type before struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	type after struct {
		Name string `json:"name"`
		Note string `json:"note,omitempty"` // compatible
		// count removed: breaking
	}

	diff := CompareSchemas(reflect.TypeOf(before{}), reflect.TypeOf(after{}))
	if diff.Change != SchemaBreaking {
		t.Errorf("change = %v, want breaking despite the compatible addition", diff.Change)
	}
	if !strings.Contains(diff.Summary(), "added note") || !strings.Contains(diff.Summary(), "removed count") {
		t.Errorf("the summary loses one of the changes: %s", diff.Summary())
	}
}

// Slices and pointers compare by their element, because that is where the
// contract lives.
func TestSlicesAndPointersCompareByElement(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	type itemV2 struct {
		Name  string `json:"name"`
		Extra string `json:"extra"`
	}

	diff := CompareSchemas(reflect.TypeOf([]item{}), reflect.TypeOf([]itemV2{}))
	if diff.Change != SchemaNewContract {
		t.Errorf("change = %v, want new contract", diff.Change)
	}

	diff = CompareSchemas(reflect.TypeOf(&item{}), reflect.TypeOf(&item{}))
	if diff.Change != SchemaUnchanged {
		t.Errorf("change = %v, want unchanged", diff.Change)
	}
}

// A change that is compatible for decoding still changes the cache identity,
// and pretending otherwise would serve stale answers.
func TestACompatibleChangeStillChangesTheSchemaHash(t *testing.T) {
	type before struct {
		Name string `json:"name"`
	}
	type after struct {
		Name string `json:"name"`
		Note string `json:"note,omitempty"`
	}

	if CompareSchemas(reflect.TypeOf(before{}), reflect.TypeOf(after{})).Change != SchemaCompatible {
		t.Fatal("setup: expected a compatible change")
	}

	if DescribeSchema(reflect.TypeOf(before{})).Hash == DescribeSchema(reflect.TypeOf(after{})).Hash {
		t.Error("a compatible change left the schema hash unchanged, so a cache would serve the old answer")
	}
}
