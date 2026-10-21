package guard_test

import (
	"errors"
	"go/ast"
	"regexp"
	"sort"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// floors are the levels a client can be held to: every level above the one a
// call meets with no floor at all.
var floors = []schemaflux.ContractLevel{
	schemaflux.ContractJSONWellFormed,
	schemaflux.ContractSchemaConstrained,
	schemaflux.ContractSchemaAndInvariantChecked,
	schemaflux.ContractEvidenceChecked,
	schemaflux.ContractFullyGoverned,
}

// TestTheTableOfRowsHoldsTheLevelsOfTheContractTable keeps this package's own
// levels from drifting from internal/ops contracttable.go, which is table A2 as
// ruling R31 amended it: a static row has the table's level, a computed row has
// at least the table's lowest.
func TestTheTableOfRowsHoldsTheLevelsOfTheContractTable(t *testing.T) {
	for _, r := range allRows() {
		t.Run(r.id(), func(t *testing.T) {
			rule, ok := ops.ContractLevelOf(r.op)
			if !ok {
				t.Fatalf("the contract table has no row for the Record's Op %q", r.op)
			}
			if r.op == "is" || r.op == "which" {
				// Whether and RouteBy make the decision of Is and Which: same row.
			}
			if rule.Computed != r.computed {
				t.Errorf("the contract table says computed = %v for %q, the guard's row says %v", rule.Computed, r.op, r.computed)
			}
			table := schemaflux.ContractLevel(rule.Level)
			if table.String() != rule.Level.String() {
				t.Fatalf("level numbering differs: v2 %v is internal %v", table, rule.Level)
			}
			switch {
			case !r.computed && r.level != table:
				t.Errorf("the contract table says %q is delivered at %v, the guard's row says %v", r.op, table, r.level)
			case r.computed && r.level < table:
				t.Errorf("the guard's row delivers %v, below the lowest the contract table allows for %q (%v)", r.level, r.op, table)
			}
		})
	}
}

func TestEveryOperationUnderEveryFloor(t *testing.T) {
	for _, r := range allRows() {
		for _, floor := range floors {
			t.Run(r.id()+"/"+floor.String(), func(t *testing.T) {
				st := stateFloor
				st.floor = floor
				o := run(t, &r, st, scenario{name: "success", sends: true, floor: floor}, neutral())
				records := o.rig.records()
				if len(records) != 1 {
					t.Fatalf("%d Records, want 1 (error: %v)", len(records), o.err)
				}
				rec := records[0]
				calls := o.rig.model.Calls()
				if o.err == nil {
					if rec.Delivered < floor {
						t.Errorf("the call was delivered at %v, below the floor %v, and returned no error", rec.Delivered, floor)
					}
					if !r.computed && r.level < floor {
						t.Errorf("the table says %v for this operation, below the floor %v, yet the call was delivered (at %v)", r.level, floor, rec.Delivered)
					}
					return
				}
				e := failureOf(o.err)
				if e == nil || e.Kind != schemaflux.ErrPolicy {
					t.Fatalf("the call failed with %v, want delivery at or above the floor or a refusal of kind policy", o.err)
				}
				if calls != 0 {
					if r.computed {
						// A computed level is checked again after the answer (rule M10), so a
						// refusal of such a call after it was sent is the rule's own.
						t.Logf("refused after sending, as a computed level may be (%d requests)", calls)
					} else {
						t.Errorf("refused with ErrPolicy after %d requests were sent: a floor the operation cannot meet is refused before anything is sent", calls)
					}
				}
				if !r.computed && r.level >= floor {
					t.Errorf("the table says this operation is delivered at %v, which meets the floor %v, yet it was refused: %v", r.level, floor, o.err)
				}
			})
		}
	}
}

// functionWords are the words a doc comment may use for a group of operations.
var streamsWord = regexp.MustCompile(`(?i)\bstreams?\b`)

// refusedUnderAnyFloor returns the names of the operations the rows say a floor
// of the lowest kind already refuses.
func refusedUnderAnyFloor() []string {
	seen := map[string]bool{}
	for _, r := range allRows() {
		if r.level == schemaflux.ContractPromptOnly {
			seen[r.name] = true
		}
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func TestMinContractDocListsTheOperationsAFloorRefuses(t *testing.T) {
	s := loadSource(t)
	requirePackage(t, s, "")
	var comment string
	for _, f := range s.files[v2Path] {
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || spec.Name.Name != "DataPolicy" {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name == "MinContract" {
						comment = commentText(field.Doc)
					}
				}
			}
			return true
		})
	}
	if comment == "" {
		t.Fatal("DataPolicy.MinContract has no doc comment")
	}
	// Only the sentence that lists what is refused is read: from "A floor above".
	listed := comment
	if i := strings.Index(comment, "A floor above"); i >= 0 {
		listed = comment[i:]
	}
	var missing []string
	for _, name := range refusedUnderAnyFloor() {
		word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		mentioned := word.MatchString(listed)
		if !mentioned && strings.HasPrefix(name, "Stream") && streamsWord.MatchString(listed) {
			mentioned = true
		}
		if !mentioned {
			missing = append(missing, name)
		}
	}
	// Every name the comment lists must be refused by some row, and by none of the
	// rows that are delivered at a higher level.
	refused := map[string]bool{}
	for _, name := range refusedUnderAnyFloor() {
		refused[name] = true
	}
	var extra []string
	for _, r := range allRows() {
		if r.level > schemaflux.ContractPromptOnly && regexp.MustCompile(`\b`+regexp.QuoteMeta(r.name)+`\b`).MatchString(listed) && !refused[r.name] {
			extra = append(extra, r.name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("the comment of DataPolicy.MinContract does not list exactly the operations the table says a floor refuses\n  refused by the table and not listed: %v\n  listed and delivered above the lowest level: %v\n  the sentence read: %q",
			missing, extra, strings.Join(strings.Fields(listed), " "))
	}
	_ = errors.New
}
