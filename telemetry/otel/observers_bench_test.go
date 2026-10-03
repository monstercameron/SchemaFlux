package otel

import (
	"context"
	"fmt"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/monstercameron/schemaflux/telemetry"
)

// What a unit costs when many observers are installed. Each observer after the
// first steps back over the spans of the observers before it to find a parent on
// its own provider (parentContext), so the work of one unit grows with the square
// of the number of observers; the benchmark shows whether that is a cost at the
// numbers a program has. A program installs one, a test harness or a migration
// two, and 100 is a stress.
//
//	go test ./telemetry/otel -run '^$' -bench ObserversInstalled
func BenchmarkUnitWithObserversInstalled(b *testing.B) {
	for _, count := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("%d observers", count), func(b *testing.B) {
			for range count {
				provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
				observer, err := New(provider)
				if err != nil {
					b.Fatal(err)
				}
				defer telemetry.AddObserver(observer)()
			}
			ctx := context.Background()
			event := telemetry.OperationEvent{Scope: telemetry.ScopeOperation, Operation: "extract"}
			request := telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "gpt-5"}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				operation := telemetry.ObserveOperationStart(ctx, event)
				first := telemetry.ObserveOperationStart(operation, request)
				telemetry.ObserveOperationEnd(first, telemetry.OperationResult{Attempts: 1})
				telemetry.ObserveOperationEnd(operation, telemetry.OperationResult{Scope: telemetry.ScopeOperation})
			}
		})
	}
}
