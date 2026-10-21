package ops

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What the catalogue holds, read from the source of internal/api/client: that
// package imports this one, so a test here cannot import it.
func contractCatalogueNames(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "api", "client", "catalog.go"), nil, 0)
	if err != nil {
		t.Fatalf("reading the catalogue: %v", err)
	}
	seen := map[string]bool{}
	var names []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "init" {
			continue
		}
		ast.Inspect(function, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(literal.Value)
			if err == nil && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
			return true
		})
	}
	sort.Strings(names)
	return names
}

// contractPackageFiles parses the non-test files of this package.
func contractPackageFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			// Another lane may be mid-edit in a file; the others still count.
			t.Logf("skipping %s: %v", path, err)
			continue
		}
		files[path] = file
	}
	if len(files) < 100 {
		t.Fatalf("only %d files of the package were read", len(files))
	}
	return files
}

// contractExportedFunctions is every exported function of the package.
func contractExportedFunctions(t *testing.T) map[string]bool {
	t.Helper()
	exported := map[string]bool{}
	for _, file := range contractPackageFiles(t) {
		for _, declaration := range file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil && function.Name.IsExported() {
				exported[function.Name.Name] = true
			}
		}
	}
	return exported
}

// contractExternalFunctions reads the v2 package named by a contract row.
// Those rows cannot be imported here because they depend on internal/ops.
func contractExternalFunctions(t *testing.T, path string) map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..", filepath.FromSlash(path))
	packages, err := parser.ParseDir(token.NewFileSet(), root, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	exported := map[string]bool{}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.IsExported() {
					exported[function.Name.Name] = true
				}
			}
		}
	}
	return exported
}

// contractCallPathNames is every operation name the package writes as a literal
// where an operation names itself: on the context (WithOperationName), to the
// recorded call (dx12ResultFrom, textResult, judgedResult), to the observer
// (observeOperation, assessCore) and to the reader of its answer (decodeAnswer,
// decodeCallerAnswer, readAnswerObject), which is where the operations that
// name nothing else say who they are. The value is the file it was found in.
func contractCallPathNames(t *testing.T) map[string]string {
	t.Helper()
	argument := map[string]int{
		"WithOperationName": 1, "dx12ResultFrom": 0, "textResult": 0, "judgedResult": 1,
		"observeOperation": 1, "assessCore": 1,
		"decodeAnswer": 0, "decodeCallerAnswer": 0, "readAnswerObject": 0,
	}
	found := map[string]string{}
	for path, file := range contractPackageFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			position, wanted := argument[callee.Name]
			if !wanted || position >= len(call.Args) {
				return true
			}
			literal, ok := call.Args[position].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			if name, err := strconv.Unquote(literal.Value); err == nil {
				found[name] = path
			}
			return true
		})
	}
	return found
}

func TestEveryCataloguedOperationHasALevelOrAsksNoModel(t *testing.T) {
	covered := map[string]string{}
	for i := range contractRows {
		for _, name := range contractRows[i].catalogue {
			if other, twice := covered[name]; twice {
				t.Errorf("%s is the catalogue entry of both %s and %s", name, other, contractRows[i].name)
			}
			covered[name] = contractRows[i].name
		}
	}
	names := contractCatalogueNames(t)
	if len(names) < 100 {
		t.Fatalf("only %d catalogue entries were read", len(names))
	}
	inCatalogue := map[string]bool{}
	for _, name := range names {
		inCatalogue[name] = true
		_, hasRow := covered[name]
		_, asksNoModel := noModelOperations[name]
		switch {
		case hasRow && asksNoModel:
			t.Errorf("%s has a row and is listed as asking no model", name)
		case !hasRow && !asksNoModel:
			t.Errorf("%s is in the catalogue and has no contract level: add a row to contractRows, or list it in noModelOperations with the reason it asks no model", name)
		}
	}
	for name := range covered {
		if !inCatalogue[name] {
			t.Errorf("a row names the catalogue entry %s, which is not in the catalogue", name)
		}
	}
	for name := range noModelOperations {
		if !inCatalogue[name] {
			t.Errorf("noModelOperations names %s, which is not in the catalogue", name)
		}
	}
}

func TestEveryRowIsAnOperationThatExists(t *testing.T) {
	exported := contractExportedFunctions(t)
	seen := map[string]bool{}
	for i := range contractRows {
		row := &contractRows[i]
		if seen[row.name] {
			t.Errorf("the table has two rows named %s", row.name)
		}
		seen[row.name] = true
		if !strings.HasSuffix(row.evidence, ".") || strings.Contains(row.evidence, ". ") {
			t.Errorf("%s: the evidence is not one sentence: %q", row.name, row.evidence)
		}
		if len(row.catalogue) == 0 && len(row.functions) == 0 {
			t.Errorf("%s names no catalogue entry and no function, so nothing shows the operation exists", row.name)
		}
		rowExported := exported
		if row.externalPkg != "" {
			rowExported = contractExternalFunctions(t, row.externalPkg)
		}
		for _, function := range row.functions {
			if !rowExported[function] {
				t.Errorf("%s: %s is not an exported function of %s", row.name, function, row.externalPkg)
			}
		}
		if row.computed && row.via == "" {
			t.Errorf("%s is computed and does not say which function computes it", row.name)
		}
		if !row.computed && row.via != "" {
			t.Errorf("%s is static and names %q as computing it", row.name, row.via)
		}
		if row.typeBound && row.without >= row.level && row.level != types.ContractPromptOnly {
			t.Errorf("%s: the level without a schema (%s) is not below the level with one (%s)", row.name, row.without, row.level)
		}
		if row.alternative != 0 && row.alternative <= row.level {
			t.Errorf("%s: the alternative level %s is not above the row's %s", row.name, row.alternative, row.level)
		}
		if row.level >= types.ContractEvidenceChecked {
			t.Errorf("%s: no operation delivers %s except an Op with an evidence policy", row.name, row.level)
		}
	}
	for name := range differsFromTableA2 {
		if !seen[name] {
			t.Errorf("differsFromTableA2 explains %s, which is not a row", name)
		}
	}
}

// contractReaderNames are the names an operation gives its refusals, where it
// writes the name of the operation to the reader of its answer and puts none on
// the context: each is the row of the operation it names, or "none: " and the
// reason it is not one.
var contractReaderNames = map[string]string{
	"predict": "exp.predict", "resolve": "exp.resolve", "enrich": "exp.enrich", "synthesize": "exp.synthesize",
	"project": "exp.project", "arbitrate": "exp.arbitrate", "conform": "exp.conform", "pivot": "exp.pivot",
	"compose": "exp.assemble", "derive": "exp.derive", "decompose": "exp.decompose", "decide": "flow.decide",
	"batch": "none: the id batch protocol names its refusals so, for op.runbatch, op.runmanyrecover and ExtractBatch, which have rows",
}

func TestEveryNameTheCallPathCanProduceHasARow(t *testing.T) {
	found := contractCallPathNames(t)
	if len(found) < 40 {
		t.Fatalf("only %d names were found in the call path", len(found))
	}
	// The names an operation can have without writing them as a literal: an
	// Op's own, which RunOp puts on the context, is a caller's and is the runner's
	// row, and Extract's cache identity names it "extract".
	for _, name := range []string{"extract", "op.run", "op.runbatch", "op.runmany"} {
		found[name] = "opname.go and op.go"
	}
	for name, path := range found {
		if _, known := ContractLevelOf(name); known {
			continue
		}
		target, named := contractReaderNames[name]
		switch {
		case !named:
			t.Errorf("%s (written in %s) names an operation and has no row", name, path)
		case strings.HasPrefix(target, "none: "):
		default:
			if _, known := ContractLevelOf(target); !known {
				t.Errorf("%s is said to be the operation %s, which has no row", name, target)
			}
		}
	}
	for name := range contractReaderNames {
		if _, written := found[name]; !written {
			t.Errorf("contractReaderNames lists %s, which the package does not write", name)
		}
		if _, known := ContractLevelOf(name); known {
			t.Errorf("contractReaderNames lists %s, which has a row of its own", name)
		}
	}
}

func TestContractLevelOf(t *testing.T) {
	cases := []struct {
		name      string
		operation string
		level     types.ContractLevel
		computed  bool
		known     bool
	}{
		{"filter is well formed", "filter", types.ContractJSONWellFormed, false, true},
		{"sort is the lower reading", "sort", types.ContractJSONWellFormed, false, true},
		{"summarize takes the text", "summarize", types.ContractPromptOnly, false, true},
		{"summarize detail is parsed", "summarizedetail", types.ContractJSONWellFormed, false, true},
		{"similar declares a schema", "similar", types.ContractSchemaConstrained, false, true},
		{"audit checks its findings", "audit", types.ContractSchemaAndInvariantChecked, false, true},
		{"the decisions are one level", "rate", types.ContractSchemaAndInvariantChecked, false, true},
		{"the detail of a decision is the same", "assessdetail", types.ContractSchemaAndInvariantChecked, false, true},
		{"extract is computed from its options", "extract", types.ContractSchemaConstrained, true, true},
		{"generate is computed from its type", "generate", types.ContractPromptOnly, true, true},
		{"a run is computed from its descriptor", "op.run", types.ContractJSONWellFormed, true, true},
		{"a batch is computed and may be prompt only", "op.runbatch", types.ContractPromptOnly, true, true},
		{"an experimental operation has an exp prefix", "exp.arbitrate", types.ContractSchemaAndInvariantChecked, false, true},
		{"redaction of text", "redact.text", types.ContractSchemaAndInvariantChecked, false, true},
		{"the call path's name for redaction", "redact_llm", types.ContractSchemaAndInvariantChecked, false, true},
		{"the call path's name for a settlement", "negotiate", types.ContractJSONWellFormed, false, true},
		{"a name with no row", "nonsense", 0, false, false},
		{"the empty name", "", 0, false, false},
		{"a name is not matched in another case", "Filter", 0, false, false},
		{"a descriptor's own name is not a row", "op.invoice", 0, false, false},
		{"a name with a space", " filter", 0, false, false},
		{"an operation that asks no model", "chunk", 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, known := ContractLevelOf(tc.operation)
			if known != tc.known || rule.Level != tc.level || rule.Computed != tc.computed {
				t.Errorf("ContractLevelOf(%q) = %+v, %v; want level %s, computed %v, known %v",
					tc.operation, rule, known, tc.level, tc.computed, tc.known)
			}
		})
	}
}

func TestContractLevelsHoldsEveryRowAndCopies(t *testing.T) {
	levels := ContractLevels()
	if len(levels) != len(contractRows) {
		t.Fatalf("ContractLevels has %d rules for %d rows", len(levels), len(contractRows))
	}
	for i := range contractRows {
		rule, ok := levels[contractRows[i].name]
		if !ok || rule.Level != contractRows[i].level || rule.Computed != contractRows[i].computed {
			t.Errorf("%s: ContractLevels says %+v, %v", contractRows[i].name, rule, ok)
		}
	}
	delete(levels, "filter")
	if _, ok := ContractLevelOf("filter"); !ok {
		t.Error("changing the map ContractLevels returned changed the table")
	}
	names := contractNames()
	if !sort.StringsAreSorted(names) || len(names) != len(contractRows) {
		t.Error("contractNames is not the sorted names of every row")
	}
}

func TestContractLevelOfAllocatesNothing(t *testing.T) {
	for _, operation := range []string{"filter", "extract", "redact_llm", "op.run", "nonsense", "", strings.Repeat("x", 200)} {
		allocations := testing.AllocsPerRun(1000, func() {
			ContractLevelOf(operation)
		})
		if allocations != 0 {
			t.Errorf("ContractLevelOf(%.20q) allocated %v times", operation, allocations)
		}
	}
}

