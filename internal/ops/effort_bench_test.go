package ops

import (
	"strings"
	"testing"
)

// The number gate reads the whole of every Classify, Similar and Deduplicate
// input, so what it costs is paid on every such call. The worst case is the
// one benchmarked: text with no number in it, which is read to the end.
func BenchmarkJudgmentEffort(b *testing.B) {
	sentence := "The customer wrote about an invoice that looked wrong to them and asked for a correction. "
	inputs := map[string]string{
		"english_1KB":  strings.Repeat(sentence, 12),
		"english_30KB": strings.Repeat(sentence, 340),
		"accented_30KB": strings.Repeat(
			"Le client a écrit à propos d'une facture erronée et a demandé une correction immédiate. ", 340),
		"japanese_30KB": strings.Repeat("お客様は請求書が間違っているように見えると書いてきました。", 370),
	}
	categories := []string{"billing", "technical", "sales", "account"}

	for name, input := range inputs {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				texts := append([]string{input, ""}, categories...)
				if judgmentEffort(texts...) == "" {
					b.Fatal("the input has no number in it and the gate found one")
				}
			}
		})
	}

	// And the common early exit: a number near the start.
	b.Run("a_number_in_the_first_line", func(b *testing.B) {
		input := "Order 4417 arrived damaged. " + strings.Repeat(sentence, 340)
		b.ReportAllocs()
		for b.Loop() {
			if judgmentEffort(input) != "" {
				b.Fatal("the gate missed the number")
			}
		}
	})
}
