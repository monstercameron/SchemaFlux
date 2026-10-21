package ops

import (
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The level of an operation for a type.
//
// A level of schema constrained or above needs a JSON schema declared on the
// request. For nine operations (the rows of the table with typeBound set) the
// schema is built from a type the caller chose, and for a type strict mode
// cannot express (a map, a recursive type, an interface, a type nested deeper
// than six) none is built and none is sent. The call is then delivered as well
// formed, whatever the row says for a type that has a schema. Each of them
// reported the row's level for every type, so the Record said schema
// constrained for a call that had sent no schema, and a floor of schema
// constrained admitted it.
//
// ContractLevelFor is the one place that says which. It asks the same question
// the request builders ask, for the same shape (what the operation puts the
// type inside), so the answer cannot be another one: the property test in
// contracttype_test.go drives every operation into a list of types and holds
// the level to the request that was sent.

// typeBoundRule is the level of one type-bound row for each type it has been
// asked about. The levels of a type are worked out once; a read is a lookup in
// a sync.Map and takes no lock.
type typeBoundRule struct {
	row    *contractRow
	levels sync.Map // reflect.Type -> types.ContractLevel
	held   atomic.Int64
}

// typeLevelLimit is how many types a rule keeps. A reflect.Type is not
// something a caller's data can mint, but reflect.StructOf makes types at run
// time, and a program that did so in a loop would add an entry for each. A type
// past the limit is worked out again each time it is asked about.
const typeLevelLimit = 4096

// typeBoundRules is the rule of each type-bound row, by every name the row is
// known by. It is written once, when the package is initialised, and read
// without a lock.
var typeBoundRules = typeBoundRulesOf(contractRows)

func typeBoundRulesOf(rows []contractRow) map[string]*typeBoundRule {
	rules := make(map[string]*typeBoundRule, 16)
	for i := range rows {
		if !rows[i].typeBound {
			continue
		}
		rule := &typeBoundRule{row: &rows[i]}
		for _, name := range append([]string{rows[i].name}, rows[i].aliases...) {
			rules[name] = rule
		}
	}
	return rules
}

// ContractLevelFor is the contract level the operation delivers when T is the
// type its schema is built from: the type of the value Extract reads, of the
// target of Transform, of the answer of Question, of the value Compress,
// Normalize and Validate are given, of the prediction of Predict, of the
// content of Decompose and of what Generate makes.
//
// For a type-bound row it is the row's level when a schema can be built for T
// and the row's without level when none can, so it never says schema
// constrained for a call that sends no schema. Generate is prompt only for a
// string, which no schema is asked for. An operation that is not bound to a
// type is the level of its row whatever T is, and a name that is no operation
// is prompt only, the lowest level, since nothing is known of it.
//
// Validate's schema follows the type only when a correction is asked for, which
// is what the answer is for here; ValidateContract takes that into account.
//
// The level of a type is worked out once and kept. A call after the first
// allocates nothing.
func ContractLevelFor[T any](operation string) types.ContractLevel {
	return ContractLevelForType(operation, reflect.TypeFor[T]())
}

// ContractLevelForType is ContractLevelFor for a type known only at run time. A
// nil type has no schema.
func ContractLevelForType(operation string, target reflect.Type) types.ContractLevel {
	if rule, bound := typeBoundRules[operation]; bound {
		return rule.levelFor(target)
	}
	// Not bound to a type: the row's level, or the zero rule's, prompt only,
	// for a name that is no operation.
	known, _ := ContractLevelOf(operation)
	return known.Level
}

// ValidateContract is the level Validate delivers for T. With autoCorrect set
// the schema holds the corrected value, so it follows the type as
// ContractLevelFor says; without it the schema is the same whatever T is, and
// the level is the row's.
func ValidateContract[T any](autoCorrect bool) types.ContractLevel {
	if !autoCorrect {
		known, _ := ContractLevelOf("validate")
		return known.Level
	}
	return ContractLevelFor[T]("validate")
}

func (rule *typeBoundRule) levelFor(target reflect.Type) types.ContractLevel {
	if target == nil {
		return rule.row.without
	}
	if kept, found := rule.levels.Load(target); found {
		return kept.(types.ContractLevel)
	}
	level := rule.workOut(target)
	if rule.held.Load() < typeLevelLimit {
		if _, loaded := rule.levels.LoadOrStore(target, level); !loaded {
			rule.held.Add(1)
		}
	}
	return level
}

// workOut is the level for a type nobody has asked about.
func (rule *typeBoundRule) workOut(target reflect.Type) types.ContractLevel {
	generate := rule.row.name == "generate"
	// Generate asks a string for no schema and takes the model's text as it
	// comes, so a string is the lowest level and not the level of a type with
	// none.
	if generate && target.Kind() == reflect.String {
		return types.ContractPromptOnly
	}
	if !schemaIsBuilt(rule.row.name, target) {
		return rule.row.without
	}
	if generate {
		// The row's own level is the lowest Generate has, which is for a string.
		return types.ContractSchemaConstrained
	}
	return rule.row.level
}

// schemaIsBuilt reports whether the request of the operation carries a schema
// when T is the type it is called with. It builds what the request builder
// builds, from the same functions where there are any: an operation that wraps
// the type in an envelope of its own has no schema when the type has none, and
// Normalize's envelope is one level deeper than the type, which is the limit's
// to say for a type six levels down.
func schemaIsBuilt(operation string, target reflect.Type) bool {
	switch operation {
	case "normalize":
		return jsonSchemaFor(normalizeEnvelopeOf(target), map[reflect.Type]bool{}, 0) != nil
	case "validate":
		return validateCorrectedSchemaOf(target) != nil
	case "exp.decompose":
		return decomposeResponseSchema(typePlanFor(target).sharedSchema(), false) != nil
	case "exp.predict":
		return predictResponseSchema(typePlanFor(target).sharedSchema(), PredictOptions{}) != nil
	}
	// Extract, Transform and Generate declare the type's own schema. Question
	// and Compress put it into an envelope, which has one whenever the type has.
	return typePlanFor(target).sharedSchema() != nil
}

// normalizeEnvelopeOf is the type Normalize declares its schema from: the
// normalized value, the changes and the warnings, with the tags that name them.
// It is written out again here because the one in Normalize is a type of the
// function's own, and a test holds the two to the same request.
func normalizeEnvelopeOf(target reflect.Type) reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "Normalized", Type: target, Tag: `json:"normalized"`},
		{Name: "Changes", Type: reflect.TypeFor[[]NormalizeChange](), Tag: `json:"changes"`},
		{Name: "Warnings", Type: reflect.TypeFor[[]string](), Tag: `json:"warnings"`},
	})
}