// Generate delivers schema constrained only for a type a strict schema can
// describe. This test used to hold that a map, and an interface, were schema
// constrained: the level followed the kind of the type alone, while the request
// sent no schema for either (jsonSchemaFor declines them), so the Record said a
// level the call had not earned and a schema-constrained floor admitted it
// (ruling R31).
func TestGenerateLevelIsTheLevelOfTheTypeArgument(t *testing.T) {
	type invoice struct{ Number string }
	type recursive struct{ Next *recursive }
	cases := []struct {
		name string
		got  types.ContractLevel
		want types.ContractLevel
	}{
		{"a string is the model's text", ContractLevelFor[string]("generate"), types.ContractPromptOnly},
		{"a named integer type has a schema", ContractLevelFor[types.Speed]("generate"), types.ContractSchemaConstrained},
		{"a struct has a schema", ContractLevelFor[invoice]("generate"), types.ContractSchemaConstrained},
		{"a pointer to a struct has a schema", ContractLevelFor[*invoice]("generate"), types.ContractSchemaConstrained},
		{"a slice has a schema", ContractLevelFor[[]invoice]("generate"), types.ContractSchemaConstrained},
		{"an integer has a schema", ContractLevelFor[int]("generate"), types.ContractSchemaConstrained},
		{"a map has none", ContractLevelFor[map[string]string]("generate"), types.ContractJSONWellFormed},
		{"a map of any has none", ContractLevelFor[map[string]any]("generate"), types.ContractJSONWellFormed},
		{"a slice of maps has none", ContractLevelFor[[]map[string]string]("generate"), types.ContractJSONWellFormed},
		{"a recursive type has none", ContractLevelFor[recursive]("generate"), types.ContractJSONWellFormed},
		{"a boolean has a schema", ContractLevelFor[bool]("generate"), types.ContractSchemaConstrained},
		{"a byte slice has a schema", ContractLevelFor[[]byte]("generate"), types.ContractSchemaConstrained},
		{"an interface has none", ContractLevelFor[any]("generate"), types.ContractJSONWellFormed},
		{"the rule's lowest is prompt only", func() types.ContractLevel { r, _ := ContractLevelOf("generate"); return r.Level }(), types.ContractPromptOnly},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, tc.got, tc.want)
		}
	}
}

// contractConstants are the constants the code already has for the level a
// result delivers. A row that is not equal to its constant has to say why.
func TestRowsAgreeWithTheConstantsTheCodeAlreadyHas(t *testing.T) {
	constants := map[string]types.ContractLevel{
		"summarizedetail": textResultContract, "rewritedetail": textResultContract,
		"translatedetail": textResultContract, "expanddetail": textResultContract,
		"sort": sortContract, "audit": auditResultContract, "redact.text": redactLLMResultContract,
		"is": assessResultContract, "isdetail": assessResultContract, "which": assessResultContract,
		"whichdetail": assessResultContract, "whichornone": assessResultContract, "whichornonedetail": assessResultContract,
		"rate": assessResultContract, "ratedetail": assessResultContract, "assess": assessResultContract,
		"assessdetail": assessResultContract,
		"extract":      requestedContractOf(types.OpOptions{}),
	}
	// Where the table is lower than the code claims, and why. No row is: Sort
	// claimed schema and invariants checked without declaring a schema, and now
	// claims the table's level (ruling R31).
	acknowledged := map[string]string{}
	for name, constant := range constants {
		rule, known := ContractLevelOf(name)
		if !known {
			t.Errorf("%s has a constant and no row", name)
			continue
		}
		reason, differs := acknowledged[name]
		switch {
		case rule.Level != constant && !differs:
			t.Errorf("%s: the row says %s and the code's constant %s, with no reason given", name, rule.Level, constant)
		case rule.Level == constant && differs:
			t.Errorf("%s: the row now equals the constant, and still lists a reason it differs: %s", name, reason)
		case differs && rule.Level >= constant:
			t.Errorf("%s: an acknowledged difference must be a row lower than the constant", name)
		}
	}
	if ContractLevelFor[string]("generate") != types.ContractPromptOnly {
		t.Error("the table's generate row is the level of a string")
	}
}

func TestEveryDifferenceFromTableA2IsExplained(t *testing.T) {
	for name, reason := range differsFromTableA2 {
		if !strings.HasPrefix(reason, "A2") {
			t.Errorf("%s: the explanation does not say what A2 had: %q", name, reason)
		}
		if _, known := ContractLevelOf(name); !known {
			t.Errorf("%s is explained and is not a row", name)
		}
	}
	// The rows the table raised or lowered, as the report to the coordinator
	// lists them; if one of these changes, the explanation has to be read again.
	for name, want := range map[string]types.ContractLevel{
		"sort": types.ContractJSONWellFormed, "exp.enrich": types.ContractJSONWellFormed,
		"exp.arbitrate": types.ContractSchemaAndInvariantChecked, "deduplicate": types.ContractSchemaAndInvariantChecked,
		"exp.decompose": types.ContractSchemaAndInvariantChecked, "flow.decide": types.ContractSchemaAndInvariantChecked,
		"verify": types.ContractSchemaAndInvariantChecked,
	} {
		if rule, _ := ContractLevelOf(name); rule.Level != want {
			t.Errorf("%s: %s, and the explanation of the difference from A2 is for %s", name, rule.Level, want)
		}
		if _, explained := differsFromTableA2[name]; !explained {
			t.Errorf("%s differs from A2 and is not explained", name)
		}
	}
}

// ---------------------------------------------------------------------------
// The rows held to the code. Each test drives the operation with a scripted
// provider and asserts the fact its row rests on: whether the request carried a
// schema, what the reader refuses, and which rule of the operation Go checks.
// ---------------------------------------------------------------------------

// contractProbe is a provider that answers from a script and keeps every request
// it was sent. It answers the nth request with the nth answer and the last
// answer to every request after.
type contractProbe struct {
	mu      sync.Mutex
	answers []string
	seen    []types.OpOptions
	users   []string
}

func newContractProbe(t *testing.T, answers ...string) *contractProbe {
	t.Helper()
	probe := &contractProbe{answers: answers}
	swapLLMCaller(t, func(_ context.Context, _, user string, opt types.OpOptions) (string, error) {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		probe.seen = append(probe.seen, opt)
		probe.users = append(probe.users, user)
		position := len(probe.seen) - 1
		if position >= len(probe.answers) {
			position = len(probe.answers) - 1
		}
		return probe.answers[position], nil
	})
	return probe
}

// requests is how many requests were sent.
func (p *contractProbe) requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// first is the options of the first request.
func (p *contractProbe) first(t *testing.T) types.OpOptions {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 {
		t.Fatal("no request was sent")
	}
	return p.seen[0]
}

// declaredNoSchema asserts that no request carried a schema.
func (p *contractProbe) declaredNoSchema(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 {
		t.Fatal("no request was sent")
	}
	for i, opt := range p.seen {
		if opt.JSONSchema != nil || opt.SchemaName != "" {
			t.Errorf("request %d declared the schema %q, and the row says none is", i+1, opt.SchemaName)
		}
	}
}

// declaredSchema asserts that the first request declared a schema under name,
// strict, with every property required, and returns it.
func (p *contractProbe) declaredSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	opt := p.first(t)
	if opt.JSONSchema == nil || opt.SchemaName != name {
		t.Fatalf("the request declared %q with schema %v, want a schema named %q", opt.SchemaName, opt.JSONSchema != nil, name)
	}
	contractAssertStrict(t, opt.JSONSchema, "$")
	return opt.JSONSchema
}

