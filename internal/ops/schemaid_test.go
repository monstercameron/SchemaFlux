package ops

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

type schemaIDPerson struct {
	Name  string    `json:"name"`
	Age   int       `json:"age"`
	Since time.Time `json:"since"`
}

// S-002. A generated schema is used in four places that all have to agree about
// which schema they mean: the prompt, the cache key, the replay fixture, and the
// stored result somebody reads back next year.
func TestSchemaIdentityIsStable(t *testing.T) {
	first := DescribeSchema(reflect.TypeOf(schemaIDPerson{}))

	for i := 0; i < 20; i++ {
		again := DescribeSchema(reflect.TypeOf(schemaIDPerson{}))
		if again != first {
			t.Fatalf("the identity changed between runs:\n  %+v\n  %+v", first, again)
		}
	}

	if first.Name == "" || first.Hash == "" || first.Version == "" || first.Dialect == "" {
		t.Errorf("an incomplete identity: %+v", first)
	}
}

// The hash follows the contract, not the Go type. Renaming a Go field without
// changing its json tag changes the type and not what was sent.
func TestTheHashFollowsTheContractNotTheGoType(t *testing.T) {
	type original struct {
		Name string `json:"name"`
	}
	type renamedField struct {
		FullName string `json:"name"` // same contract, different Go field
	}
	type renamedTag struct {
		Name string `json:"full_name"` // different contract
	}
	type retyped struct {
		Name int `json:"name"` // different contract
	}

	base := DescribeSchema(reflect.TypeOf(original{}))

	if got := DescribeSchema(reflect.TypeOf(renamedField{})); got.Hash != base.Hash {
		t.Errorf("renaming a Go field changed the hash: %s vs %s", got.Hash, base.Hash)
	}
	if got := DescribeSchema(reflect.TypeOf(renamedTag{})); got.Hash == base.Hash {
		t.Error("renaming the json tag did not change the hash")
	}
	if got := DescribeSchema(reflect.TypeOf(retyped{})); got.Hash == base.Hash {
		t.Error("changing a field's type did not change the hash")
	}
}

// Adding a field changes the contract, which is what makes the hash usable as a
// cache key: an answer produced against the old shape must not be served for
// the new one.
func TestAddingAFieldChangesTheHash(t *testing.T) {
	type before struct {
		Name string `json:"name"`
	}
	type after struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	if DescribeSchema(reflect.TypeOf(before{})).Hash == DescribeSchema(reflect.TypeOf(after{})).Hash {
		t.Error("adding a field left the hash unchanged")
	}
}

// A type strict mode cannot express still has an identity, because a
// prompt-only call still has a contract -- the prose schema. Collapsing every
// such type onto one hash would make the cache key useless for exactly the
// types that need it most.
func TestUnexpressibleTypesStillHaveAnIdentity(t *testing.T) {
	type withMap struct {
		Labels map[string]string `json:"labels"`
	}
	type withOtherMap struct {
		Tags map[string]int `json:"tags"`
	}

	first := DescribeSchema(reflect.TypeOf(withMap{}))
	second := DescribeSchema(reflect.TypeOf(withOtherMap{}))

	if first.Hash == "" {
		t.Fatal("no hash for a map-valued type")
	}
	if first.Hash == second.Hash {
		t.Error("two different unexpressible types share a hash")
	}
	if first.Dialect == second.Dialect && first.Dialect != "prose" {
		t.Errorf("dialect = %q, want prose for a type strict mode cannot express", first.Dialect)
	}
}

// The cache key carries the operation as well as the schema, because a cache
// keyed on the schema alone serves an answer produced by a different question.
func TestTheCacheKeyCarriesTheOperation(t *testing.T) {
	descriptor := DescribeSchema(reflect.TypeOf(schemaIDPerson{}))

	extract := SchemaCacheKey("extract", "v1", descriptor)
	generate := SchemaCacheKey("generate", "v1", descriptor)
	extractV2 := SchemaCacheKey("extract", "v2", descriptor)

	if extract == generate {
		t.Error("two different operations share a cache key")
	}
	if extract == extractV2 {
		t.Error("two versions of an operation share a cache key")
	}
	if !strings.Contains(extract, descriptor.Hash) {
		t.Error("the key does not carry the schema hash")
	}
}

// The rendered identity is readable, because it goes in logs and provenance.
func TestTheIdentityRendersReadably(t *testing.T) {
	descriptor := DescribeSchema(reflect.TypeOf(schemaIDPerson{}))
	rendered := descriptor.String()

	if !strings.Contains(rendered, "schemaIDPerson") && !strings.Contains(rendered, "schemaperson") {
		t.Errorf("the rendered identity does not name the type: %s", rendered)
	}
	if !strings.Contains(rendered, descriptor.Hash) {
		t.Errorf("the rendered identity does not carry the hash: %s", rendered)
	}
}

// The identity has to reach the result, or it is a fact the library knows and
// the caller cannot use.
func TestTheSchemaIdentityReachesTheResultMetadata(t *testing.T) {
	var sawSchemaID string
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, _ string, opts types.OpOptions) (string, error) {
		sawSchemaID = opts.SchemaID
		return `{"name":"Ada","age":36,"since":"2026-01-02T15:04:05Z"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	if _, err := Extract[schemaIDPerson]("Ada, 36", NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if sawSchemaID == "" {
		t.Fatal("the operation sent no schema identity")
	}

	// It must match what the descriptor says, so the two cannot drift.
	want := DescribeSchema(reflect.TypeOf(schemaIDPerson{})).String()
	if sawSchemaID != want {
		t.Errorf("the operation sent %q, the descriptor says %q", sawSchemaID, want)
	}
	if !strings.Contains(want, "@") {
		t.Errorf("the identity is not in name/version@hash form: %s", want)
	}
}
