package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Documentation that overstates a security property is a defect in the same way
// a wrong return value is: a caller reads it, believes it, and ships. These
// checks keep the disclosures in place, because a doc comment has nothing else
// holding it there.

// docComment returns the doc comment of a top-level function, or "".
func docComment(t *testing.T, name string) string {
	t.Helper()

	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || filepath.Ext(fileName) != ".go" || strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, fileName, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", fileName, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name.Name != name {
				continue
			}
			if function.Doc == nil {
				return ""
			}
			return function.Doc.Text()
		}
	}
	t.Fatalf("no top-level function named %s", name)
	return ""
}

// The redaction operations no longer carry the S1 defects the review found:
// T-07 (substring field matching), T-08 (patterns that missed their formats),
// T-09 (an audit that reported nothing), T-11 (a reversible permutation with a
// guessable seed), and the offset half of T-13 are all closed.
//
// What replaces the warning is a description of what the mechanism does and
// does not do, because "it works now" is not a useful thing to tell a caller
// evaluating it for compliance use. These checks keep that description in
// place; a doc comment has nothing else holding it there.
func TestRedactionDocumentsWhatItDetects(t *testing.T) {
	cases := []struct {
		name    string
		mustSay []string
	}{
		{"Redact", []string{
			"whole names, not substrings", // T-07: how field matching works
			"Luhn",                        // T-08: how a card is recognised
			"not a classifier",            // the limit
			"redact` struct tag",          // the reliable path
		}},
		// Updated when OP-503 and OP-507 landed: the phrases have to describe
		// the mechanism that exists now, not the one that was being warned
		// about. RedactWithResult reports rather than returning an empty map;
		// RedactLLM locates spans by their text, so the interesting disclosure
		// is what happens to a span it cannot find, not offset validation.
		{"RedactWithResult", []string{"reports what it replaced", "audit"}},
		{"RedactLLM", []string{"offsets", "hint", "dropped", "not classification"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := docComment(t, tc.name)
			if doc == "" {
				t.Fatalf("%s has no doc comment at all", tc.name)
			}
			for _, phrase := range tc.mustSay {
				if !strings.Contains(doc, phrase) {
					t.Errorf("the %s doc comment does not mention %q", tc.name, phrase)
				}
			}
		})
	}
}

// Jumbling is the one part that is still not a privacy mechanism, and it has to
// say so: a permutation preserves length, alphabet, and frequency whatever the
// seed.
func TestJumbleIsDocumentedAsObfuscation(t *testing.T) {
	for _, name := range []string{"Redact", "jumbleString"} {
		t.Run(name, func(t *testing.T) {
			doc := docComment(t, name)
			if !strings.Contains(doc, "not anonymization") {
				t.Errorf("the %s doc comment does not say jumbling is not anonymization", name)
			}
		})
	}

	doc := docComment(t, "jumbleString")
	for _, phrase := range []string{"length", "alphabet", "frequency", "crypto/rand"} {
		if !strings.Contains(doc, phrase) {
			t.Errorf("the jumbleString doc comment does not mention %q", phrase)
		}
	}
}

// Project's Exclude is a real mechanism now, so its documentation has to state
// both what it guarantees and where the guarantee stops. It used to advertise
// "privacy filtering" over a prompt hint.
func TestProjectDocumentsWhatExcludeGuarantees(t *testing.T) {
	doc := docComment(t, "Project")
	if doc == "" {
		t.Fatal("Project has no doc comment")
	}

	for _, phrase := range []string{
		"before it is serialised",  // the guarantee
		"never reach the provider", // stated plainly
		"not a classifier",         // the limit
		"still sent",               // the concrete case the limit covers
	} {
		if !strings.Contains(doc, phrase) {
			t.Errorf("the Project doc comment does not say %q", phrase)
		}
	}

	// The old framing claimed a property the mechanism did not have.
	if strings.Contains(doc, "privacy filtering") {
		t.Error("the doc comment still advertises 'privacy filtering'")
	}
}

// The README describes what redaction detects and where it stops, because that
// is where someone evaluating the library looks first. It used to carry a
// NOT PRODUCTION READY warning; the defects behind that warning are closed, and
// what replaces it is a description rather than reassurance.
func TestREADMEDescribesRedaction(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	// Markdown wraps and this text is a blockquote, so the prose is flattened
	// before matching rather than being checked against whatever happened to
	// fit on one line today.
	var flattened []string
	for _, line := range strings.Split(string(raw), "\n") {
		flattened = append(flattened, strings.TrimPrefix(strings.TrimSpace(line), "> "))
	}
	readme := strings.Join(strings.Fields(strings.Join(flattened, " ")), " ")

	for _, phrase := range []string{
		"whole names, not substrings", // T-07
		"Luhn",                        // T-08
		"RedactWithResult` reports",   // T-09
		"not anonymization",           // T-11
		"not a classifier",            // the honest limit
		"nine-digit",                  // the deliberate miss, stated
	} {
		if !strings.Contains(readme, phrase) {
			t.Errorf("the README does not mention %q", phrase)
		}
	}

	// The warning is gone because the defects are, not because it was quietly
	// deleted: this fails if redaction regains a NOT PRODUCTION READY marker
	// without the README saying why.
	if strings.Contains(readme, "not production ready") {
		t.Error("the README still marks redaction not production ready; update this test with the reason")
	}
}

// Project's Exclude documentation has to describe a mechanism that exists. Each
// claim here is checked against the code by project_exclude_test.go; this
// checks the claim is actually made.
func TestProjectExcludeDocumentationMatchesTheMechanism(t *testing.T) {
	doc := docComment(t, "Project")

	claims := []struct {
		phrase string
		why    string
	}{
		{"before it is serialised", "when the strip happens"},
		{"never reach the provider", "what that buys"},
		{"case-insensitively", "how names are matched"},
		{"every level", "that nesting is covered"},
		{"scanned", "that the output is checked too"},
		{"not a classifier", "the limit"},
		{"still sent", "the concrete case the limit covers"},
		{"caller's job", "who owns the field list"},
	}

	for _, claim := range claims {
		t.Run(claim.why, func(t *testing.T) {
			if !strings.Contains(doc, claim.phrase) {
				t.Errorf("the documentation does not state %s (%q)", claim.why, claim.phrase)
			}
		})
	}
}
