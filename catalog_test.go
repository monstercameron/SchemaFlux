package schemaflux_test

import (
	"os"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// PS-002. The catalogue is only worth having if it cannot fall behind the
// surface it describes, so it is checked against `testdata/api_surface.txt` —
// the snapshot that already fails the build when the public API moves.
//
// Without this, the catalogue is a comment: correct on the day it was written
// and quietly wrong afterwards, which is how the list of operations got to a
// state where nobody could say which ones were load-bearing.

// exportedFunctions reads the API surface snapshot, which is the same file
// TestPublicAPISurface pins. Reading it rather than reflecting over the package
// is deliberate: the snapshot is what a reviewer looks at when the API moves,
// so tying the catalogue to it means one review covers both.
func exportedFunctions(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile("testdata/api_surface.txt")
	if err != nil {
		t.Fatalf("reading the API surface snapshot: %v", err)
	}

	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "func ") {
			continue
		}
		names = append(names, strings.TrimPrefix(line, "func "))
	}
	return names
}

// infrastructure is the exported surface that is not an operation: clients,
// option constructors, combinators, control flow, logging, and plumbing. It is
// an explicit list rather than a prefix rule so that adding an operation whose
// name happens to start with "New" cannot slip through uncatalogued.
var infrastructure = map[string]struct{}{
	"NewClient": {}, "GetDefaultClient": {}, "SetDefaultClient": {},
	"Init": {}, "InitWithEnv": {},
	"GetLogger": {}, "GetLogEntries": {}, "ResetLogEntries": {}, "ConfigureLogging": {}, "SetLogLevel": {},
	"ConfigureRequestTracking": {}, "GetRequestTrackingConfig": {}, "ExtractRequestTracking": {},
	"InjectRequestTracking": {}, "RequestTrackingFromContext": {},
	"KindOf": {}, "RetryAfterFrom": {}, "StatusCodeFrom": {},
	"MapReduce": {}, "MapReduceFlat": {},
	"Escalate": {}, "Until": {}, "Fallback": {}, "ExactAgreement": {},
	"Checkpoint": {}, "NewMemoryCheckpointStore": {},
	"Like": {}, "Otherwise": {}, "When": {},
	"WithRequestID": {}, "WithCorrelationID": {}, "WithRequestTrackingMetadata": {},
	"NewOp": {}, "MustNewOp": {}, "RunOp": {}, "RunOpResult": {},
	"Preflight": {}, "NewPlanBuilder": {}, "RunOpBatch": {}, "RunOpBatchResult": {}, "RunOpMany": {},
	"WithDiagnosticSink": {},
	"StreamSummarize":    {}, "StreamRewrite": {}, "StreamTranslate": {}, "StreamExpand": {},
	"Catalog": {}, "Describe": {},
}

func TestEveryExportedOperationIsCatalogued(t *testing.T) {
	names := exportedFunctions(t)
	if len(names) < 100 {
		t.Fatalf("only %d exported functions found; the snapshot is not being read", len(names))
	}

	var uncatalogued []string
	for _, name := range names {
		if _, isInfrastructure := infrastructure[name]; isInfrastructure {
			continue
		}
		// The fluent builders (Extracting, Choosing, ...) are entry points to
		// the operation of the same stem, catalogued under that stem.
		if _, found := schemaflux.Describe(name); found {
			continue
		}
		if strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Options") {
			continue
		}
		if isFluentEntryPoint(name) {
			continue
		}
		uncatalogued = append(uncatalogued, name)
	}

	for _, name := range uncatalogued {
		t.Errorf("%s is exported and not in the catalogue: give it a category and a tier in catalog.go, "+
			"or add it to the infrastructure list if it is not an operation", name)
	}
}

// irregularFluentSpellings are the builder names whose stem is not derivable
// from the operation's. They are listed rather than guessed at: a heuristic
// that silently accepts an unrecognised name would let a genuinely
// uncatalogued operation through, which is the one thing this check exists to
// prevent.
var irregularFluentSpellings = map[string]string{
	"Asking":             "Question",
	"Inferring":          "Infer",
	"VerifyingClaim":     "VerifyClaim",
	"EnrichingInPlace":   "EnrichInPlace",
	"CheckingSimilarity": "Similar",
	"LLMRedacting":       "RedactLLM",
}

// isFluentEntryPoint reports the builder spellings — Extracting for Extract,
// Choosing for Choose, Settling for Settle. They are the same operation under a
// participle, so they inherit its entry rather than getting their own.
func isFluentEntryPoint(name string) bool {
	if operation, irregular := irregularFluentSpellings[name]; irregular {
		_, found := schemaflux.Describe(operation)
		return found
	}

	for _, suffix := range []string{"ing", "ingInto", "ingOne", "ingBatch", "ingText", "ingField", "ingAdversarially"} {
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		stem := strings.TrimSuffix(name, suffix)
		if stem == "" {
			continue
		}
		for _, candidate := range []string{stem, stem + "e", stem + "y", strings.TrimSuffix(stem, "t") + "te"} {
			if _, found := schemaflux.Describe(candidate); found {
				return true
			}
		}
	}
	return false
}

// A catalogue entry naming an operation that no longer exists is the other
// direction of the same rot.
func TestTheCatalogueNamesNothingThatIsGone(t *testing.T) {
	exported := map[string]struct{}{}
	for _, name := range exportedFunctions(t) {
		exported[name] = struct{}{}
	}

	for _, entry := range schemaflux.Catalog() {
		if _, found := exported[entry.Name]; !found {
			t.Errorf("the catalogue lists %s, which is not exported; remove the entry", entry.Name)
		}
	}
}

// Every deprecated entry has to name a replacement that exists, or the
// catalogue tells a caller to migrate to nothing.
func TestEveryDeprecatedEntryNamesALiveReplacement(t *testing.T) {
	for _, entry := range schemaflux.Catalog() {
		if entry.Tier != schemaflux.TierDeprecated {
			continue
		}
		if entry.ReplacedBy == "" {
			t.Errorf("%s is deprecated and names no replacement", entry.Name)
			continue
		}
		replacement, found := schemaflux.Describe(entry.ReplacedBy)
		if !found {
			t.Errorf("%s points at %s, which is not in the catalogue", entry.Name, entry.ReplacedBy)
			continue
		}
		if replacement.Tier == schemaflux.TierDeprecated {
			t.Errorf("%s points at %s, which is itself deprecated", entry.Name, entry.ReplacedBy)
		}
	}
}

func TestEveryEntryHasACategoryAndATier(t *testing.T) {
	entries := schemaflux.Catalog()
	if len(entries) < 50 {
		t.Fatalf("the catalogue holds only %d entries", len(entries))
	}

	for _, entry := range entries {
		if entry.Category == "" {
			t.Errorf("%s has no category", entry.Name)
		}
		if entry.Tier == "" {
			t.Errorf("%s has no tier", entry.Name)
		}
	}
}

// The point of the tiers is that they are not all the same. A catalogue where
// everything is stable is a catalogue that has stopped distinguishing.
func TestTheTiersActuallyDistinguish(t *testing.T) {
	counts := map[schemaflux.OperationTier]int{}
	for _, entry := range schemaflux.Catalog() {
		counts[entry.Tier]++
	}

	for _, tier := range []schemaflux.OperationTier{
		schemaflux.TierStable, schemaflux.TierExperimental, schemaflux.TierDeprecated,
	} {
		if counts[tier] == 0 {
			t.Errorf("no operation is %s; the tier is not distinguishing anything", tier)
		}
	}
}
