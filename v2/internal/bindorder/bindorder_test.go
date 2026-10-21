// Package bindorder holds one test: that a package which imports the root of
// version 2 for its types, as every subpackage of v2 does, finds the hooks the
// root registers with internal/bind already in place when its own
// initialisation runs, both in a package-level variable and in an init
// function. It has no non-test file because it is not part of the library: it
// stands in for exp, redact, flow, op, provider, mw and schemafluxtest.
package bindorder

import (
	"context"
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// fake is a Provider written outside the root, as a caller's would be.
type fake struct{}

func (fake) Name() string { return "fake" }
func (fake) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{}, nil
}

// seen is what this package saw of internal/bind while it was initialising.
type seen struct {
	registered   bool
	converted    error
	toInternal   llm.Provider
	fromInternal any
}

// byVariable is read in a package-level initialiser, which runs before every
// init function of this package.
var byVariable = look()

// byInit is read in an init function.
var byInit seen

func init() { byInit = look() }

// look reads internal/bind the way a subpackage would at its own init.
func look() seen {
	s := seen{registered: bind.Registered()}
	s.converted = bind.ConvertError("order", types.Newf(types.KindTimeout, "order", "slow"))
	s.toInternal = bind.ToInternal(fake{})
	s.fromInternal = bind.FromInternal(s.toInternal)
	return s
}

func TestTheRootsHooksAreRegisteredWhenAPackageThatImportsItInitialises(t *testing.T) {
	for name, got := range map[string]seen{"a package-level variable": byVariable, "an init function": byInit} {
		t.Run(name, func(t *testing.T) {
			if !got.registered {
				t.Fatal("bind.Registered() was false: the root had not registered when this package initialised")
			}
			var e *schemaflux.Error
			if !errors.As(got.converted, &e) || e.Kind != schemaflux.ErrTimeout || e.Op != "order" {
				t.Errorf("bind.ConvertError gave %#v, want the root's *Error of kind timeout for op order", got.converted)
			}
			if got.toInternal == nil || got.toInternal.Name() != "fake" {
				t.Errorf("bind.ToInternal gave %v, want an adapter of the Provider (the unregistered stand-in is named unregistered)", got.toInternal)
			}
			if got.fromInternal != any(fake{}) {
				t.Errorf("bind.FromInternal(bind.ToInternal(p)) = %#v, want p", got.fromInternal)
			}
		})
	}
}
