package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Schema migrations (S-013).
//
// S-011 built the classification of what changed between two schemas, and
// S-002 (schemaid.go) built the identity a result carries: a
// SchemaDescriptor names the exact contract that produced it. What was
// missing is the third piece: a deterministic function from one stored shape
// to another, and a registry that finds the right one for a given stored
// identity. It was not built alongside the identity because nothing in this
// library stores a result yet -- writing the machinery first is building for
// an imagined caller, and S-013 was filed rather than built for exactly that
// reason until now.
//
// A migration is keyed on the *identity* it starts from, not the Go type,
// because the same Go type can have produced two different schemas over time
// -- a field added, a rule tightened -- and only one of them is what a given
// stored blob actually is. SchemaDescriptor.String() (name/version@hash) is
// that identity in comparable form.

// Migration is a deterministic transform from one stored schema identity to
// another.
type Migration struct {
	// Name identifies the migration itself, independent of the schemas it
	// connects -- e.g. "person-v1-split-name-v2" rather than just "v1 to v2".
	// It is the provenance a caller records next to the result it produced:
	// which migration ran, not merely that one did.
	Name string

	// From is the stored identity this migration applies to.
	From SchemaDescriptor

	// To is the identity this migration's output is claimed to satisfy.
	// Migrate checks the claim against the caller's actual target type after
	// running Fn, rather than trusting it -- a migration that produces the
	// wrong shape is exactly the failure this exists to catch.
	To SchemaDescriptor

	// Fn transforms the stored bytes into bytes the new schema expects. An
	// error here is a failed migration, not a value to decode through and
	// hope.
	Fn func(json.RawMessage) (json.RawMessage, error)
}

// Registry finds the migration registered for a stored schema identity.
//
// Keyed on SchemaDescriptor.String() rather than the struct itself, because a
// caller reconstructs a descriptor from stored metadata (name, version, hash,
// dialect) and two descriptors built separately have to compare equal by
// content, not by identity.
type Registry struct {
	mu         sync.RWMutex
	migrations map[string]Migration
}

// NewRegistry returns an empty migration registry.
func NewRegistry() *Registry {
	return &Registry{migrations: map[string]Migration{}}
}

// Register adds a migration, or refuses one that cannot be applied
// unambiguously later:
//
//   - no Name: there would be nothing to record as provenance once it ran.
//   - no Fn: nothing to run.
//   - From and To are the same identity: that is not a change, so it is not a
//     migration.
//   - a migration is already registered for From: one stored identity has
//     exactly one next step, so a second registration would make Find's
//     answer depend on registration order instead of the schema.
func (r *Registry) Register(m Migration) error {
	if m.Name == "" {
		return &types.OperationError{
			Kind:    types.KindConfiguration,
			Op:      "Register",
			Message: "a migration must have a name -- it is the provenance a migrated result records",
		}
	}
	if m.Fn == nil {
		return &types.OperationError{
			Kind:    types.KindConfiguration,
			Op:      "Register",
			Message: fmt.Sprintf("migration %q has no transform function", m.Name),
		}
	}
	from := m.From.String()
	to := m.To.String()
	if from == to {
		return &types.OperationError{
			Kind:    types.KindConfiguration,
			Op:      "Register",
			Message: fmt.Sprintf("migration %q has the same From and To identity (%s) -- that is not a migration", m.Name, from),
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.migrations == nil {
		r.migrations = map[string]Migration{}
	}
	if existing, found := r.migrations[from]; found {
		return &types.OperationError{
			Kind: types.KindConfiguration,
			Op:   "Register",
			Message: fmt.Sprintf(
				"%s already has a migration registered (%q); registering %q for the same identity would make the next step depend on registration order",
				from, existing.Name, m.Name,
			),
		}
	}
	r.migrations[from] = m
	return nil
}

// Find returns the migration registered for a stored identity, if any.
func (r *Registry) Find(from SchemaDescriptor) (Migration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, found := r.migrations[from.String()]
	return m, found
}

// Result records what Migrate did, so a caller storing the migrated value
// keeps the provenance next to it rather than re-deriving it later.
type Result struct {
	// Applied is false when the stored identity already matched the target
	// and no migration ran -- distinct from an error, and distinct from
	// silently looking the same as having migrated.
	Applied bool

	// MigrationName is which migration ran; empty when Applied is false.
	MigrationName string

	From SchemaDescriptor
	To   SchemaDescriptor

	// Data is the bytes to decode into the target type: migrated if Applied,
	// the original bytes otherwise.
	Data json.RawMessage
}

// Migrate takes bytes stored under the identity `from` and returns bytes
// that fit `targetType`, running exactly one registered migration if the
// identities differ and none if they already match.
//
// It does not chase a chain of migrations. A From with a registered
// migration always names exactly one To (Register enforces that), but
// nothing here walks on from that To to a further step -- composing several
// single-step migrations is the caller's decision to make, not a hop count
// this library guesses on their behalf.
func (r *Registry) Migrate(data json.RawMessage, from SchemaDescriptor, targetType reflect.Type) (Result, error) {
	to := DescribeSchema(targetType)

	if from.String() == to.String() {
		return Result{From: from, To: to, Data: data}, nil
	}

	migration, found := r.Find(from)
	if !found {
		return Result{}, &types.OperationError{
			Kind: types.KindSchemaViolation,
			Op:   "Migrate",
			Message: fmt.Sprintf(
				"no migration is registered from %s to %s -- register one, or decode the stored value under its original type first",
				from.String(), to.String(),
			),
		}
	}
	if migration.To.String() != to.String() {
		return Result{}, &types.OperationError{
			Kind: types.KindConfiguration,
			Op:   "Migrate",
			Message: fmt.Sprintf(
				"the migration registered from %s (%q) produces %s, not the requested %s",
				from.String(), migration.Name, migration.To.String(), to.String(),
			),
		}
	}

	migrated, err := migration.Fn(data)
	if err != nil {
		// The cause is kept for errors.Unwrap, not stringified into Message --
		// a caller-authored Fn's error can carry the payload it was
		// transforming, and Message is what every logger prints.
		return Result{}, &types.OperationError{
			Kind:    types.KindSchemaViolation,
			Op:      "Migrate",
			Message: fmt.Sprintf("migration %q failed", migration.Name),
			Cause:   err,
		}
	}

	return Result{
		Applied:       true,
		MigrationName: migration.Name,
		From:          from,
		To:            to,
		Data:          migrated,
	}, nil
}
