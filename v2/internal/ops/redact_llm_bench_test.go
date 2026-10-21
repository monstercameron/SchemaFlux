package ops

import (
	"strings"
	"testing"
)

// benchRedactDocument is about 32,000 characters of mixed-alphabet prose with
// ten sensitive values in it, each several times: the size of document a
// redaction call is likely to carry at the top of a model's context.
func benchRedactDocument() (string, string) {
	const paragraph = "Kunde Jürgen Müller (juergen.mueller@example.de, +49 30 5550142) bat um Rückruf. " +
		"Dana Kim at dana.kim@example.com confirmed order 4111-2222-3333-4444 for 東京都渋谷区. " +
		"SSN 078-05-1120 was on file; the account key is sk-test-9f8e7d6c5b4a. Müllers Vertrag läuft weiter. "
	document := strings.Repeat(paragraph, 110)

	response := `{"spans":[` +
		`{"text":"Jürgen Müller","category":"name"},` +
		`{"text":"juergen.mueller@example.de","category":"email"},` +
		`{"text":"+49 30 5550142","category":"phone"},` +
		`{"text":"Dana Kim","category":"name"},` +
		`{"text":"dana.kim@example.com","category":"email"},` +
		`{"text":"4111-2222-3333-4444","category":"credit_card"},` +
		`{"text":"東京都渋谷区","category":"address"},` +
		`{"text":"078-05-1120","category":"ssn"},` +
		`{"text":"sk-test-9f8e7d6c5b4a","category":"api_key"},` +
		`{"text":"Müller","category":"name"}]}`
	return document, response
}

// The whole of what RedactLLM does after the model answers: parse, locate
// every value, merge, convert to character offsets, and splice the masks in.
func BenchmarkRedactLLMLocateAndMask(b *testing.B) {
	document, response := benchRedactDocument()
	opts := NewRedactLLMOptions()
	b.SetBytes(int64(len(document)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		spans, _, err := locateRedactSpans(response, document)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := applyRedactions(document, spans, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// Folding the document is done once a call, whatever the number of values.
func BenchmarkRedactLLMFoldText(b *testing.B) {
	document, _ := benchRedactDocument()
	b.SetBytes(int64(len(document)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = foldText(document)
	}
}

// One value searched in a folded document.
func BenchmarkRedactLLMFindOneValue(b *testing.B) {
	document, _ := benchRedactDocument()
	folded := foldText(document)
	pattern := foldValue("dana.kim@example.com")
	b.SetBytes(int64(len(document)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = folded.find(pattern)
	}
}

// The case the search was rewritten for: a long value inside a repetitive run.
func BenchmarkRedactLLMFindInARepetitiveRun(b *testing.B) {
	folded := foldText(strings.Repeat("A", 20000))
	pattern := foldValue(strings.Repeat("a", 10000))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = folded.find(pattern)
	}
}