// contractAssertStrict holds a schema to strict mode: every object closed and
// every property listed as required.
func contractAssertStrict(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	if schema["type"] == "object" {
		if schema["additionalProperties"] != false {
			t.Errorf("%s is an object that is not closed", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		required := map[string]bool{}
		switch listed := schema["required"].(type) {
		case []string:
			for _, name := range listed {
				required[name] = true
			}
		case []any:
			for _, name := range listed {
				required[name.(string)] = true
			}
		}
		for name, inner := range properties {
			if !required[name] {
				t.Errorf("%s.%s is not required", path, name)
			}
			if object, ok := inner.(map[string]any); ok {
				contractAssertStrict(t, object, path+"."+name)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		contractAssertStrict(t, items, path+"[]")
	}
}

// contractRowLevel asserts the level and kind of the row a test is written for.
func contractRowLevel(t *testing.T, operation string, level types.ContractLevel, computed bool) {
	t.Helper()
	rule, known := ContractLevelOf(operation)
	if !known || rule.Level != level || rule.Computed != computed {
		t.Fatalf("the row of %s is %+v, %v; the test is written for level %s, computed %v", operation, rule, known, level, computed)
	}
}

func TestFilterRowIsWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "filter", types.ContractJSONWellFormed, false)
	items := []string{"red", "green", "blue"}
	options := NewFilterOptions().WithCriteria("warm colours")

	probe := newContractProbe(t, `{"ids":["i-000001","i-000003"]}`)
	kept, err := Filter(items, options)
	if err != nil || !reflect.DeepEqual(kept, []string{"red", "blue"}) {
		t.Fatalf("Filter = %v, %v", kept, err)
	}
	probe.declaredNoSchema(t)

	for name, answer := range map[string]string{
		"an id that was not offered":    `{"ids":["i-000009"]}`,
		"an id twice":                   `{"ids":["i-000001","i-000001"]}`,
		"more ids than items":           `{"ids":["i-000001","i-000002","i-000003","i-000001"]}`,
		"no ids member":                 `{"kept":["i-000001"]}`,
		"a null list":                   `{"ids":null}`,
		"an empty object":               `{}`,
		"a bare list":                   `["i-000001"]`,
		"text that is not JSON":         `red and blue`,
		"an id that is the item itself": `{"ids":["red"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if kept, err := Filter(items, options); err == nil || kept != nil {
				t.Errorf("Filter accepted %s: %v, %v", name, kept, err)
			}
		})
	}
	t.Run("an empty list is an answer", func(t *testing.T) {
		newContractProbe(t, `{"ids":[]}`)
		if kept, err := Filter(items, options); err != nil || len(kept) != 0 {
			t.Errorf("Filter = %v, %v", kept, err)
		}
	})
	t.Run("no items send nothing", func(t *testing.T) {
		probe := newContractProbe(t, `{"ids":[]}`)
		if _, err := Filter([]string{}, options); err != nil || probe.requests() != 0 {
			t.Errorf("Filter of nothing sent %d requests, err %v", probe.requests(), err)
		}
	})
}

func TestChooseRowIsWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "choose", types.ContractJSONWellFormed, false)
	options := []string{"alpha", "beta", "gamma"}

	probe := newContractProbe(t, `{"id":"i-000002"}`)
	chosen, err := Choose(options, NewChooseOptions())
	if err != nil || chosen != "beta" {
		t.Fatalf("Choose = %q, %v", chosen, err)
	}
	probe.declaredNoSchema(t)

	for name, answer := range map[string]string{
		"an id that was not offered": `{"id":"i-000099"}`,
		"the option itself":          `{"id":"beta"}`,
		"no id member":               `{}`,
		"a null id":                  `{"id":null}`,
		"an id that is a number":     `{"id":2}`,
		"text":                       `beta`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if chosen, err := Choose(options, NewChooseOptions()); err == nil {
				t.Errorf("Choose accepted %s: %q", name, chosen)
			}
		})
	}
	t.Run("one option is returned and nothing is sent", func(t *testing.T) {
		probe := newContractProbe(t, `{"id":"i-000009"}`)
		chosen, err := Choose([]string{"only"}, NewChooseOptions())
		if err != nil || chosen != "only" || probe.requests() != 0 {
			t.Errorf("Choose = %q, %v after %d requests", chosen, err, probe.requests())
		}
	})
}

func TestSortDeliversWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "sort", types.ContractJSONWellFormed, false)
	items := []string{"low", "critical", "medium"}
	options := NewSortOptions().WithCriteria("by urgency")

	probe := newContractProbe(t, `{"ids":["i-000002","i-000003","i-000001"]}`)
	result, err := SortResult(items, options)
	if err != nil || !reflect.DeepEqual(result.Value, []string{"critical", "medium", "low"}) {
		t.Fatalf("SortResult = %v, %v", result.Value, err)
	}
	probe.declaredNoSchema(t)
	if result.Meta.Strategy != "whole-list" {
		t.Errorf("a permutation was read as strategy %q", result.Meta.Strategy)
	}
	// The call declared no schema, so what it says it requested and delivered is
	// the row's level and no more, although it checked a permutation.
	rule, _ := ContractLevelOf("sort")
	if result.Meta.RequestedContract != rule.Level || result.Meta.DeliveredContract != rule.Level {
		t.Errorf("SortResult requested %s and delivered %s, and the row is %s",
			result.Meta.RequestedContract, result.Meta.DeliveredContract, rule.Level)
	}

	t.Run("an answer that is not a permutation goes to the scoring path", func(t *testing.T) {
		for name, answer := range map[string]string{
			"an id twice and one missing": `{"ids":["i-000001","i-000001","i-000002"]}`,
			"an id that was not offered":  `{"ids":["i-000001","i-000002","i-000007"]}`,
			"too few ids":                 `{"ids":["i-000001"]}`,
			"no ids":                      `{"order":["i-000001"]}`,
		} {
			probe := newContractProbe(t, answer, `{"rank_score":0.9}`)
			result, err := SortResult(items, options)
			if err != nil || result.Meta.Strategy != "scoring-fallback" {
				t.Errorf("%s: strategy %q, err %v", name, result.Meta.Strategy, err)
			}
			if probe.requests() != 1+len(items) {
				t.Errorf("%s: %d requests, want the refused answer and one score each", name, probe.requests())
			}
			probe.declaredNoSchema(t)
		}
	})
	t.Run("the scoring path refuses a score off its scale", func(t *testing.T) {
		newContractProbe(t, `{"ids":["i-000001"]}`, `{"rank_score":12}`)
		if _, err := Sort(items, options); err == nil {
			t.Error("a rank_score of 12 was accepted")
		}
		newContractProbe(t, `{"ids":["i-000001"]}`, `{}`)
		if _, err := Sort(items, options); err == nil {
			t.Error("a missing rank_score was accepted")
		}
	})
	t.Run("more than fifty items go straight to scoring and no permutation is checked", func(t *testing.T) {
		many := make([]int, sortScoringThreshold+1)
		probe := newContractProbe(t, `{"rank_score":0.5}`)
		result, err := SortResult(many, NewSortOptions().WithCriteria("by size"))
		if err != nil || result.Meta.Strategy != "scoring" || len(result.Value) != len(many) {
			t.Fatalf("strategy %q, %d items, err %v", result.Meta.Strategy, len(result.Value), err)
		}
		if probe.requests() != len(many) {
			t.Errorf("%d requests for %d items", probe.requests(), len(many))
		}
		probe.declaredNoSchema(t)
	})
	t.Run("fewer than two items send nothing", func(t *testing.T) {
		probe := newContractProbe(t, `{"ids":[]}`)
		if _, err := Sort([]string{"one"}, options); err != nil || probe.requests() != 0 {
			t.Errorf("a single item sent %d requests, err %v", probe.requests(), err)
		}
	})
}

func TestRankRowIsWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "rank", types.ContractJSONWellFormed, false)
	items := []string{"a", "b", "c"}
	options := NewRankOptions().WithQuery("relevance")

	probe := newContractProbe(t, `{"rankings":[{"index":2,"score":0.9},{"index":0,"score":0.5},{"index":1,"score":0.1}]}`)
	result, err := Rank(items, options)
	if err != nil || len(result.Items) != 3 || result.Items[0].Item != "c" {
		t.Fatalf("Rank = %+v, %v", result.Items, err)
	}
	probe.declaredNoSchema(t)

	for name, answer := range map[string]string{
		"an item missing":       `{"rankings":[{"index":0,"score":0.9},{"index":1,"score":0.5}]}`,
		"an item twice":         `{"rankings":[{"index":0,"score":0.9},{"index":0,"score":0.5},{"index":1,"score":0.1}]}`,
		"an index not offered":  `{"rankings":[{"index":0,"score":0.9},{"index":1,"score":0.5},{"index":7,"score":0.1}]}`,
		"a score above one":     `{"rankings":[{"index":0,"score":1.5},{"index":1,"score":0.5},{"index":2,"score":0.1}]}`,
		"a score missing":       `{"rankings":[{"index":0},{"index":1,"score":0.5},{"index":2,"score":0.1}]}`,
		"a null list":           `{"rankings":null}`,
		"no rankings member":    `{"scores":[]}`,
		"an index that is text": `{"rankings":[{"index":"0","score":0.9},{"index":1,"score":0.5},{"index":2,"score":0.1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if result, err := Rank(items, options); err == nil {
				t.Errorf("Rank accepted %s: %+v", name, result.Items)
			}
		})
	}
}

func TestClusterRowIsWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "cluster", types.ContractJSONWellFormed, false)
	items := []string{"a", "b", "c", "d"}
	options := NewClusterOptions()
	cluster := func(indices string, outliers string) string {
		return `{"clusters":[{"name":"one","indices":` + indices + `,"keywords":["k"]}],"outlier_indices":` + outliers + `,"quality":0.8}`
	}

	probe := newContractProbe(t, cluster(`[0,1,2]`, `[3]`))
	result, err := Cluster(items, options)
	if err != nil || len(result.Clusters) != 1 || result.Clusters[0].Size != 3 {
		t.Fatalf("Cluster = %+v, %v", result, err)
	}
	probe.declaredNoSchema(t)

	for name, answer := range map[string]string{
		"an item in no group":       cluster(`[0,1]`, `[3]`),
		"an item in two groups":     cluster(`[0,1,2,3]`, `[3]`),
		"an index out of range":     cluster(`[0,1,2,9]`, `[3]`),
		"no outlier_indices member": `{"clusters":[{"name":"one","indices":[0,1,2,3],"keywords":[]}],"quality":0.5}`,
		"no quality member":         `{"clusters":[{"name":"one","indices":[0,1,2,3],"keywords":[]}],"outlier_indices":[]}`,
		"a cluster with no indices": `{"clusters":[{"name":"one","keywords":[]}],"outlier_indices":[0,1,2,3],"quality":0.5}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if _, err := Cluster(items, options); err == nil {
				t.Errorf("Cluster accepted %s", name)
			}
		})
	}
	t.Run("outliers are refused when they are not allowed", func(t *testing.T) {
		newContractProbe(t, cluster(`[0,1,2]`, `[3]`))
		options := NewClusterOptions()
		options.IncludeOutliers = false
		if _, err := Cluster(items, options); err == nil {
			t.Error("an outlier was accepted when none was allowed")
		}
	})
	t.Run("one item is its own cluster and nothing is sent", func(t *testing.T) {
		probe := newContractProbe(t, cluster(`[0]`, `[]`))
		if result, err := Cluster([]string{"only"}, options); err != nil || len(result.Clusters) != 1 || probe.requests() != 0 {
			t.Errorf("Cluster of one sent %d requests, err %v", probe.requests(), err)
		}
	})
}

func TestSemanticMatchRowIsWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "semanticmatch", types.ContractJSONWellFormed, false)
	sources := []string{"s0", "s1"}
	targets := []string{"t0", "t1"}
	options := NewMatchOptions()

	probe := newContractProbe(t, `{"matches":[{"source_index":0,"target_index":1,"score":0.9,"explanation":"x","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0]}`)
	result, err := SemanticMatch(sources, targets, options)
	if err != nil || result.TotalMatches != 1 {
		t.Fatalf("SemanticMatch = %+v, %v", result, err)
	}
	probe.declaredNoSchema(t)

	pair := `{"source_index":0,"target_index":1,"score":0.9,"explanation":"x","is_partial":false}`
	for name, answer := range map[string]string{
		"an unmatched list that omits an item": `{"matches":[` + pair + `],"unmatched_sources":[],"unmatched_targets":[0]}`,
		"an unmatched item that is in a pair":  `{"matches":[` + pair + `],"unmatched_sources":[0,1],"unmatched_targets":[0]}`,
		"a source out of range":                `{"matches":[{"source_index":5,"target_index":1,"score":0.9,"explanation":"x","is_partial":false}],"unmatched_sources":[0,1],"unmatched_targets":[0]}`,
		"a score above one":                    `{"matches":[{"source_index":0,"target_index":1,"score":3,"explanation":"x","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0]}`,
		"a score below the threshold":          `{"matches":[{"source_index":0,"target_index":1,"score":0.1,"explanation":"x","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0]}`,
		"the same pair twice":                  `{"matches":[` + pair + `,` + pair + `],"unmatched_sources":[1],"unmatched_targets":[0]}`,
		"no matches member":                    `{"unmatched_sources":[0,1],"unmatched_targets":[0,1]}`,
		"an index that is not a whole number":  `{"matches":[{"source_index":0.5,"target_index":1,"score":0.9,"explanation":"x","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if _, err := SemanticMatch(sources, targets, options); err == nil {
				t.Errorf("SemanticMatch accepted %s", name)
			}
		})
	}
	t.Run("an empty side sends nothing", func(t *testing.T) {
		probe := newContractProbe(t, `{}`)
		result, err := SemanticMatch(sources, []string{}, options)
		if err != nil || probe.requests() != 0 || len(result.UnmatchedSources) != 2 {
			t.Errorf("SemanticMatch with no targets sent %d requests: %+v, %v", probe.requests(), result, err)
		}
	})
}

func TestClassifyRowIsWellFormedBecauseNoSchemaIsDeclared(t *testing.T) {
	contractRowLevel(t, "classify", types.ContractJSONWellFormed, false)
	options := NewClassifyOptions().WithCategories([]string{"urgent", "routine"})
	answer := func(category string) string {
		return `{"category":"` + category + `","confidence":0.9,"alternatives":[],"reasoning":"because"}`
	}

	probe := newContractProbe(t, answer("Urgent"))
	result, err := Classify[string, string]("the server is down", options)
	if err != nil || result.Category != "urgent" {
		t.Fatalf("Classify = %+v, %v", result, err)
	}
	probe.declaredNoSchema(t)

	for name, body := range map[string]string{
		"a category that was not offered": answer("spam"),
		"no category member":              `{"confidence":0.9,"alternatives":[],"reasoning":"x"}`,
		"no confidence member":            `{"category":"urgent","alternatives":[],"reasoning":"x"}`,
		"a confidence above one":          `{"category":"urgent","confidence":4,"alternatives":[],"reasoning":"x"}`,
		"a confidence below the floor":    `{"category":"urgent","confidence":0.1,"alternatives":[],"reasoning":"x"}`,
		"no reasoning member":             `{"category":"urgent","confidence":0.9,"alternatives":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, body)
			if result, err := Classify[string, string]("the server is down", options); err == nil {
				t.Errorf("Classify accepted %s: %+v", name, result)
			}
		})
	}
	t.Run("an alternative that was not offered is dropped and not refused", func(t *testing.T) {
		newContractProbe(t, `{"category":"urgent","confidence":0.9,"alternatives":[{"category":"spam","confidence":0.4}],"reasoning":"x"}`)
		result, err := Classify[string, string]("the server is down", options)
		if err != nil || len(result.Alternatives) != 0 {
			t.Errorf("Classify = %+v, %v", result, err)
		}
	})
}

func TestSimilarRowDeclaresItsSchemaAndReadsEveryRequiredMember(t *testing.T) {
	contractRowLevel(t, "similar", types.ContractSchemaConstrained, false)
	probe := newContractProbe(t, `{"score":0.9,"explanation":"alike"}`)
	result, err := Similar("a cat", "a kitten", NewSimilarOptions())
	if err != nil || !result.IsSimilar {
		t.Fatalf("Similar = %+v, %v", result, err)
	}
	schema := probe.declaredSchema(t, "similarity")
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["score"]; !ok {
		t.Errorf("the schema does not ask for a score: %v", schema)
	}
	for name, answer := range map[string]string{
		"no explanation":  `{"score":0.9}`,
		"no score":        `{"explanation":"alike"}`,
		"a score above 1": `{"score":7,"explanation":"alike"}`,
		"a blank reason":  `{"score":0.9,"explanation":"  "}`,
		"a bare true":     `true`,
		"a score as text": `{"score":"high","explanation":"alike"}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if _, err := Similar("a cat", "a kitten", NewSimilarOptions()); err == nil {
				t.Errorf("Similar accepted %s", name)
			}
		})
	}
}

// The decisions: one schema, one reader, the same checks in the plain form and
// the Detail form, apart from the confidence member (ruling R2).
func TestDecisionRowsShareOneSchemaAndOneReader(t *testing.T) {
	for _, operation := range []string{"is", "isdetail", "which", "whichdetail", "whichornone", "whichornonedetail", "rate", "ratedetail", "assess", "assessdetail"} {
		contractRowLevel(t, operation, types.ContractSchemaAndInvariantChecked, false)
	}
	ctx := context.Background()
	labels := []string{"billing", "technical", "shipping"}

	schemaOf := func(answer string, run func() error) map[string]any {
		t.Helper()
		probe := newContractProbe(t, answer)
		if err := run(); err != nil {
			t.Fatalf("the decision was refused: %v", err)
		}
		return probe.declaredSchema(t, "assessment")
	}
	// withoutConfidence is the schema with the confidence member taken out.
	withoutConfidence := func(schema map[string]any) map[string]any {
		clone := map[string]any{}
		for key, value := range schema {
			clone[key] = value
		}
		properties := map[string]any{}
		for key, value := range schema["properties"].(map[string]any) {
			if key != "confidence" {
				properties[key] = value
			}
		}
		clone["properties"] = properties
		var required []string
		if listed, ok := schema["required"].([]string); ok {
			for _, name := range listed {
				if name != "confidence" {
					required = append(required, name)
				}
			}
		}
		clone["required"] = required
		return clone
	}
	for _, tc := range []struct {
		name        string
		plain, rich func() error
		plainAnswer string
		richAnswer  string
	}{
		{"is",
			func() error { _, err := Is(ctx, "x", "Is it?"); return err },
			func() error { _, err := IsResult(ctx, "x", "Is it?"); return err },
			`{"q1":true}`, `{"q1":true,"confidence":0.8}`},
		{"which",
			func() error { _, err := Which(ctx, "x", "Which team?", labels); return err },
			func() error { _, err := WhichResult(ctx, "x", "Which team?", labels); return err },
			`{"q1":2}`, `{"q1":2,"confidence":0.8}`},
		{"rate",
			func() error { _, err := Rate(ctx, "x", "How urgent?", 1, 5); return err },
			func() error { _, err := RateResult(ctx, "x", "How urgent?", 1, 5); return err },
			`{"q1":4}`, `{"q1":4,"confidence":0.8}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := schemaOf(tc.plainAnswer, tc.plain)
			rich := schemaOf(tc.richAnswer, tc.rich)
			if _, ok := plain["properties"].(map[string]any)["confidence"]; ok {
				t.Error("the plain form asks for a confidence")
			}
			if _, ok := rich["properties"].(map[string]any)["confidence"]; !ok {
				t.Error("the Detail form does not ask for a confidence")
			}
			if !reflect.DeepEqual(plain, withoutConfidence(rich)) {
				t.Errorf("the schemas differ in more than the confidence:\nplain  %v\ndetail %v", plain, rich)
			}
		})
	}

	// The same answers are refused by both forms.
	refusedByBoth := map[string]map[string]string{
		"is": {
			"no answer":            `{}`,
			"a string for a bool":  `{"q1":"yes","confidence":0.8}`,
			"a null":               `{"q1":null,"confidence":0.8}`,
			"an answer twice":      `{"q1":true,"q1":false,"confidence":0.8}`,
			"a question not asked": `{"q1":true,"q2":true,"confidence":0.8}`,
		},
		"which": {
			"an option past the last": `{"q1":4,"confidence":0.8}`,
			"option zero":             `{"q1":0,"confidence":0.8}`,
			"the label itself":        `{"q1":"billing","confidence":0.8}`,
			"a fraction":              `{"q1":1.5,"confidence":0.8}`,
		},
		"rate": {
			"a level above the scale": `{"q1":9,"confidence":0.8}`,
			"a level below the scale": `{"q1":0,"confidence":0.8}`,
			"a level as text":         `{"q1":"4","confidence":0.8}`,
		},
	}
	runPlain := map[string]func() error{
		"is":    func() error { _, err := Is(ctx, "x", "Is it?"); return err },
		"which": func() error { _, err := Which(ctx, "x", "Which team?", labels); return err },
		"rate":  func() error { _, err := Rate(ctx, "x", "How urgent?", 1, 5); return err },
	}
	runDetail := map[string]func() error{
		"is":    func() error { _, err := IsResult(ctx, "x", "Is it?"); return err },
		"which": func() error { _, err := WhichResult(ctx, "x", "Which team?", labels); return err },
		"rate":  func() error { _, err := RateResult(ctx, "x", "How urgent?", 1, 5); return err },
	}
	for operation, answers := range refusedByBoth {
		for name, answer := range answers {
			t.Run(operation+" refuses "+name, func(t *testing.T) {
				newContractProbe(t, answer)
				if runPlain[operation]() == nil {
					t.Error("the plain form accepted it")
				}
				if runDetail[operation]() == nil {
					t.Error("the Detail form accepted it")
				}
			})
		}
	}
	t.Run("only the Detail form requires the confidence", func(t *testing.T) {
		newContractProbe(t, `{"q1":true}`)
		if err := runPlain["is"](); err != nil {
			t.Errorf("the plain form refused an answer with no confidence: %v", err)
		}
		if err := runDetail["is"](); err == nil {
			t.Error("the Detail form accepted an answer with no confidence")
		}
		newContractProbe(t, `{"q1":true,"confidence":3}`)
		if err := runDetail["is"](); err == nil {
			t.Error("the Detail form accepted a confidence of 3")
		}
	})
	t.Run("the Detail form delivers the level the row says", func(t *testing.T) {
		newContractProbe(t, `{"q1":true,"confidence":0.8}`)
		result, err := IsResult(ctx, "x", "Is it?")
		if err != nil || result.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
			t.Errorf("IsResult delivered %s, %v", result.Meta.DeliveredContract, err)
		}
	})
}

