package parity

import (
	"context"
	"iter"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/telemetry"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
)

// StreamSummarize, StreamRewrite, StreamTranslate and StreamExpand, each against
// the old root's function of the same request. The cases are written as the
// harness's are (a parityCase is one pair), but the recording providers stream:
// each one answers with the script's next reply in one piece and then the last
// chunk, and records the request it was sent as the providers of the harness do.
// A stream's request is sent when the stream is read, so each case reads its
// stream to the end.

// streamingOldModel is the harness's provider of the old root's contract with a
// CompleteStream: the request is recorded through Complete, which keeps the
// record and picks the reply.
type streamingOldModel struct{ oldModel }

func (m streamingOldModel) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		resp, err := m.oldModel.Complete(ctx, req)
		if err != nil {
			yield(llm.StreamChunk{}, err)
			return
		}
		if !yield(llm.StreamChunk{Delta: resp.Content}, nil) {
			return
		}
		yield(llm.StreamChunk{Done: true, Response: resp}, nil)
	}
}

// streamingNewModel is the harness's provider of version 2's contract with a
// Stream.
type streamingNewModel struct{ newModel }

func (m streamingNewModel) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		resp, err := m.newModel.Complete(ctx, req)
		if err != nil {
			yield(schemaflux.Chunk{}, err)
			return
		}
		if !yield(schemaflux.Chunk{Delta: resp.Content}, nil) {
			return
		}
		yield(schemaflux.Chunk{Done: true, Response: resp}, nil)
	}
}

// oldStreamRequests runs the old root's stream on a recording provider and
// returns what it sent.
func oldStreamRequests(t *testing.T, c parityCase) []sentRequest {
	t.Helper()
	model := streamingOldModel{oldModel{&script{replies: c.replies}}}
	ctx := ops.WithSharedExecConfig(context.Background(), &ops.ExecConfig{Provider: model, Retries: -1})
	if err := c.old(ctx); err != nil {
		t.Fatalf("the old root's stream failed: %v", err)
	}
	return model.requests()
}

// v2StreamRequests runs version 2's stream on a recording provider in the state
// given and returns what it sent and the Records its recorder was given. The
// floor state is a policy that allows the provider and holds the client to the
// lowest level, which is the one a stream is delivered at.
func v2StreamRequests(t *testing.T, c parityCase, s state) ([]sentRequest, []schemaflux.Record) {
	t.Helper()
	model := streamingNewModel{newModel{&script{replies: c.replies}}}
	client := schemaflux.Client{Provider: model}
	recorder := &schemaflux.Recorder{}
	switch s {
	case stateRecorder:
		client.Recorder = recorder
	case stateFloor:
		floorRoute.Do(func() {
			for _, model := range []string{modelName, pinnedModel} {
				provider.DeclareRoute(providerName, model, []string{"eu"}, 0)
			}
		})
		client.Policy = &schemaflux.DataPolicy{Providers: []string{providerName}, Regions: []string{"eu"}, MinContract: schemaflux.ContractPromptOnly}
	}
	ctx := schemaflux.WithClient(context.Background(), client)
	if err := c.v2(ctx); err != nil {
		t.Fatalf("the v2 stream failed in the state %q: %v", s, err)
	}
	return model.requests(), recorder.Records()
}

// runStreamParity is runParity for streams: the same four states, and in the
// recorder state the Record is there when the stream has been read to its end.
func runStreamParity(t *testing.T, cases []parityCase) {
	t.Helper()
	t.Run("plain, a recorder and a floor", func(t *testing.T) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				reference := oldStreamRequests(t, c)
				if len(reference) == 0 {
					t.Fatal("the old root sent nothing: the case proves nothing")
				}
				for _, s := range []state{statePlain, stateRecorder, stateFloor} {
					got, records := v2StreamRequests(t, c, s)
					requireSameRequests(t, s.String(), reference, got)
					if s == stateRecorder {
						if len(records) != 1 || records[0].Op != c.op || records[0].Err != nil || records[0].Requests != len(got) {
							t.Errorf("a recorder was given %d Records, want one for %q with %d requests and no error: %+v", len(records), c.op, len(got), records)
						}
					}
				}
			})
		}
	})
	t.Run("an observer", func(t *testing.T) {
		counter := &operationCounter{}
		t.Cleanup(telemetry.AddObserver(counter))
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				reference := oldStreamRequests(t, c)
				counter.mu.Lock()
				counter.names = nil
				counter.mu.Unlock()
				got, _ := v2StreamRequests(t, c, stateObserver)
				requireSameRequests(t, stateObserver.String(), reference, got)
				counter.mu.Lock()
				defer counter.mu.Unlock()
				if len(counter.names) != 1 || counter.names[0] != c.op {
					t.Errorf("the observer saw %d operations finish (%v), want exactly one, %s", len(counter.names), counter.names, c.op)
				}
			})
		}
	})
}

