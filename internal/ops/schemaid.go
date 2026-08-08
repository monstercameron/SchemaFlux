package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Schema identity.
//
// A generated schema is used in four places that all have to agree about which
// schema they mean: the prompt the model sees, the cache key a repeat request
// resolves to, the fixture a replay compares against, and the stored result
// somebody reads back next year. Until this existed, the answer was "the Go
// type's name", which changes when a field changes and does not change when a
// field's *type* changes -- so a cache could serve an answer produced by a
// different contract, and a stored result could not say which contract produced
// it.
//
// The hash is over the emitted schema rather than over the Go type, because the
// schema is the thing that was actually sent. Renaming a Go field without
// changing its json tag changes the type and not the contract, and the hash
// should follow the contract.

// SchemaDescriptor identifies a schema.
type SchemaDescriptor struct {
	// Name is a stable, provider-safe identifier for the shape, derived from
	// the Go type.
	Name string

	// Version is the contract version. Bumping it is a deliberate act -- see
	// the compatibility rules in S-011 -- and it is part of the identity, so a
	// v2 answer is never served from a v1 cache.
	Version string

	// Hash is a digest of the emitted schema. Two types that produce the same
	// JSON Schema have the same hash, because they are the same contract.
	Hash string

	// Dialect names the schema flavour, so a fixture recorded against one is
	// not replayed against another.
	Dialect string
}

// String renders the identity compactly, for logs, cache keys, and provenance.
func (d SchemaDescriptor) String() string {
	if d.Name == "" {
		return "anonymous@" + d.Hash
	}
	return fmt.Sprintf("%s/%s@%s", d.Name, d.Version, d.Hash)
}

// SchemaVersionDefault is the version a type carries when it does not say.
//
// Types do not carry a version today; the field exists so that stored results
// and cache keys have somewhere to put one when S-011's evolution rules land,
// rather than needing a migration to gain the concept.
const SchemaVersionDefault = "v1"

// SchemaDialect names what GenerateJSONSchema emits.
const SchemaDialect = "json-schema-2020-12-strict"

// DescribeSchema builds the identity for a Go type.
func DescribeSchema(targetType reflect.Type) SchemaDescriptor {
	descriptor := SchemaDescriptor{
		Name:    schemaNameFor(targetType),
		Version: SchemaVersionDefault,
		Dialect: SchemaDialect,
	}

	schema := GenerateJSONSchema(targetType)
	if schema == nil {
		// A type strict mode cannot express still has an identity, because a
		// prompt-only call still has a contract: the prose schema. Hashing that
		// keeps the two paths distinguishable rather than collapsing every
		// unexpressible type onto one hash.
		descriptor.Dialect = "prose"
		descriptor.Hash = digestOf(GenerateTypeSchema(targetType))
		return descriptor
	}

	descriptor.Hash = digestOf(canonicalJSON(schema))
	return descriptor
}

// canonicalJSON renders a value with map keys sorted at every level, so the
// same schema always produces the same bytes.
//
// encoding/json already sorts map keys, but only for map[string]any; a schema
// built from nested maps and slices is exactly that, so this is mostly a
// guarantee in writing. It matters because the hash is a contract identity: two
// runs that produce the same schema must produce the same hash, and Go's map
// iteration order is not something to leave to chance -- CA-001 is the same
// lesson about prompts.
func canonicalJSON(value any) string {
	normalized := normalizeForHash(value)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func normalizeForHash(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		ordered := make(map[string]any, len(typed))
		for _, key := range keys {
			ordered[key] = normalizeForHash(typed[key])
		}
		return ordered

	case []any:
		normalized := make([]any, 0, len(typed))
		for _, entry := range typed {
			normalized = append(normalized, normalizeForHash(entry))
		}
		return normalized

	case []string:
		// Property lists: sorted, because "required" is a set and its order
		// carries no meaning a hash should be sensitive to.
		sorted := append([]string(nil), typed...)
		sort.Strings(sorted)
		return sorted

	default:
		return value
	}
}

// digestOf is the short hash used in identities. Twelve hex characters is 48
// bits: enough that a collision between two schemas in one program is not a
// thing that happens, and short enough to read in a log line.
func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:12]
}

// SchemaCacheKey is the identity a cache or a prompt-prefix key should use.
//
// It carries the operation and its version alongside the schema, because a
// cache keyed on the schema alone serves an answer produced by a different
// question. P-009 and MW-004 both need this and neither should invent its own.
func SchemaCacheKey(operation, operationVersion string, descriptor SchemaDescriptor) string {
	parts := []string{operation, operationVersion, descriptor.Name, descriptor.Version, descriptor.Hash, descriptor.Dialect}
	for i, part := range parts {
		if strings.TrimSpace(part) == "" {
			parts[i] = "-"
		}
	}
	return strings.Join(parts, ":")
}