// contractTypedProvider answers a decision the way a decision provider does:
// typed, with no text, from what it reads off the request's Decision.
type contractTypedProvider struct {
	mu      sync.Mutex
	answer  func(questions []llm.DecisionQuestion) []llm.DecisionAnswer
	request llm.CompletionRequest
}

func (p *contractTypedProvider) Name() string                               { return "local" }
func (p *contractTypedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *contractTypedProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *contractTypedProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.request = req
	if req.Decision == nil {
		return llm.CompletionResponse{}, errors.New("a request that decides nothing")
	}
	return llm.CompletionResponse{Provider: "local", Model: "typed-1", DecisionAnswers: p.answer(req.Decision.Questions)}, nil
}

// What a decision provider's answer is delivered with. The reading: it is typed,
// so it fits its question by construction, and llm.RenderDecisionAnswers and the
// decision reader hold it to the question, so it is delivered at the level of
// the decisions. The test shows the refusals and the request that was sent.
func TestADecisionProviderAnswersTypedAndIsHeldToTheQuestion(t *testing.T) {
	labels := []string{"billing", "technical", "shipping"}
	answering := func(change func(q llm.DecisionQuestion, a *llm.DecisionAnswer)) *contractTypedProvider {
		return &contractTypedProvider{answer: func(questions []llm.DecisionQuestion) []llm.DecisionAnswer {
			answers := make([]llm.DecisionAnswer, len(questions))
			for i, q := range questions {
				answers[i] = llm.DecisionAnswer{Kind: q.Kind, ModelConfidence: 0.9, ConfidenceReported: true}
				switch q.Kind {
				case llm.DecisionYesNo:
					answers[i].Yes = true
				case llm.DecisionChoice:
					answers[i].Label = q.Options[1]
				case llm.DecisionScale:
					answers[i].Level = q.High
				}
				if change != nil {
					change(q, &answers[i])
				}
			}
			return answers
		}}
	}
	call := func(p *contractTypedProvider) (string, error) {
		return Which(WithProvider(context.Background(), p), "x", "Which team?", labels)
	}

	t.Run("a label that was offered is delivered", func(t *testing.T) {
		p := answering(nil)
		got, err := call(p)
		if err != nil || got != "technical" {
			t.Fatalf("Which = %q, %v", got, err)
		}
		if p.request.Decision == nil || len(p.request.Decision.Questions) != 1 {
			t.Fatalf("the request carried the decision %+v", p.request.Decision)
		}
		// The request also declares the schema a generating model would be held
		// to; a decision provider does not read it.
		if p.request.JSONSchema == nil || p.request.SchemaName != "assessment" {
			t.Errorf("the request declared %q", p.request.SchemaName)
		}
	})
	t.Run("the result form says so", func(t *testing.T) {
		p := answering(nil)
		result, err := WhichResult(WithProvider(context.Background(), p), "x", "Which team?", labels)
		if err != nil || result.Value.Answer != "technical" || result.Value.ModelConfidence != 0.9 {
			t.Fatalf("WhichResult = %+v, %v", result.Value, err)
		}
		if result.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
			t.Errorf("delivered %s", result.Meta.DeliveredContract)
		}
	})
	for name, change := range map[string]func(q llm.DecisionQuestion, a *llm.DecisionAnswer){
		"a label that was not offered": func(_ llm.DecisionQuestion, a *llm.DecisionAnswer) { a.Label = "legal" },
		"an answer of another kind":    func(_ llm.DecisionQuestion, a *llm.DecisionAnswer) { a.Kind = llm.DecisionYesNo },
		"a probability above one":      func(_ llm.DecisionQuestion, a *llm.DecisionAnswer) { a.ModelConfidence = 1.5 },
		"no kind at all":               func(_ llm.DecisionQuestion, a *llm.DecisionAnswer) { a.Kind = 0 },
	} {
		t.Run("a provider that gives "+name+" is refused", func(t *testing.T) {
			if got, err := call(answering(change)); err == nil {
				t.Errorf("Which accepted %s: %q", name, got)
			}
		})
	}
	t.Run("a level off the scale is refused", func(t *testing.T) {
		p := answering(func(q llm.DecisionQuestion, a *llm.DecisionAnswer) { a.Level = q.High + 4 })
		if got, err := Rate(WithProvider(context.Background(), p), "x", "How urgent?", 1, 5); err == nil {
			t.Errorf("Rate accepted a level off its scale: %d", got)
		}
	})
	t.Run("fewer answers than questions are refused", func(t *testing.T) {
		p := &contractTypedProvider{answer: func([]llm.DecisionQuestion) []llm.DecisionAnswer {
			return []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: true}}
		}}
		if _, err := Rate(WithProvider(context.Background(), p), "x", "How urgent?", 1, 5); err == nil {
			t.Error("a provider that answered a different question was accepted")
		}
	})
}

