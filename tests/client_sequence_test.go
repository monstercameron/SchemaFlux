package tests

import (
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// Building B after A is the sequence that used to repoint A: every
// WithProviderInstance call, and every Client constructor, wrote the one
// package-level provider that every operation read unconditionally. A client's
// own context now carries its snapshot, so A keeps answering as A.
//
// This lives in the black-box suite rather than beside the client because it
// drives a real operation (Format) from the root package, and the client
// package cannot import the package that imports it.
func TestClientsBuiltInSequenceDoNotRepointEachOther(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	clientA := schemaflux.NewClient("").WithProviderInstance(testfixtures.NewNamed("client-a"))
	_ = schemaflux.NewClient("").WithProviderInstance(testfixtures.NewNamed("client-b"))

	got, err := schemaflux.Format("payload", "uppercase",
		schemaflux.OpOptions{Context: clientA.Context(context.Background())})
	if err != nil {
		t.Fatalf("Format via clientA: %v", err)
	}
	if !strings.Contains(got, "client-a") {
		t.Errorf("Format via clientA answered %q; building clientB repointed clientA", got)
	}
}
