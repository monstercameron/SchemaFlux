package ops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type personV1 struct {
	Name string `json:"name"`
}

type personV2 struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func splitName(raw json.RawMessage) (json.RawMessage, error) {
	var v1 personV1
	if err := json.Unmarshal(raw, &v1); err != nil {
		return nil, err
	}
	parts := strings.SplitN(v1.Name, " ", 2)
	v2 := personV2{FirstName: parts[0]}
	if len(parts) > 1 {
		v2.LastName = parts[1]
	}
	return json.Marshal(v2)
}

// S-013. This is the verification TODOS.md asks for: a result stored under
// one identity decodes into a later type through a registered migration, and
// the result records which migration ran.
func TestMigrateAppliesARegisteredMigration(t *testing.T) {
	from := DescribeSchema(reflect.TypeOf(personV1{}))
	to := DescribeSchema(reflect.TypeOf(personV2{}))

	registry := NewRegistry()
	if err := registry.Register(Migration{
		Name: "person-v1-split-name-v2",
		From: from,
		To:   to,
		Fn:   splitName,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stored := json.RawMessage(`{"name":"Ada Lovelace"}`)
	result, err := registry.Migrate(stored, from, reflect.TypeOf(personV2{}))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if !result.Applied {
		t.Fatal("Applied = false, want true -- a migration ran")
	}
	if result.MigrationName != "person-v1-split-name-v2" {
		t.Errorf("MigrationName = %q, want the registered name", result.MigrationName)
	}
	if result.From.String() != from.String() {
		t.Errorf("From = %s, want %s", result.From, from)
	}
	if result.To.String() != to.String() {
		t.Errorf("To = %s, want %s", result.To, to)
	}

	var decoded personV2
	if err := DecodeExact(string(result.Data), &decoded, DecodeLimits{}); err != nil {
		t.Fatalf("the migrated bytes did not decode into v2: %v", err)
	}
	if decoded.FirstName != "Ada" || decoded.LastName != "Lovelace" {
		t.Errorf("decoded = %+v, want split first/last name", decoded)
	}
}

// No migration ran, no schema changed: Migrate is a no-op that says so.
func TestMigrateIsANoOpWhenIdentitiesMatch(t *testing.T) {
	registry := NewRegistry()
	from := DescribeSchema(reflect.TypeOf(personV1{}))
	stored := json.RawMessage(`{"name":"Ada Lovelace"}`)

	result, err := registry.Migrate(stored, from, reflect.TypeOf(personV1{}))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if result.Applied {
		t.Error("Applied = true, want false -- identities matched, nothing should have run")
	}
	if result.MigrationName != "" {
		t.Errorf("MigrationName = %q, want empty when nothing ran", result.MigrationName)
	}
	if string(result.Data) != string(stored) {
		t.Errorf("Data = %s, want the original bytes unchanged", result.Data)
	}
}

// An unregistered path is a schema violation, not a guess.
func TestMigrateRefusesAnUnregisteredPath(t *testing.T) {
	registry := NewRegistry()
	from := DescribeSchema(reflect.TypeOf(personV1{}))

	_, err := registry.Migrate(json.RawMessage(`{"name":"x"}`), from, reflect.TypeOf(personV2{}))
	if err == nil {
		t.Fatal("Migrate accepted an identity with no registered migration")
	}
	if kind := types.KindOf(err); kind != types.KindSchemaViolation {
		t.Errorf("kind = %v, want schema violation", kind)
	}
	if !strings.Contains(err.Error(), "no migration is registered") {
		t.Errorf("error does not explain the gap: %v", err)
	}
}

// A migration whose Fn errors is a failed migration, and the failure does
// not leak the payload it was transforming into the error string every
// logger prints -- AGENTS.md's "never log or embed the caller's payload".
func TestMigrateSurfacesAFailedTransformWithoutThePayload(t *testing.T) {
	from := SchemaDescriptor{Name: "broken", Version: "v1", Hash: "aaa", Dialect: SchemaDialect}
	to := DescribeSchema(reflect.TypeOf(personV2{}))

	registry := NewRegistry()
	if err := registry.Register(Migration{
		Name: "broken-migration",
		From: from,
		To:   to,
		Fn: func(raw json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("SECRET-PAYLOAD-MARKER account=" + string(raw))
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := registry.Migrate(json.RawMessage(`{"account":"12345"}`), from, reflect.TypeOf(personV2{}))
	if err == nil {
		t.Fatal("Migrate accepted a transform that returned an error")
	}
	if strings.Contains(err.Error(), "SECRET-PAYLOAD-MARKER") || strings.Contains(err.Error(), "12345") {
		t.Errorf("the error string embeds the payload the migration was transforming: %v", err)
	}
	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatal("error is not an *types.OperationError")
	}
	if opErr.Cause == nil {
		t.Error("Cause is nil -- the underlying error should still be reachable via Unwrap, just not stringified into Message")
	}
}

// Migrate checks the migration's claimed To against what the caller actually
// asked for, rather than trusting it -- a migration that produces the wrong
// shape is the failure this exists to catch.
func TestMigrateRefusesAMigrationThatClaimsTheWrongTarget(t *testing.T) {
	from := DescribeSchema(reflect.TypeOf(personV1{}))
	wrongTo := SchemaDescriptor{Name: "not-person-v2", Version: "v1", Hash: "zzz", Dialect: SchemaDialect}

	registry := NewRegistry()
	if err := registry.Register(Migration{
		Name: "mismatched",
		From: from,
		To:   wrongTo,
		Fn:   splitName,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := registry.Migrate(json.RawMessage(`{"name":"Ada Lovelace"}`), from, reflect.TypeOf(personV2{}))
	if err == nil {
		t.Fatal("Migrate accepted a migration whose declared To does not match the requested target type")
	}
}

func TestRegisterRefusesAMigrationWithNoName(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(Migration{
		From: DescribeSchema(reflect.TypeOf(personV1{})),
		To:   DescribeSchema(reflect.TypeOf(personV2{})),
		Fn:   splitName,
	})
	if err == nil {
		t.Fatal("Register accepted a migration with no name")
	}
}

func TestRegisterRefusesAMigrationWithNoFn(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(Migration{
		Name: "no-fn",
		From: DescribeSchema(reflect.TypeOf(personV1{})),
		To:   DescribeSchema(reflect.TypeOf(personV2{})),
	})
	if err == nil {
		t.Fatal("Register accepted a migration with no transform function")
	}
}

func TestRegisterRefusesAMigrationToItself(t *testing.T) {
	same := DescribeSchema(reflect.TypeOf(personV1{}))
	registry := NewRegistry()
	err := registry.Register(Migration{
		Name: "noop",
		From: same,
		To:   same,
		Fn:   splitName,
	})
	if err == nil {
		t.Fatal("Register accepted a migration whose From and To are the same identity")
	}
}

func TestRegisterRefusesASecondMigrationForTheSameFrom(t *testing.T) {
	from := DescribeSchema(reflect.TypeOf(personV1{}))
	to := DescribeSchema(reflect.TypeOf(personV2{}))

	registry := NewRegistry()
	if err := registry.Register(Migration{Name: "first", From: from, To: to, Fn: splitName}); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := registry.Register(Migration{Name: "second", From: from, To: to, Fn: splitName})
	if err == nil {
		t.Fatal("Register accepted a second migration for an identity that already has one")
	}
}

func TestFindReportsAbsence(t *testing.T) {
	registry := NewRegistry()
	_, found := registry.Find(DescribeSchema(reflect.TypeOf(personV1{})))
	if found {
		t.Error("Find reported a migration in an empty registry")
	}
}

func TestFindReportsWhatWasRegistered(t *testing.T) {
	from := DescribeSchema(reflect.TypeOf(personV1{}))
	to := DescribeSchema(reflect.TypeOf(personV2{}))

	registry := NewRegistry()
	if err := registry.Register(Migration{Name: "person-v1-v2", From: from, To: to, Fn: splitName}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	found, ok := registry.Find(from)
	if !ok {
		t.Fatal("Find did not report the registered migration")
	}
	if found.Name != "person-v1-v2" {
		t.Errorf("Name = %q, want %q", found.Name, "person-v1-v2")
	}
}

// The zero-value Registry from a struct literal is not usable until
// constructed, but a nil migrations map must not panic on Find.
func TestFindOnZeroValueRegistryDoesNotPanic(t *testing.T) {
	var registry Registry
	_, found := registry.Find(DescribeSchema(reflect.TypeOf(personV1{})))
	if found {
		t.Error("a zero-value registry reported finding a migration")
	}
}
