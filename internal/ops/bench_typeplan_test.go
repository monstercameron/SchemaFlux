package ops

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// What the plan of a type saves, and what is left.
//
// BenchmarkTypePlanBuild is the part of an Extract that depends on the type
// alone, as core.go does it. It is the counterpart of BenchmarkExtractBuildAll,
// which runs the exported generators one after the other the way core.go did
// before there was a plan. The same sequence read from the plan allocates
// nothing.
//
// BenchmarkExtractThroughAStub is a whole Extract with the model caller
// replaced by one that answers at once, so that the figure is what the library
// does around the call: the options, the type's plan, the prompts, the decode of
// the answer and its checks. "uncached" gives every call a plan of its own, which
// is the work Extract did before the plan was kept; it does not reproduce the
// older code exactly, since the identity now reuses the schema it has just
// written and used to write it a second time.
//
//	BenchmarkTypePlanBuild/<type>
//	BenchmarkExtractThroughAStub/<type>/<mode>/<cached|uncached>

func BenchmarkTypePlanBuild(b *testing.B) {
	for _, target := range benchBuildTypes {
		b.Run(target.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				typePlanBuildAsExtractDoes(target.typ)
			}
		})
		b.Run(target.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					typePlanBuildAsExtractDoes(target.typ)
				}
			})
		})
	}
}

func BenchmarkExtractThroughAStub(b *testing.B) {
	typePlanQuietLogs(b)
	for _, target := range []struct {
		name string
		typ  reflect.Type
	}{
		{"flat3", reflect.TypeFor[benchFlat]()},
		{"nested12", reflect.TypeFor[benchNested]()},
	} {
		for _, mode := range []types.Mode{types.TransformMode, types.Strict} {
			for _, cached := range []bool{true, false} {
				label := "cached"
				if !cached {
					label = "uncached"
				}
				b.Run(target.name+"/"+mode.String()+"/"+label, func(b *testing.B) {
					previousPlans := typePlans
					previousCaller, previousProvider := currentHooks()
					b.Cleanup(func() {
						typePlans = previousPlans
						setLLMCaller(previousCaller)
						SetDefaultProvider(previousProvider)
					})
					if !cached {
						typePlans = newTypePlanCache(0)
					}
					answer := benchAnswerFor(b, target.typ)
					setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
						return answer, nil
					})
					run := typePlanExtractOf(target.typ, mode)
					if err := run(); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						if err := run(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
