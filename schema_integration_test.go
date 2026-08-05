package schemaflux_test

import (
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// A record with fields the caller has excluded from JSON. If the schema
// describes them, their names leave the process on every call and the model is
// asked for values that will be discarded.
type patient struct {
	Name        string `json:"name"`
	Diagnosis   string `json:"diagnosis"`
	SSN         string `json:"-"`
	InternalRef string `json:"-"`
	CaseNotes   string `json:"-"`
}

// End to end: the prompt the stack actually sends must not name an excluded
// field.
func TestIntegrationExcludedFieldsNeverReachTheProvider(t *testing.T) {
	provider := withScriptedProvider(t, `{"name":"Ada","diagnosis":"none"}`, nil)

	if _, err := schemaflux.Extract[patient]("Ada, no diagnosis", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("no request was recorded")
	}

	var sent strings.Builder
	for _, request := range provider.requests {
		sent.WriteString(renderRequest(request))
	}
	body := sent.String()

	for _, excluded := range []string{"SSN", "InternalRef", "CaseNotes"} {
		if strings.Contains(body, excluded) {
			t.Errorf("the request names the excluded field %q", excluded)
		}
	}
	for _, expected := range []string{"name", "diagnosis"} {
		if !strings.Contains(body, expected) {
			t.Errorf("the request is missing %q", expected)
		}
	}
}

// The same request, made twice with the same options, must produce byte-
// identical prompts. Anything else defeats a prefix cache and makes a run
// irreproducible.
func TestIntegrationRepeatedCallsSendIdenticalBytes(t *testing.T) {
	hints := map[string]string{
		"alpha": "a", "bravo": "b", "charlie": "c", "delta": "d", "echo": "e",
		"foxtrot": "f", "golf": "g", "hotel": "h", "india": "i", "juliet": "j",
	}

	provider := withScriptedProvider(t, `{"name":"Ada","diagnosis":"none"}`, nil)

	for run := 0; run < 20; run++ {
		opts := schemaflux.NewExtractOptions()
		opts.SchemaHints = hints
		if _, err := schemaflux.Extract[patient]("Ada", opts); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	}

	if len(provider.requests) < 2 {
		t.Fatalf("expected repeated requests, got %d", len(provider.requests))
	}

	first := renderRequest(provider.requests[0])
	for i, request := range provider.requests[1:] {
		if got := renderRequest(request); got != first {
			t.Fatalf("request %d differs from the first; a map is being rendered unsorted", i+1)
		}
	}
}

func renderRequest(request schemaflux.CompletionRequest) string {
	return strings.Join([]string{request.Model, request.SystemPrompt, request.UserPrompt}, "\x00")
}

// Example_extractOmitsExcludedFields shows that a field the caller has kept out
// of JSON is also kept out of the prompt. It runs under go test with a scripted
// provider: no credential, no spend.
func Example_extractOmitsExcludedFields() {
	provider := &scriptedProvider{body: `{"name":"Ada Lovelace","diagnosis":"none"}`}
	schemaflux.NewClient("example-key").WithProviderInstance(provider)

	record, err := schemaflux.Extract[patient](
		"Ada Lovelace, no diagnosis on file.",
		schemaflux.NewExtractOptions())
	if err != nil {
		fmt.Println("extract failed:", err)
		return
	}

	fmt.Println("name:", record.Name)

	sent := renderRequest(provider.requests[0])
	fmt.Println("prompt mentions SSN:", strings.Contains(sent, "SSN"))
	fmt.Println("prompt mentions diagnosis:", strings.Contains(sent, "diagnosis"))

	// Output:
	// name: Ada Lovelace
	// prompt mentions SSN: false
	// prompt mentions diagnosis: true
}