// Extract's level is computed from its options: the schema is declared and the
// reader is weak, and Strict, ExactFields or CompleteFields add the checks that
// lift it.
func TestExtractRowIsComputedFromItsOptions(t *testing.T) {
	contractRowLevel(t, "extract", types.ContractSchemaConstrained, true)
	whole := `{"Name":"Ada","Age":36}`
	partial := `{"Name":"Ada"}`

	probe := newContractProbe(t, whole)
	result, err := ExtractResult[Person]("Ada, 36", NewExtractOptions())
	if err != nil || result.Value.Age != 36 {
		t.Fatalf("ExtractResult = %+v, %v", result.Value, err)
	}
	schema := probe.declaredSchema(t, probe.first(t).SchemaName)
	if properties, _ := schema["properties"].(map[string]any); len(properties) != 2 {
		t.Errorf("the schema is not Person's: %v", schema)
	}
	if result.Meta.RequestedContract != types.ContractSchemaConstrained || result.Meta.DeliveredContract != types.ContractSchemaConstrained {
		t.Errorf("a default extraction requested %s and delivered %s", result.Meta.RequestedContract, result.Meta.DeliveredContract)
	}

	t.Run("the default reads the caller's type weakly", func(t *testing.T) {
		newContractProbe(t, partial)
		got, err := Extract[Person]("Ada", NewExtractOptions())
		if err != nil || got.Name != "Ada" || got.Age != 0 {
			t.Errorf("Extract = %+v, %v: a missing member is read as its zero value", got, err)
		}
		newContractProbe(t, `{"Other":1}`)
		if got, err := Extract[Person]("Ada", NewExtractOptions()); err == nil {
			t.Errorf("an answer that shares no member with the type was accepted: %+v", got)
		}
		newContractProbe(t, `{"Name":7,"Age":"old"}`)
		if got, err := Extract[Person]("Ada", NewExtractOptions()); err == nil {
			t.Errorf("an answer of the wrong kinds was accepted: %+v", got)
		}
	})
	for name, change := range map[string]func(o *ExtractOptions){
		"CompleteFields": func(o *ExtractOptions) { o.CommonOptions.CompleteFields = true },
		"ExactFields":    func(o *ExtractOptions) { o.CommonOptions.ExactFields = true },
		"Strict mode":    func(o *ExtractOptions) { o.CommonOptions.Mode = types.Strict },
	} {
		t.Run(name+" lifts the level", func(t *testing.T) {
			options := NewExtractOptions()
			change(&options)
			newContractProbe(t, whole)
			result, err := ExtractResult[Person]("Ada, 36", options)
			if err != nil || result.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
				t.Fatalf("delivered %s, %v", result.Meta.DeliveredContract, err)
			}
		})
	}
	t.Run("CompleteFields refuses a member that is missing", func(t *testing.T) {
		options := NewExtractOptions()
		options.CommonOptions.CompleteFields = true
		newContractProbe(t, partial)
		if got, err := Extract[Person]("Ada", options); err == nil {
			t.Errorf("an answer without Age was accepted: %+v", got)
		}
	})
	t.Run("ExactFields refuses a member the type does not have", func(t *testing.T) {
		options := NewExtractOptions()
		options.CommonOptions.ExactFields = true
		newContractProbe(t, `{"Name":"Ada","Age":36,"Extra":true}`)
		if got, err := Extract[Person]("Ada", options); err == nil {
			t.Errorf("an extra member was accepted: %+v", got)
		}
	})
	t.Run("a failed call delivers nothing", func(t *testing.T) {
		newContractProbe(t, `not json`)
		result, err := ExtractResult[Person]("Ada", NewExtractOptions())
		if err == nil || result.Meta.DeliveredContract != types.ContractPromptOnly {
			t.Errorf("a refused answer delivered %s, %v", result.Meta.DeliveredContract, err)
		}
	})
}

type contractSource struct {
	FullName string
	Years    int
}

func TestTransformRowDeclaresASchemaAndReadsTheCallersTypeWeakly(t *testing.T) {
	contractRowLevel(t, "transform", types.ContractSchemaConstrained, false)
	probe := newContractProbe(t, `{"Name":"Ada","Age":36}`)
	got, err := Transform[contractSource, Person](contractSource{"Ada", 36}, NewTransformOptions())
	if err != nil || got.Name != "Ada" {
		t.Fatalf("Transform = %+v, %v", got, err)
	}
	probe.declaredSchema(t, probe.first(t).SchemaName)

	newContractProbe(t, `{"Name":"Ada"}`)
	if got, err := Transform[contractSource, Person](contractSource{"Ada", 36}, NewTransformOptions()); err != nil || got.Age != 0 {
		t.Errorf("a missing member is read as its zero value: %+v, %v", got, err)
	}
	for name, answer := range map[string]string{
		"a member of another kind": `{"Name":true,"Age":"old"}`,
		"no shared member":         `{"Other":1}`,
		"null":                     `null`,
		"text":                     `Ada is 36`,
	} {
		newContractProbe(t, answer)
		if got, err := Transform[contractSource, Person](contractSource{"Ada", 36}, NewTransformOptions()); err == nil {
			t.Errorf("Transform accepted %s: %+v", name, got)
		}
	}
}

func TestGenerateRowIsPromptOnlyForAStringAndSchemaConstrainedForAType(t *testing.T) {
	contractRowLevel(t, "generate", types.ContractPromptOnly, true)

	probe := newContractProbe(t, "Once upon a time, {not json} at all")
	text, err := Generate[string]("write a story", NewGenerateOptions())
	if err != nil || text != "Once upon a time, {not json} at all" {
		t.Fatalf("Generate[string] = %q, %v", text, err)
	}
	probe.declaredNoSchema(t)
	result, err := GenerateResult[string]("write a story", NewGenerateOptions())
	if err != nil || result.Meta.DeliveredContract != types.ContractPromptOnly {
		t.Errorf("GenerateResult[string] delivered %s, %v", result.Meta.DeliveredContract, err)
	}

	probe = newContractProbe(t, `{"Name":"Ada","Age":36}`)
	person, err := Generate[Person]("a person", NewGenerateOptions())
	if err != nil || person.Name != "Ada" {
		t.Fatalf("Generate[Person] = %+v, %v", person, err)
	}
	probe.declaredSchema(t, probe.first(t).SchemaName)
	structured, err := GenerateResult[Person]("a person", NewGenerateOptions())
	if err != nil || structured.Meta.DeliveredContract != types.ContractSchemaConstrained {
		t.Errorf("GenerateResult[Person] delivered %s, %v", structured.Meta.DeliveredContract, err)
	}
	newContractProbe(t, "a person called Ada")
	if got, err := Generate[Person]("a person", NewGenerateOptions()); err == nil {
		t.Errorf("text was accepted as a Person: %+v", got)
	}

	t.Run("a type with no schema sends none", func(t *testing.T) {
		probe := newContractProbe(t, `{"a":"b"}`)
		got, err := Generate[map[string]string]("a map", NewGenerateOptions())
		if err != nil || got["a"] != "b" {
			t.Fatalf("Generate[map] = %v, %v", got, err)
		}
		probe.declaredNoSchema(t)
		// No schema was sent, so the level is the row's level for a type with none.
		if got := ContractLevelFor[map[string]string]("generate"); got != types.ContractJSONWellFormed {
			t.Errorf("the level of a map is %s, and no schema was sent", got)
		}
	})
}

func TestTextRowsPlainFormTakesTheTextAndDetailFormRequiresJSON(t *testing.T) {
	for operation, level := range map[string]types.ContractLevel{
		"summarize": types.ContractPromptOnly, "rewrite": types.ContractPromptOnly,
		"translate": types.ContractPromptOnly, "expand": types.ContractPromptOnly,
		"summarizedetail": types.ContractJSONWellFormed, "rewritedetail": types.ContractJSONWellFormed,
		"translatedetail": types.ContractJSONWellFormed, "expanddetail": types.ContractJSONWellFormed,
	} {
		contractRowLevel(t, operation, level, false)
	}

	probe := newContractProbe(t, "  A short summary, not JSON at all.  ")
	got, err := Summarize("a long text", NewSummarizeOptions())
	if err != nil || got != "A short summary, not JSON at all." {
		t.Fatalf("Summarize = %q, %v", got, err)
	}
	probe.declaredNoSchema(t)
	newContractProbe(t, "   ")
	if _, err := Summarize("a long text", NewSummarizeOptions()); err == nil {
		t.Error("an empty summary was accepted")
	}
	newContractProbe(t, "Hola")
	if got, err := Translate("Hello", NewTranslateOptions().WithTargetLanguage("Spanish")); err != nil || got != "Hola" {
		t.Errorf("Translate = %q, %v", got, err)
	}

	probe = newContractProbe(t, `{"text":"A summary.","key_points":["one"],"confidence":0.8}`)
	result, err := SummarizeResult("a long text", NewSummarizeOptions())
	if err != nil || result.Value.Text != "A summary." {
		t.Fatalf("SummarizeResult = %+v, %v", result.Value, err)
	}
	probe.declaredNoSchema(t)
	if result.Meta.DeliveredContract != types.ContractJSONWellFormed {
		t.Errorf("the Detail form delivered %s", result.Meta.DeliveredContract)
	}
	for name, answer := range map[string]string{
		"plain text":        "A summary.",
		"no key_points":     `{"text":"A summary.","confidence":0.8}`,
		"no confidence":     `{"text":"A summary.","key_points":[]}`,
		"no text":           `{"key_points":[],"confidence":0.8}`,
		"an empty text":     `{"text":" ","key_points":[],"confidence":0.8}`,
		"a confidence of 9": `{"text":"A summary.","key_points":[],"confidence":9}`,
		"a null text":       `{"text":null,"key_points":[],"confidence":0.8}`,
	} {
		t.Run("SummarizeResult refuses "+name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := SummarizeResult("a long text", NewSummarizeOptions()); err == nil {
				t.Errorf("accepted %s: %+v", name, got.Value)
			}
		})
	}
}