// ---- StreamSummarize ----

func streamSummarizeCases() []parityCase {
	var cases []parityCase
	for _, s := range summarizeSettings() {
		cases = append(cases, parityCase{
			name: "StreamSummarize/" + s.name, op: "streamsummarize", replies: []string{summarizeAnswer},
			old: func(ctx context.Context) error {
				o := oldroot.NewSummarizeOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				stream, err := oldroot.StreamSummarize(summarizeInput, o)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
			v2: func(ctx context.Context) error {
				stream, err := schemaflux.StreamSummarize(ctx, summarizeInput, s.v2)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
		})
	}
	return cases
}

func TestStreamSummarizeSendsTheRequestTheOldRootSends(t *testing.T) {
	runStreamParity(t, streamSummarizeCases())
}

// ---- StreamRewrite ----

const (
	streamRewriteInput  = "hey, the figures are attached, tell me what you think"
	streamRewriteAnswer = "Dear team, the figures are attached."
)

type rewriteSetting struct {
	name string
	v2   schemaflux.RewriteOptions
	old  func(o *oldroot.RewriteOptions)
}

func rewriteSettings() []rewriteSetting {
	settings := []rewriteSetting{
		{"the default", schemaflux.RewriteOptions{}, func(*oldroot.RewriteOptions) {}},
		{"Tone", schemaflux.RewriteOptions{Tone: "formal"}, func(o *oldroot.RewriteOptions) { o.TargetTone = "formal" }},
		{"Formality 8", schemaflux.RewriteOptions{Formality: 8}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 8 }},
		{"Formality 1", schemaflux.RewriteOptions{Formality: 1}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 1 }},
		{"Formality 5 is the default", schemaflux.RewriteOptions{Formality: 5}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 5 }},
		{"Formality 10", schemaflux.RewriteOptions{Formality: 10}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 10 }},
		{"NoPreserveFacts", schemaflux.RewriteOptions{NoPreserveFacts: true}, func(o *oldroot.RewriteOptions) { o.PreserveFacts = false }},
		{"Audience", schemaflux.RewriteOptions{Audience: "engineers"}, func(o *oldroot.RewriteOptions) { o.Audience = "engineers" }},
		{"StyleGuide", schemaflux.RewriteOptions{StyleGuide: "AP"}, func(o *oldroot.RewriteOptions) { o.StyleGuide = "AP" }},
		{"Changes", schemaflux.RewriteOptions{Changes: []string{"shorten", "simplify"}}, func(o *oldroot.RewriteOptions) { o.Changes = []string{"shorten", "simplify"} }},
		{"AvoidWords", schemaflux.RewriteOptions{AvoidWords: []string{"synergy", "leverage"}}, func(o *oldroot.RewriteOptions) { o.AvoidWords = []string{"synergy", "leverage"} }},
		{"IncludeWords", schemaflux.RewriteOptions{IncludeWords: []string{"figures"}}, func(o *oldroot.RewriteOptions) { o.IncludeWords = []string{"figures"} }},
		{"every own setting",
			schemaflux.RewriteOptions{Tone: "friendly", Formality: 7, NoPreserveFacts: true, Audience: "customers", StyleGuide: "plain", Changes: []string{"shorten"}, AvoidWords: []string{"synergy"}, IncludeWords: []string{"figures"}},
			func(o *oldroot.RewriteOptions) {
				o.TargetTone, o.FormalityLevel, o.PreserveFacts, o.Audience, o.StyleGuide = "friendly", 7, false, "customers", "plain"
				o.Changes, o.AvoidWords, o.IncludeWords = []string{"shorten"}, []string{"synergy"}, []string{"figures"}
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, rewriteSetting{c.name, schemaflux.RewriteOptions{Options: c.v2}, func(o *oldroot.RewriteOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func streamRewriteCases() []parityCase {
	var cases []parityCase
	for _, s := range rewriteSettings() {
		cases = append(cases, parityCase{
			name: "StreamRewrite/" + s.name, op: "streamrewrite", replies: []string{streamRewriteAnswer},
			old: func(ctx context.Context) error {
				o := oldroot.NewRewriteOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				stream, err := oldroot.StreamRewrite(streamRewriteInput, o)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
			v2: func(ctx context.Context) error {
				stream, err := schemaflux.StreamRewrite(ctx, streamRewriteInput, s.v2)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
		})
	}
	return cases
}

func TestStreamRewriteSendsTheRequestTheOldRootSends(t *testing.T) {
	runStreamParity(t, streamRewriteCases())
}

// ---- StreamTranslate ----

const (
	streamTranslateInput  = "The invoice is due in thirty days."
	streamTranslateAnswer = "La factura vence en treinta dias."
)

type translateSetting struct {
	name string
	v2   schemaflux.TranslateOptions
	old  func(o *oldroot.TranslateOptions)
}

func translateSettings() []translateSetting {
	glossary := map[string]string{"invoice": "factura", "due": "vence"}
	settings := []translateSetting{
		{"the default", schemaflux.TranslateOptions{}, func(*oldroot.TranslateOptions) {}},
		{"SourceLanguage", schemaflux.TranslateOptions{SourceLanguage: "English"}, func(o *oldroot.TranslateOptions) { o.SourceLanguage = "English" }},
		{"NoPreserveFormatting", schemaflux.TranslateOptions{NoPreserveFormatting: true}, func(o *oldroot.TranslateOptions) { o.PreserveFormatting = false }},
		{"CulturalAdaptation 8", schemaflux.TranslateOptions{CulturalAdaptation: 8}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 8 }},
		{"CulturalAdaptation 5 is the default", schemaflux.TranslateOptions{CulturalAdaptation: 5}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 5 }},
		{"CulturalAdaptation 10", schemaflux.TranslateOptions{CulturalAdaptation: 10}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 10 }},
		{"CulturalAdaptation 1", schemaflux.TranslateOptions{CulturalAdaptation: 1}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 1 }},
		// A negative value means a literal translation, which the old root says as 0.
		{"CulturalAdaptation negative is zero", schemaflux.TranslateOptions{CulturalAdaptation: -1}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 0 }},
		{"Formality", schemaflux.TranslateOptions{Formality: "formal"}, func(o *oldroot.TranslateOptions) { o.Formality = "formal" }},
		{"Formality neutral is the default", schemaflux.TranslateOptions{Formality: "neutral"}, func(o *oldroot.TranslateOptions) { o.Formality = "neutral" }},
		{"Glossary", schemaflux.TranslateOptions{Glossary: glossary}, func(o *oldroot.TranslateOptions) { o.Glossary = glossary }},
		{"Dialect", schemaflux.TranslateOptions{Dialect: "Mexican"}, func(o *oldroot.TranslateOptions) { o.Dialect = "Mexican" }},
		{"every own setting",
			schemaflux.TranslateOptions{SourceLanguage: "English", NoPreserveFormatting: true, CulturalAdaptation: 7, Formality: "informal", Glossary: glossary, Dialect: "Mexican"},
			func(o *oldroot.TranslateOptions) {
				o.SourceLanguage, o.PreserveFormatting, o.CulturalAdaptation, o.Formality, o.Glossary, o.Dialect = "English", false, 7, "informal", glossary, "Mexican"
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, translateSetting{c.name, schemaflux.TranslateOptions{Options: c.v2}, func(o *oldroot.TranslateOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func streamTranslateCases() []parityCase {
	var cases []parityCase
	for _, s := range translateSettings() {
		cases = append(cases, parityCase{
			name: "StreamTranslate/" + s.name, op: "streamtranslate", replies: []string{streamTranslateAnswer},
			old: func(ctx context.Context) error {
				o := oldroot.NewTranslateOptions()
				o.TargetLanguage = "Spanish"
				s.old(&o)
				o.CommonOptions.Context = ctx
				stream, err := oldroot.StreamTranslate(streamTranslateInput, o)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
			v2: func(ctx context.Context) error {
				stream, err := schemaflux.StreamTranslate(ctx, streamTranslateInput, "Spanish", s.v2)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
		})
	}
	return cases
}

func TestStreamTranslateSendsTheRequestTheOldRootSends(t *testing.T) {
	runStreamParity(t, streamTranslateCases())
}

// ---- StreamExpand ----

const (
	streamExpandInput  = "Revenue grew twelve percent."
	streamExpandAnswer = "Revenue grew twelve percent over the year, while costs stayed flat."
)

type expandSetting struct {
	name string
	v2   schemaflux.ExpandOptions
	old  func(o *oldroot.ExpandOptions)
}

func expandSettings() []expandSetting {
	settings := []expandSetting{
		{"the default", schemaflux.ExpandOptions{}, func(*oldroot.ExpandOptions) {}},
		{"Factor 3", schemaflux.ExpandOptions{Factor: 3}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 3 }},
		{"Factor 2 is the default", schemaflux.ExpandOptions{Factor: 2}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 2 }},
		{"Factor 1", schemaflux.ExpandOptions{Factor: 1}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 1 }},
		{"Factor 2.5", schemaflux.ExpandOptions{Factor: 2.5}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 2.5 }},
		{"DetailLevel 8", schemaflux.ExpandOptions{DetailLevel: 8}, func(o *oldroot.ExpandOptions) { o.DetailLevel = 8 }},
		{"DetailLevel 1", schemaflux.ExpandOptions{DetailLevel: 1}, func(o *oldroot.ExpandOptions) { o.DetailLevel = 1 }},
		{"DetailLevel 5 is the default", schemaflux.ExpandOptions{DetailLevel: 5}, func(o *oldroot.ExpandOptions) { o.DetailLevel = 5 }},
		{"IncludeExamples", schemaflux.ExpandOptions{IncludeExamples: true}, func(o *oldroot.ExpandOptions) { o.IncludeExamples = true }},
		{"ElaborateOn", schemaflux.ExpandOptions{ElaborateOn: []string{"costs", "revenue"}}, func(o *oldroot.ExpandOptions) { o.ElaborateOn = []string{"costs", "revenue"} }},
		{"AddContext", schemaflux.ExpandOptions{AddContext: []string{"history"}}, func(o *oldroot.ExpandOptions) { o.AddContext = []string{"history"} }},
		{"Style", schemaflux.ExpandOptions{Style: "technical"}, func(o *oldroot.ExpandOptions) { o.ExpansionStyle = "technical" }},
		{"every own setting",
			schemaflux.ExpandOptions{Factor: 3, DetailLevel: 8, IncludeExamples: true, ElaborateOn: []string{"costs"}, AddContext: []string{"history"}, Style: "narrative"},
			func(o *oldroot.ExpandOptions) {
				o.ExpansionFactor, o.DetailLevel, o.IncludeExamples, o.ElaborateOn, o.AddContext, o.ExpansionStyle = 3, 8, true, []string{"costs"}, []string{"history"}, "narrative"
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, expandSetting{c.name, schemaflux.ExpandOptions{Options: c.v2}, func(o *oldroot.ExpandOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func streamExpandCases() []parityCase {
	var cases []parityCase
	for _, s := range expandSettings() {
		cases = append(cases, parityCase{
			name: "StreamExpand/" + s.name, op: "streamexpand", replies: []string{streamExpandAnswer},
			old: func(ctx context.Context) error {
				o := oldroot.NewExpandOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				stream, err := oldroot.StreamExpand(streamExpandInput, o)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
			v2: func(ctx context.Context) error {
				stream, err := schemaflux.StreamExpand(ctx, streamExpandInput, s.v2)
				if err != nil {
					return err
				}
				_, err = stream.Text()
				return err
			},
		})
	}
	return cases
}

func TestStreamExpandSendsTheRequestTheOldRootSends(t *testing.T) {
	runStreamParity(t, streamExpandCases())
}
