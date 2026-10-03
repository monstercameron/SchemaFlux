package tests

import (
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                                    allocs/op       B/op
//	CallPathExtractTypes/nested12/Transform                            952     117089
//	CallPathExtractTypes/nested12/Transform/parallel                   954     117328
//	CallPathExtractTypes/nested12/Strict                              4138     217871
//	CallPathExtractTypes/nested12/Strict/parallel                     4143     218953
//	CallPathExtractTypes/wide60/Transform                             1979     215573
//	CallPathExtractTypes/wide60/Transform/parallel                    1982     216856
//	CallPathExtractTypes/wide60/Strict                                3345     270788
//	CallPathExtractTypes/wide60/Strict/parallel                       3350     272258
//	CallPathExtractTypes/list20/Transform                              425      77821
//	CallPathExtractTypes/list20/Transform/parallel                     426      77859
//	CallPathExtractTypes/list20/Strict                                3237     170266
//	CallPathExtractTypes/list20/Strict/parallel                       3241     170631
//	CallPathExtractAtDefaultLoggingNested                              975     119296
//	CallPathExtractInputSize/1KB                                       295      61278
//	CallPathExtractInputSize/100KB                                     301     273928
//	CallPathExtractInputSize/1MB                                       303    2175184
//	CallPathExtractResultVersusExtract/Extract                         302     487050
//	CallPathExtractResultVersusExtract/ExtractResult                   330     692818
//	CallPathExtractResultVersusExtract/ExtractResult/small_input       322      61281
//	CallPathTransform                                                  371      25494
//	CallPathGenerate                                                   569      45716
//	CallPathClassify                                                   149      13836
//	CallPathAuditWithModel/0_policies                                  200      19077
//	CallPathAuditWithModel/3_policies                                  324      29109
//	CallPathAuditWithModel/10_policies                                 463      40300
//	CallPathValidateHybrid                                             266      22606
//	CallPathAssess13                                                   128      11197
//	CallPathAssess13Parallel                                           128      11206

// What one real call costs, operation by operation, through the exported
// functions as a caller makes them, with a provider that answers at once.
//
// bench_callpath_test.go measures Extract of a three-field struct, Is and
// Summarize. These add the rest of what a caller reaches: Extract of larger
// types and in both modes, Transform, Generate, Classify, the judgments
// (AuditWithModel, ValidateHybrid), Assess over thirteen questions, and the
// fluent-free envelope form ExtractResult. Everything the library does around
// a call is in each figure (options, request identity, schema, prompt, the
// provider call, pricing, metrics, the answer's decode and checks) and
// nothing a network would add.
//
// The logging is turned off for these (benchSilenceLogging), because at its
// default settings it writes to standard error and would be most of what is
// measured. The benchmarks that measure the logging are the ones named
// ...AtDefaultLogging here, and the logger's own benchmarks in
// internal/telemetry. Run with standard error discarded.
//
// The benchmarks:
//
//	BenchmarkCallPathExtractTypes/<type>/<mode>        Extract of nested12, wide60 and list20, Transform and Strict
//	BenchmarkCallPathExtractTypes/.../parallel         the same on every core
//	BenchmarkCallPathExtractAtDefaultLoggingNested     Extract of nested12 with the logging left at its default
//	BenchmarkCallPathExtractInputSize/<size>           Extract of a 1 KB, 100 KB and 1 MB string
//	BenchmarkCallPathExtractResultVersusExtract/<fn>   the envelope form against the plain one, on a 200 KB input
//	BenchmarkCallPathTransform                         Transform of an order into a summary
//	BenchmarkCallPathGenerate                          Generate of an order from a prompt
//	BenchmarkCallPathClassify                          Classify into four categories
//	BenchmarkCallPathAuditWithModel/<n policies>       the audit judgment with 0, 3, 10 policies
//	BenchmarkCallPathValidateHybrid                    the hybrid validation, one rule only a model can decide
//	BenchmarkCallPathAssess13                          thirteen questions in one call
//
// A parallel variant is given where concurrency is the point: the figure to
// read is its ns/op against the serial one divided by the number of cores. If
// the library shared nothing across calls they would be equal.

// extractBench runs Extract[T] through the call path with options built by
// mutate, serially or in parallel.
func extractBench[T any](b *testing.B, answer string, input any, mutate func(schemaflux.ExtractOptions) schemaflux.ExtractOptions, parallel bool) {
	b.Helper()
	ctx := benchContext(answer)
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	if mutate != nil {
		opts = mutate(opts)
	}
	if _, err := schemaflux.Extract[T](input, opts); err != nil {
		b.Fatalf("the benchmark's call fails before it is measured: %v", err)
	}
	b.ReportAllocs()
	if parallel {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := schemaflux.Extract[T](input, opts); err != nil {
					b.Error(err)
					return
				}
			}
		})
		return
	}
	for b.Loop() {
		if _, err := schemaflux.Extract[T](input, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCallPathExtractTypes(b *testing.B) {
	type shape struct {
		name string
		run  func(b *testing.B, mutate func(schemaflux.ExtractOptions) schemaflux.ExtractOptions, parallel bool)
	}
	text := benchText
	shapes := []shape{
		{"nested12", func(b *testing.B, m func(schemaflux.ExtractOptions) schemaflux.ExtractOptions, p bool) {
			extractBench[benchOrder](b, benchAnswerOf[benchOrder](b), text, m, p)
		}},
		{"wide60", func(b *testing.B, m func(schemaflux.ExtractOptions) schemaflux.ExtractOptions, p bool) {
			extractBench[benchWideOrder](b, benchAnswerOf[benchWideOrder](b), text, m, p)
		}},
		{"list20", func(b *testing.B, m func(schemaflux.ExtractOptions) schemaflux.ExtractOptions, p bool) {
			extractBench[benchOrderList](b, benchAnswerOf[benchOrderList](b), text, m, p)
		}},
	}
	modes := []struct {
		name   string
		mutate func(schemaflux.ExtractOptions) schemaflux.ExtractOptions
	}{
		{"Transform", nil},
		{"Strict", func(o schemaflux.ExtractOptions) schemaflux.ExtractOptions { return o.WithMode(schemaflux.Strict) }},
	}
	for _, s := range shapes {
		for _, m := range modes {
			b.Run(s.name+"/"+m.name, func(b *testing.B) {
				benchSilenceLogging(b)
				s.run(b, m.mutate, false)
			})
			b.Run(s.name+"/"+m.name+"/parallel", func(b *testing.B) {
				benchSilenceLogging(b)
				s.run(b, m.mutate, true)
			})
		}
	}
}

func BenchmarkCallPathExtractAtDefaultLoggingNested(b *testing.B) {
	benchDefaultLogging(b)
	b.Cleanup(func() { benchSilenceLogging(b) })
	extractBench[benchOrder](b, benchAnswerOf[benchOrder](b), benchText, nil, false)
}

// BenchmarkCallPathExtractInputSize varies the size of the input, which a
// prompt-sized copy costs per megabyte.
func BenchmarkCallPathExtractInputSize(b *testing.B) {
	answer := `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`
	for _, size := range []struct {
		name  string
		bytes int
	}{{"1KB", 1 << 10}, {"100KB", 100 << 10}, {"1MB", 1 << 20}} {
		input := benchTextOfSize(size.bytes)
		b.Run(size.name, func(b *testing.B) {
			benchSilenceLogging(b)
			b.SetBytes(int64(size.bytes))
			extractBench[benchInvoice](b, answer, input, nil, false)
		})
	}
}

// BenchmarkCallPathExtractResultVersusExtract: the envelope form rebuilds the
// options several more times and hashes the input once more after the answer.
func BenchmarkCallPathExtractResultVersusExtract(b *testing.B) {
	answer := `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`
	input := benchTextOfSize(200 << 10)
	ctx := benchContext(answer)
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)

	b.Run("Extract", func(b *testing.B) {
		benchSilenceLogging(b)
		b.SetBytes(int64(len(input)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := schemaflux.Extract[benchInvoice](input, opts); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ExtractResult", func(b *testing.B) {
		benchSilenceLogging(b)
		b.SetBytes(int64(len(input)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := schemaflux.ExtractResult[benchInvoice](input, opts); err != nil {
				b.Fatal(err)
			}
		}
	})
	smallOpts := opts
	b.Run("ExtractResult/small_input", func(b *testing.B) {
		benchSilenceLogging(b)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := schemaflux.ExtractResult[benchInvoice](benchText, smallOpts); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkCallPathTransform(b *testing.B) {
	benchSilenceLogging(b)
	benchContext(benchAnswerOf[benchOrderSummary](b))
	order := benchOrder{OrderID: "A-2207", Customer: "Dana Whitfield", Email: "dana@example.com", Total: 49,
		Items: []benchLineItem{{SKU: "T-1", Description: "Team plan", Quantity: 1, UnitPrice: 49}}}
	opts := schemaflux.NewTransformOptions()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := schemaflux.Transform[benchOrder, benchOrderSummary](order, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCallPathGenerate(b *testing.B) {
	benchSilenceLogging(b)
	benchContext(benchAnswerOf[benchOrder](b))
	opts := schemaflux.NewGenerateOptions()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := schemaflux.Generate[benchOrder]("a plausible order for a team plan subscription", opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCallPathClassify(b *testing.B) {
	benchSilenceLogging(b)
	benchContext(`{"category":"billing","confidence":0.95,"reasoning":"the message is about a charge"}`)
	opts := schemaflux.NewClassifyOptions().
		WithCategories([]string{"billing", "technical", "shipping", "account"}).
		WithMinConfidence(0.5)
	b.ReportAllocs()
	for b.Loop() {
		result, err := schemaflux.Classify[string, string](benchText, opts)
		if err != nil || result.Category != "billing" {
			b.Fatalf("got %+v, err %v", result, err)
		}
	}
}

func BenchmarkCallPathAuditWithModel(b *testing.B) {
	subject := benchCustomer{Email: "sam@example.com", Country: "GB", Age: 36, Password: "hunter2"}
	for _, policies := range []int{0, 3, 10} {
		b.Run(fmt.Sprintf("%d_policies", policies), func(b *testing.B) {
			benchSilenceLogging(b)
			opts := schemaflux.AuditOptions{}
			var keyed []string
			for i := 1; i <= policies; i++ {
				opts.Policies = append(opts.Policies, fmt.Sprintf("Policy %d: a rule the data must follow", i))
				keyed = append(keyed, fmt.Sprintf(`"p%d":[]`, i))
			}
			answer := `{"findings":[]}`
			if policies > 0 {
				answer = `{"policies":{` + strings.Join(keyed, ",") + `},"findings":[]}`
			}
			benchContext(answer)
			if _, err := schemaflux.AuditWithModel(subject, opts); err != nil {
				b.Fatalf("the benchmark's call fails before it is measured: %v", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := schemaflux.AuditWithModel(subject, opts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCallPathValidateHybrid(b *testing.B) {
	benchSilenceLogging(b)
	benchContext(`{"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"the record follows the rules"}`)
	// One rule Go decides (the email) and one only a model can (the sentence),
	// so the call reaches the provider.
	opts := schemaflux.NewValidateOptions().
		WithRules("the password must not be a common word").
		WithFieldRules(map[string]string{"email": "email", "age": "min:18"})
	subject := benchCustomer{Email: "sam@example.com", Country: "GB", Age: 36, Password: "hunter2"}
	if _, err := schemaflux.ValidateHybrid(subject, opts); err != nil {
		b.Fatalf("the benchmark's call fails before it is measured: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := schemaflux.ValidateHybrid(subject, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCallPathAssess13(b *testing.B) {
	benchSilenceLogging(b)
	ctx := benchContext(benchLookAnswer)
	if _, err := schemaflux.Assess[benchLook](ctx, benchMessage); err != nil {
		b.Fatalf("the benchmark's call fails before it is measured: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := schemaflux.Assess[benchLook](ctx, benchMessage); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCallPathAssess13Parallel(b *testing.B) {
	benchSilenceLogging(b)
	ctx := benchContext(benchLookAnswer)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := schemaflux.Assess[benchLook](ctx, benchMessage); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// benchTextOfSize is a text of exactly size bytes.
func benchTextOfSize(size int) string {
	const sentence = "Why is my October invoice for order A-2207 $49 and not $39? "
	return strings.Repeat(sentence, size/len(sentence)+1)[:size]
}