func TestCompleteFormatAndDiffRowsTakeTheModelsText(t *testing.T) {
	contractRowLevel(t, "complete", types.ContractPromptOnly, false)
	contractRowLevel(t, "format", types.ContractPromptOnly, false)

	probe := newContractProbe(t, " and then they lived happily")
	got, err := Complete(context.Background(), "Once upon a time", NewCompleteOptions())
	if err != nil || !strings.Contains(got.Text, "happily") {
		t.Fatalf("Complete = %+v, %v", got, err)
	}
	probe.declaredNoSchema(t)

	probe = newContractProbe(t, "  | a | b |  ")
	formatted, err := Format(map[string]int{"a": 1}, "a markdown table")
	if err != nil || formatted != "| a | b |" {
		t.Fatalf("Format = %q, %v", formatted, err)
	}
	probe.declaredNoSchema(t)

	t.Run("the legacy detail form of Format asks for JSON", func(t *testing.T) {
		contractRowLevel(t, "legacy.formatwithmetadata", types.ContractJSONWellFormed, false)
		newContractProbe(t, `{"text":"x","format_applied":"table","transformation_notes":[],"confidence":0.7}`)
		if got, err := FormatWithMetadata(map[string]int{"a": 1}, "a table"); err != nil || got.Text != "x" {
			t.Errorf("FormatWithMetadata = %+v, %v", got, err)
		}
		newContractProbe(t, "just text")
		if _, err := FormatWithMetadata(map[string]int{"a": 1}, "a table"); err == nil {
			t.Error("FormatWithMetadata accepted text")
		}
	})
	t.Run("QuestionLegacy takes the text", func(t *testing.T) {
		contractRowLevel(t, "legacy.questionlegacy", types.ContractPromptOnly, false)
		probe := newContractProbe(t, " Because. ")
		if got, err := QuestionLegacy("data", "why?"); err != nil || got != "Because." {
			t.Errorf("QuestionLegacy = %q, %v", got, err)
		}
		probe.declaredNoSchema(t)
	})
}

func TestInferRowIsWellFormedAndChecksWhatWasSupplied(t *testing.T) {
	contractRowLevel(t, "infer", types.ContractJSONWellFormed, false)
	supplied := Person{Name: "Ada"}

	probe := newContractProbe(t, `{"Name":"Ada","Age":36}`)
	got, err := Infer(supplied, NewInferOptions())
	if err != nil || got.Age != 36 {
		t.Fatalf("Infer = %+v, %v", got, err)
	}
	probe.declaredNoSchema(t)
	for name, answer := range map[string]string{
		"a supplied value changed":  `{"Name":"Grace","Age":36}`,
		"a member of the type left": `{"Name":"Ada"}`,
		"text":                      `Ada is 36`,
		"a list":                    `[1,2]`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := Infer(supplied, NewInferOptions()); err == nil {
				t.Errorf("Infer accepted %s: %+v", name, got)
			}
		})
	}
}

func TestMatchRowTakesTrueOrFalseAndNothingElse(t *testing.T) {
	contractRowLevel(t, "flow.match", types.ContractPromptOnly, false)
	ctx := context.Background()
	for answer, want := range map[string]bool{"true": true, " True. ": true, `"yes"`: true, "false": false, "No": false} {
		probe := newContractProbe(t, answer)
		got, err := matchesStringCondition(ctx, "an invoice", "is a bill")
		if err != nil || got != want {
			t.Errorf("answer %q: %v, %v", answer, got, err)
		}
		probe.declaredNoSchema(t)
	}
	for _, answer := range []string{"maybe", `{"match":true}`, "", "true or false", "I think so"} {
		newContractProbe(t, answer)
		if got, err := matchesStringCondition(ctx, "an invoice", "is a bill"); err == nil {
			t.Errorf("answer %q was accepted as %v", answer, got)
		}
	}
}

// Enrich's response shape holds maps, strict mode cannot express a map, and so
// no schema is ever sent, which is why its row is not schema constrained.
func TestEnrichRowSendsNoSchemaAndIsWellFormed(t *testing.T) {
	contractRowLevel(t, "exp.enrich", types.ContractJSONWellFormed, false)
	probe := newContractProbe(t, `{"enriched":{"Name":"Ada","Age":36},"added_fields":["Age"],"derivations":{"Age":"from the text"}}`)
	options := NewEnrichOptions()
	options.AddOnly = false
	got, err := Enrich[Person, Person](Person{Name: "Ada"}, options)
	if err != nil || got.Enriched.Age != 36 {
		t.Fatalf("Enrich = %+v, %v", got, err)
	}
	probe.declaredNoSchema(t)
	for name, answer := range map[string]string{
		"no enriched member":    `{"added_fields":[],"derivations":{}}`,
		"a null enriched value": `{"enriched":null,"added_fields":[],"derivations":{}}`,
		"no derivations":        `{"enriched":{"Name":"Ada"},"added_fields":[]}`,
		"text":                  `enriched`,
	} {
		newContractProbe(t, answer)
		if got, err := Enrich[Person, Person](Person{Name: "Ada"}, options); err == nil {
			t.Errorf("Enrich accepted %s: %+v", name, got)
		}
	}
	// A shape with no map in it has a schema, which is what the comment above
	// the declaration says the operation does.
	type withoutMaps struct {
		Enriched    Person   `json:"enriched"`
		AddedFields []string `json:"added_fields"`
	}
	if typePlanFor(reflect.TypeOf(withoutMaps{})).sharedSchema() == nil {
		t.Error("a response shape with no map has no schema, so the reason Enrich sends none is not the map")
	}
	if typePlanFor(reflect.TypeOf(map[string]float64{})).sharedSchema() != nil {
		t.Error("a map has a strict schema, so Enrich could send one")
	}
}

func TestVerifyRowDeclaresItsSchemaAndHoldsFindingsToTheirVocabularies(t *testing.T) {
	contractRowLevel(t, "verify", types.ContractSchemaAndInvariantChecked, false)
	options := NewVerifyOptions()
	options.CheckLogic, options.CheckConsistency = false, false
	options.IncludeEvidence, options.ExplainReasoning = false, false
	claims := func(verdict string) string {
		return `{"summary":"s","overall_confidence":0.9,"trust_score":0.9,"claims":[{"claim":"the sky is blue","verdict":"` + verdict + `","confidence":0.9}]}`
	}

	probe := newContractProbe(t, claims("verified"))
	result, err := VerifyWithModel("The sky is blue.", options)
	if err != nil {
		t.Fatalf("VerifyWithModel: %v (%+v)", err, result)
	}
	probe.declaredSchema(t, "verification")

	for name, answer := range map[string]string{
		"a verdict that is not one of the five": claims("probably"),
		"no claims member":                      `{"summary":"s","overall_confidence":0.9,"trust_score":0.9}`,
		"no summary":                            `{"overall_confidence":0.9,"trust_score":0.9,"claims":[]}`,
		"no trust score":                        `{"summary":"s","overall_confidence":0.9,"claims":[]}`,
		"a trust score above one":               `{"summary":"s","overall_confidence":0.9,"trust_score":4,"claims":[]}`,
		"a claim with no confidence":            `{"summary":"s","overall_confidence":0.9,"trust_score":0.9,"claims":[{"claim":"x","verdict":"verified"}]}`,
		"a claim with no text":                  `{"summary":"s","overall_confidence":0.9,"trust_score":0.9,"claims":[{"claim":" ","verdict":"verified","confidence":0.9}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := VerifyWithModel("The sky is blue.", options); err == nil {
				t.Errorf("VerifyWithModel accepted %s: %+v", name, got)
			}
		})
	}
	t.Run("a logic issue of a kind that was not offered", func(t *testing.T) {
		options := NewVerifyOptions()
		options.CheckFacts, options.CheckConsistency = false, false
		newContractProbe(t, `{"summary":"s","overall_confidence":0.9,"trust_score":0.9,"logic_issues":[{"type":"vibes","description":"d","severity":"minor"}]}`)
		if _, err := VerifyWithModel("text", options); err == nil {
			t.Error("a logic issue of an unknown kind was accepted")
		}
		newContractProbe(t, `{"summary":"s","overall_confidence":0.9,"trust_score":0.9,"logic_issues":[{"type":"false_premise","description":"d","severity":"minor"}]}`)
		if _, err := VerifyWithModel("text", options); err == nil {
			t.Error("false_premise was accepted with CheckFacts off, where it is not offered")
		}
	})
}

func TestAuditRowDeclaresItsSchemaAndHoldsFindingsToTheCategoriesAskedAbout(t *testing.T) {
	contractRowLevel(t, "audit", types.ContractSchemaAndInvariantChecked, false)
	finding := func(category string) string {
		return `{"findings":[{"category":"` + category + `","severity":0.6,"issue":"a gap"}]}`
	}

	probe := newContractProbe(t, finding("security"))
	result, err := AuditWithMeta("the data")
	if err != nil || result.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
		t.Fatalf("AuditWithMeta = %+v, %v", result.Meta.DeliveredContract, err)
	}
	probe.declaredSchema(t, "audit")

	for name, answer := range map[string]string{
		"a category that was not asked about": finding("vibes"),
		"no findings member":                  `{"issues":[]}`,
		"a null findings":                     `{"findings":null}`,
		"a finding with no severity":          `{"findings":[{"category":"security","issue":"x"}]}`,
		"a finding with no issue":             `{"findings":[{"category":"security","severity":0.5}]}`,
		"a severity above one":                `{"findings":[{"category":"security","severity":3,"issue":"x"}]}`,
		"a report on policies nobody gave":    `{"findings":[],"policies":{"p1":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := AuditWithModel("the data"); err == nil {
				t.Errorf("AuditWithModel accepted %s: %+v", name, got)
			}
		})
	}
	t.Run("a policy that is not reported on", func(t *testing.T) {
		options := AuditOptions{Policies: []string{"no secrets"}}
		newContractProbe(t, `{"findings":[],"policies":{}}`)
		if got, err := AuditWithModel("the data", options); err == nil {
			t.Errorf("an answer that did not report on a policy was accepted: %+v", got)
		}
	})
}

func TestRedactTextRowFindsEverySpanInTheText(t *testing.T) {
	contractRowLevel(t, "redact.text", types.ContractSchemaAndInvariantChecked, false)
	ctx := context.Background()
	text := "Call John Smith on 555-0100 today."

	probe := newContractProbe(t, `{"spans":[{"text":"John Smith","category":"name"}]}`)
	result, err := RedactLLMWithMeta(ctx, text, NewRedactLLMOptions())
	if err != nil || result.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
		t.Fatalf("RedactLLMWithMeta = %+v, %v", result.Meta.DeliveredContract, err)
	}
	probe.declaredSchema(t, "redaction_spans")
	if strings.Contains(result.Value.Text, "John") {
		t.Errorf("the span was not redacted: %q", result.Value.Text)
	}

	for name, answer := range map[string]string{
		"a span that is not in the text": `{"spans":[{"text":"Grace Hopper","category":"name"}]}`,
		"a blank span":                   `{"spans":[{"text":"  ","category":"name"}]}`,
		"no spans member":                `{"found":[]}`,
		"a null spans":                   `{"spans":null}`,
		"a span with no category":        `{"spans":[{"text":"John Smith"}]}`,
		"text":                           `John Smith`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := RedactLLM(ctx, text, NewRedactLLMOptions()); err == nil {
				t.Errorf("RedactLLM accepted %s: %q", name, got.Text)
			}
		})
	}
	t.Run("a label outside the categories asked for", func(t *testing.T) {
		options := NewRedactLLMOptions()
		options.Categories = []string{"email"}
		newContractProbe(t, `{"spans":[{"text":"John Smith","category":"name"}]}`)
		if got, err := RedactLLM(ctx, text, options); err == nil {
			t.Errorf("a span outside the requested categories was accepted: %q", got.Text)
		}
	})
}

func TestArbitrateRowRequiresEveryOptionOnEveryRule(t *testing.T) {
	contractRowLevel(t, "exp.arbitrate", types.ContractSchemaAndInvariantChecked, false)
	options := ArbitrateOptions{Rules: []string{"cheap", "fast"}}
	evaluation := `{"passed":true,"score":0.8,"reasoning":"ok"}`
	rules := `"` + arbitrateRuleKey(0) + `":` + evaluation + `,"` + arbitrateRuleKey(1) + `":` + evaluation
	both := `{"options":{"` + arbitrateOptionKey(0) + `":{` + rules + `},"` + arbitrateOptionKey(1) + `":{` + rules + `}},"reasoning":"r","confidence":0.7}`

	probe := newContractProbe(t, both)
	result, err := Arbitrate([]string{"alpha", "beta"}, options)
	if err != nil {
		t.Fatalf("Arbitrate = %+v, %v", result, err)
	}
	probe.declaredSchema(t, "arbitration")

	oneRule := `"` + arbitrateRuleKey(0) + `":` + evaluation
	for name, answer := range map[string]string{
		"an option missing":   `{"options":{"` + arbitrateOptionKey(0) + `":{` + rules + `}},"reasoning":"r","confidence":0.7}`,
		"a rule missing":      `{"options":{"` + arbitrateOptionKey(0) + `":{` + oneRule + `},"` + arbitrateOptionKey(1) + `":{` + rules + `}},"reasoning":"r","confidence":0.7}`,
		"a score above one":   strings.Replace(both, `"score":0.8`, `"score":8`, 1),
		"no options member":   `{"reasoning":"r","confidence":0.7}`,
		"no confidence":       strings.Replace(both, `,"confidence":0.7`, "", 1),
		"a rule with no pass": strings.Replace(both, `"passed":true,`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := Arbitrate([]string{"alpha", "beta"}, options); err == nil {
				t.Errorf("Arbitrate accepted %s: %+v", name, got)
			}
		})
	}
}

func TestDeduplicateRowChecksEveryLinkAgainstTheList(t *testing.T) {
	contractRowLevel(t, "deduplicate", types.ContractSchemaAndInvariantChecked, false)
	items := []string{"a", "b", "c"}
	link := func(index, of int) string {
		return `{"duplicates":[{"index":` + strconv.Itoa(index) + `,"duplicate_of":` + strconv.Itoa(of) + `,"similarity":0.95}]}`
	}

	probe := newContractProbe(t, link(1, 0))
	result, err := Deduplicate(items, 0.9)
	if err != nil || len(result.Unique) != 2 || result.TotalRemoved != 1 {
		t.Fatalf("Deduplicate = %+v, %v", result, err)
	}
	probe.declaredSchema(t, "duplicates")

	for name, answer := range map[string]string{
		"a link to a later item":       link(0, 1),
		"an item that is its own copy": link(1, 1),
		"an index out of range":        link(9, 0),
		"a target out of range":        link(1, 9),
		"a negative target":            link(1, -1),
		"no duplicates member":         `{"pairs":[]}`,
		"a null list":                  `{"duplicates":null}`,
		"an entry with no similarity":  `{"duplicates":[{"index":1,"duplicate_of":0}]}`,
		"a similarity above one":       `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":5}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := Deduplicate(items, 0.9); err == nil {
				t.Errorf("Deduplicate accepted %s: %+v", name, got)
			}
		})
	}
	t.Run("items written the same are linked in Go and one item sends nothing", func(t *testing.T) {
		probe := newContractProbe(t, link(1, 0))
		if got, err := Deduplicate([]string{"a", "a"}, 0.9); err != nil || got.TotalRemoved != 1 || probe.requests() != 0 {
			t.Errorf("Deduplicate of equal items sent %d requests: %+v, %v", probe.requests(), got, err)
		}
	})
}

func TestDecideRowHoldsTheSelectionToTheBranchesOffered(t *testing.T) {
	contractRowLevel(t, "flow.decide", types.ContractSchemaAndInvariantChecked, false)
	ctx := context.Background()
	branches := []Decision[string]{{Value: "refund", Description: "refund the order"}, {Value: "reship", Description: "ship it again"}}
	answer := func(selected, alternatives string) string {
		return `{"selected":` + selected + `,"explanation":"x","confidence":0.8,"alternatives":` + alternatives + `}`
	}

	probe := newContractProbe(t, answer("1", "[0]"))
	got, result, err := Decide(ctx, "the parcel was lost", branches)
	if err != nil || got != "reship" || result.SelectedIndex != 1 {
		t.Fatalf("Decide = %q, %+v, %v", got, result, err)
	}
	probe.declaredSchema(t, "decision")

	for name, body := range map[string]string{
		"a selection past the last branch":   answer("2", "[]"),
		"a negative selection":               answer("-1", "[]"),
		"a fractional selection":             answer("0.5", "[]"),
		"the selection among its own":        answer("1", "[1]"),
		"an alternative twice":               answer("0", "[1,1]"),
		"an alternative that does not exist": answer("0", "[7]"),
		"no explanation":                     `{"selected":0,"confidence":0.8}`,
		"no confidence":                      `{"selected":0,"explanation":"x"}`,
		"no selection":                       `{"explanation":"x","confidence":0.8}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, body)
			// A refused answer with no fallback is an error; nothing is chosen.
			if got, _, err := Decide(ctx, "the parcel was lost", branches); err == nil {
				t.Errorf("Decide accepted %s: %q", name, got)
			}
		})
	}
}

func TestDecomposeRowDeclaresItsSchemaAndChecksTheStructure(t *testing.T) {
	contractRowLevel(t, "exp.decompose", types.ContractSchemaAndInvariantChecked, false)
	part := func(id, parent string, depth int) string {
		return `{"id":"` + id + `","name":"n","description":"d","content":{"Name":"A","Age":1},"parent_id":"` + parent +
			`","children":[],"dependencies":[],"depth":` + strconv.Itoa(depth) + `,"order":1}`
	}
	input := Person{Name: "Ada", Age: 36}

	probe := newContractProbe(t, `{"parts":[`+part("p1", "", 1)+`],"root_parts":["p1"]}`)
	result, err := Decompose(input, NewDecomposeOptions())
	if err != nil || len(result.Parts) != 1 {
		t.Fatalf("Decompose = %+v, %v", result, err)
	}
	probe.declaredSchema(t, "decomposition")

	for name, answer := range map[string]string{
		"a parent that is not a part":        `{"parts":[` + part("p1", "zz", 2) + `],"root_parts":["p1"]}`,
		"an id twice":                        `{"parts":[` + part("p1", "", 1) + `,` + part("p1", "", 1) + `],"root_parts":["p1"]}`,
		"a root that is not a part":          `{"parts":[` + part("p1", "", 1) + `],"root_parts":["nope"]}`,
		"a depth that is not the parent's+1": `{"parts":[` + part("p1", "", 1) + `,` + part("p2", "p1", 5) + `],"root_parts":["p1"]}`,
		"no parts":                           `{"parts":[],"root_parts":[]}`,
		"no root_parts member":               `{"parts":[` + part("p1", "", 1) + `]}`,
		"a part with no content":             `{"parts":[{"id":"p1","name":"n","depth":1,"order":1}],"root_parts":["p1"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			newContractProbe(t, answer)
			if got, err := Decompose(input, NewDecomposeOptions()); err == nil {
				t.Errorf("Decompose accepted %s: %+v", name, got)
			}
		})
	}
}

// The rows whose schema is built from the caller's type: with a type strict
// mode can express, a schema is declared; with one it cannot, none is sent and
// the row's without level is what the call delivers.
func TestTypeBoundRowsSendASchemaOnlyForATypeThatHasOne(t *testing.T) {
	for _, operation := range []string{"extract", "generate", "transform", "question", "compress", "normalize", "validate", "exp.predict", "exp.decompose"} {
		row, ok := contractRowOf(operation)
		if !ok || !row.typeBound || row.without >= row.level && row.level != types.ContractPromptOnly {
			t.Errorf("%s is not a type-bound row with a lower level for a type with no schema: %+v", operation, row)
		}
	}
	type recursive struct {
		Name string
		Next *recursive
	}
	type flat struct{ Name string }

	t.Run("question", func(t *testing.T) {
		answer := `{"answer":"x","confidence":0.5,"reasoning":"r","evidence":["e"]}`
		probe := newContractProbe(t, answer)
		if _, err := Question[string, string]("data", NewQuestionOptions("why?")); err != nil {
			t.Fatal(err)
		}
		probe.declaredSchema(t, "question_answer")

		probe = newContractProbe(t, `{"answer":{"a":"b"},"confidence":0.5,"reasoning":"r","evidence":["e"]}`)
		if got, err := Question[string, map[string]string]("data", NewQuestionOptions("why?")); err != nil || got.Answer["a"] != "b" {
			t.Fatalf("Question with a map answer = %+v, %v", got, err)
		}
		probe.declaredNoSchema(t)
		// The level follows the request: it said schema constrained for the map
		// until ContractLevelFor read the type.
		if got := ContractLevelFor[map[string]string]("question"); got != types.ContractJSONWellFormed {
			t.Errorf("a call that sent no schema has the level %s", got)
		}
		if got := ContractLevelFor[string]("question"); got != types.ContractSchemaConstrained {
			t.Errorf("a call that sent a schema has the level %s", got)
		}
	})
	t.Run("compress", func(t *testing.T) {
		answer := `{"compressed":{"Name":"A"},"preserved_info":["x"],"removed_info":["y"]}`
		probe := newContractProbe(t, answer)
		if _, err := Compress(flat{"A"}, NewCompressOptions()); err != nil {
			t.Fatal(err)
		}
		probe.declaredSchema(t, "compression")
		probe = newContractProbe(t, `{"compressed":{"Name":"A"},"preserved_info":["x"],"removed_info":["y"]}`)
		if _, err := Compress(recursive{Name: "A"}, NewCompressOptions()); err != nil {
			t.Fatal(err)
		}
		probe.declaredNoSchema(t)
		if got := ContractLevelFor[recursive]("compress"); got != types.ContractJSONWellFormed {
			t.Errorf("a call that sent no schema has the level %s", got)
		}
		if got := ContractLevelFor[flat]("compress"); got != types.ContractSchemaConstrained {
			t.Errorf("a call that sent a schema has the level %s", got)
		}
	})
}

// RunOp sets no schema of its own: the level a descriptor asks for is read from
// its SchemaName and Invariants, and the level it is delivered at follows what
// was sent. A descriptor that names a schema and never sends one asks for schema
// and invariants checked and is delivered as well formed, which is all an answer
// that no schema was held to has earned; the same descriptor with the schema in
// the request, to a route that enforces it, is delivered as it asked.
func TestADescriptorThatNamesASchemaItNeverSendsIsDeliveredAsWellFormed(t *testing.T) {
	contractRowLevel(t, "op.run", types.ContractJSONWellFormed, true)
	probe := newContractProbe(t, "even")
	op := echoOp(func(value string) error {
		if value != "even" {
			return errFor(value)
		}
		return nil
	})
	op.Contract.SchemaName = "echo"

	result, err := RunOpResult(context.Background(), op, "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	probe.declaredNoSchema(t)
	if result.Meta.RequestedContract != types.ContractSchemaAndInvariantChecked {
		t.Errorf("the descriptor asked for %s", result.Meta.RequestedContract)
	}
	if result.Meta.DeliveredContract != types.ContractJSONWellFormed {
		t.Errorf("the descriptor delivered %s, want well formed: no schema was sent", result.Meta.DeliveredContract)
	}
	if !result.Meta.Degraded() {
		t.Error("an answer delivered below what was asked is not said to be degraded")
	}
	// Without a declared name the same operation is well formed, which is the
	// lowest the row says.
	plain := echoOp(func(string) error { return nil })
	if got := declaredContractLevel(plain.Contract); got != types.ContractJSONWellFormed {
		t.Errorf("a descriptor that names no schema is %s", got)
	}
	// A batch with a merge written by hand is prompt only, which is the lowest
	// the batch rows say.
	contractRowLevel(t, "op.runbatch", types.ContractPromptOnly, true)
	if got := batchContractLevel(plain); got != types.ContractPromptOnly {
		t.Errorf("a batch with its own merge is %s", got)
	}
}

type contractTriage struct {
	Refund  bool   `ask:"Is the customer asking for a refund?"`
	Team    string `ask:"Which team should handle this?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is this?" scale:"1-5"`
}

// contractProbeCase drives one operation once and names the schema its first
// request has to declare, or "" for none.
type contractProbeCase struct {
	row    string
	schema string
	call   func()
}

// contractProbeCases is the table that holds every row that can be driven with
// one scripted answer to what its row says about the request.
func contractProbeCases() []contractProbeCase {
	ctx := context.Background()
	ada := Person{Name: "Ada", Age: 36}
	grace := Person{Name: "Grace", Age: 45}
	labels := []string{"billing", "technical", "shipping"}
	return []contractProbeCase{
		// No schema: the answer is read as JSON or taken as text.
		{"parse", "", func() {
			options := NewParseOptions()
			options.AllowLLMFallback = true
			_, _ = Parse[Person]("Ada is thirty six and writes programs, they say", options)
		}},
		{"suggest", "", func() { _, _ = Suggest[string]("a project name", NewSuggestOptions()) }},
		{"annotate", "", func() { _, _ = Annotate("Ada wrote programs", NewAnnotateOptions()) }},
		{"explain", "", func() { _, _ = Explain(ada, NewExplainOptions()) }},
		{"merge", "", func() { _, _ = Merge([]Person{ada, grace}, "newest wins") }},
		{"score", "", func() { _, _ = Score("a text", NewScoreOptions()) }},
		{"compare", "", func() { _, _ = Compare("a", "b", NewCompareOptions()) }},
		{"critique", "", func() {
			options := NewCritiqueOptions()
			options.Criteria = []string{"clarity"}
			_, _ = CritiqueWithModel("a text", options)
		}},
		{"diff", "", func() { _, _ = Diff(ada, grace, NewDiffOptions()) }},
		{"classify", "", func() { _, _ = Classify[string, string]("x", NewClassifyOptions().WithCategories(labels)) }},
		{"cluster", "", func() { _, _ = Cluster([]string{"a", "b"}, NewClusterOptions()) }},
		{"rank", "", func() { _, _ = Rank([]string{"a", "b"}, NewRankOptions()) }},
		{"choose", "", func() { _, _ = Choose([]string{"a", "b"}, NewChooseOptions()) }},
		{"filter", "", func() { _, _ = Filter([]string{"a", "b"}, NewFilterOptions().WithCriteria("short")) }},
		{"sort", "", func() { _, _ = Sort([]string{"a", "b"}, NewSortOptions().WithCriteria("alphabetical")) }},
		{"semanticmatch", "", func() { _, _ = SemanticMatch([]string{"a"}, []string{"b"}, NewMatchOptions()) }},
		{"complete", "", func() { _, _ = Complete(ctx, "Once upon", NewCompleteOptions()) }},
		{"format", "", func() { _, _ = Format(ada, "a card") }},
		{"infer", "", func() { _, _ = Infer(Person{Name: "Ada"}, NewInferOptions()) }},
		{"summarize", "", func() { _, _ = Summarize("a text", NewSummarizeOptions()) }},
		{"rewrite", "", func() { _, _ = Rewrite("a text", NewRewriteOptions()) }},
		{"translate", "", func() { _, _ = Translate("a text", NewTranslateOptions().WithTargetLanguage("French")) }},
		{"expand", "", func() { _, _ = Expand("a text", NewExpandOptions()) }},
		{"summarizedetail", "", func() { _, _ = SummarizeResult("a text", NewSummarizeOptions()) }},
		{"rewritedetail", "", func() { _, _ = RewriteResult("a text", NewRewriteOptions()) }},
		{"translatedetail", "", func() { _, _ = TranslateResult("a text", NewTranslateOptions().WithTargetLanguage("French")) }},
		{"expanddetail", "", func() { _, _ = ExpandResult("a text", NewExpandOptions()) }},
		{"exp.synthesize", "", func() { _, _ = Synthesize[Person]([]any{"a", "b"}, NewSynthesizeOptions()) }},
		{"exp.assemble", "", func() { _, _ = Assemble[Person]([]any{"a", "b"}) }},
		{"exp.derive", "", func() { _, _ = Derive[Person, Person](ada) }},
		{"exp.interpolate", "", func() { _, _ = Interpolate([]Person{ada, {}, grace}) }},
		{"exp.resolve", "", func() { _, _ = Resolve([]Person{ada, grace}) }},
		{"exp.settle", "", func() { _, _ = Settle[Person]("a budget of ten") }},
		{"exp.conform", "", func() { _, _ = Conform(ada, "a standard") }},
		{"exp.project", "", func() { _, _ = Project[Person, Person](ada) }},
		{"exp.pivot", "", func() { _, _ = Pivot[Person, Person](ada) }},
		{"exp.enrich", "", func() { _, _ = Enrich[Person, Person](ada, NewEnrichOptions()) }},
		{"exp.enrichinplace", "", func() { _, _ = EnrichInPlace(Person{Name: "Ada"}, NewEnrichOptions()) }},
		{"exp.decomposetoslice", "", func() { _, _ = DecomposeToSlice[Person, Person](ada, NewDecomposeOptions()) }},
		{"legacy.validatelegacy", "", func() { _, _ = ValidateLegacy(ada, "an adult") }},
		{"legacy.mergewithmetadata", "", func() { _, _ = MergeWithMetadata([]Person{ada, grace}, "newest wins") }},
		{"legacy.formatwithmetadata", "", func() { _, _ = FormatWithMetadata(ada, "a card") }},
		{"legacy.questionlegacy", "", func() { _, _ = QuestionLegacy(ada, "how old?") }},
		{"flow.match", "", func() { _, _ = matchesStringCondition(ctx, "an invoice", "is a bill") }},

		// A schema: declared on the request, strict, every key required.
		{"similar", "similarity", func() { _, _ = Similar("a", "b", NewSimilarOptions()) }},
		{"deduplicate", "duplicates", func() { _, _ = Deduplicate([]string{"a", "b"}, 0.9) }},
		{"verify", "verification", func() { _, _ = VerifyWithModel("text", NewVerifyOptions()) }},
		{"audit", "audit", func() { _, _ = AuditWithModel("text") }},
		{"redact.text", "redaction_spans", func() { _, _ = RedactLLM(ctx, "Call John", NewRedactLLMOptions()) }},
		{"exp.arbitrate", "arbitration", func() { _, _ = Arbitrate([]string{"a", "b"}, ArbitrateOptions{Rules: []string{"cheap"}}) }},
		{"flow.decide", "decision", func() {
			_, _, _ = Decide(ctx, "a parcel", []Decision[string]{{Value: "a", Description: "a"}, {Value: "b", Description: "b"}})
		}},
		{"is", "assessment", func() { _, _ = Is(ctx, "x", "Is it?") }},
		{"isdetail", "assessment", func() { _, _ = IsResult(ctx, "x", "Is it?") }},
		{"which", "assessment", func() { _, _ = Which(ctx, "x", "Which?", labels) }},
		{"whichdetail", "assessment", func() { _, _ = WhichResult(ctx, "x", "Which?", labels) }},
		{"whichornone", "assessment", func() { _, _, _ = WhichOrNone(ctx, "x", "Which?", labels) }},
		{"whichornonedetail", "assessment", func() { _, _ = WhichOrNoneResult(ctx, "x", "Which?", labels) }},
		{"rate", "assessment", func() { _, _ = Rate(ctx, "x", "How?", 1, 5) }},
		{"ratedetail", "assessment", func() { _, _ = RateResult(ctx, "x", "How?", 1, 5) }},
		{"assess", "assessment", func() { _, _ = Assess[contractTriage](ctx, "a message") }},
		{"assessdetail", "assessment", func() { _, _ = AssessResult[contractTriage](ctx, "a message") }},

		// A schema built from the caller's type, which is one that has a schema. The
		// name of the one built from a type is the type's, written "*" here.
		{"transform", "*", func() { _, _ = Transform[contractSource, Person](contractSource{"Ada", 36}, NewTransformOptions()) }},
		{"question", "question_answer", func() { _, _ = Question[string, string]("data", NewQuestionOptions("why?")) }},
		{"compress", "compression", func() { _, _ = Compress(ada, NewCompressOptions()) }},
		{"normalize", "normalize_response", func() { _, _ = Normalize(ada, NewNormalizeOptions()) }},
		{"exp.predict", "prediction", func() { _, _ = Predict[Person]("a history", NewPredictOptions()) }},
		{"exp.decompose", "decomposition", func() { _, _ = Decompose(ada, NewDecomposeOptions()) }},
		{"validate", "validation", func() {
			options := NewValidateOptions()
			options.Rules = "must be an adult"
			_, _ = ValidateHybrid(ada, options)
		}},
	}
}

// contractUnprobed are the rows no scripted answer drives, and why.
var contractUnprobed = map[string]string{
	"chat.send":             "the v2/chat package depends on internal/ops; its request shape is checked in v2/chat/session_test.go",
	"chat.sendresults":      "the v2/chat package depends on internal/ops; its request shape is checked in v2/chat/session_test.go",
	"streamsummarize":       "a stream goes to the provider's stream method, which the scripted caller does not stand for",
	"streamrewrite":         "a stream goes to the provider's stream method, which the scripted caller does not stand for",
	"streamtranslate":       "a stream goes to the provider's stream method, which the scripted caller does not stand for",
	"streamexpand":          "a stream goes to the provider's stream method, which the scripted caller does not stand for",
	"completefield":         "CompleteField runs completeImpl, which complete drives",
	"exp.settleadversarial": "it needs a built negotiation; Settle drives the same reader",
}

// Every row that is static is driven once and its first request is held to what
// the row says: no schema for the rows that rest on none, and a strict schema
// under the name the operation declares for the rows that rest on one.
func TestEveryStaticRowIsHeldToItsRequest(t *testing.T) {
	probed := map[string]bool{}
	for _, tc := range contractProbeCases() {
		probed[tc.row] = true
		t.Run(tc.row, func(t *testing.T) {
			rule, known := ContractLevelOf(tc.row)
			if !known || rule.Computed {
				t.Fatalf("%s is not a static row: %+v, %v", tc.row, rule, known)
			}
			probe := newContractProbe(t, `{}`)
			tc.call()
			if probe.requests() == 0 {
				t.Fatal("the call sent no request")
			}
			switch {
			case tc.schema == "" && rule.Level >= types.ContractSchemaConstrained:
				t.Errorf("the row is %s and the request declares no schema", rule.Level)
			case tc.schema == "":
				probe.declaredNoSchema(t)
			case rule.Level < types.ContractSchemaConstrained:
				t.Errorf("the request declares a schema and the row is only %s", rule.Level)
			default:
				opt := probe.first(t)
				if opt.SchemaName == "" || (tc.schema != "*" && opt.SchemaName != tc.schema) {
					t.Errorf("the request declares %q, want %q", opt.SchemaName, tc.schema)
				}
				probe.declaredSchema(t, opt.SchemaName)
			}
		})
	}
	for i := range contractRows {
		row := &contractRows[i]
		if probed[row.name] || row.computed {
			continue
		}
		if reason, listed := contractUnprobed[row.name]; !listed || reason == "" {
			t.Errorf("the static row %s is driven by no probe and says why not", row.name)
		}
	}
	for name := range contractUnprobed {
		if probed[name] {
			t.Errorf("%s is listed as not driven and is", name)
		}
	}
}
