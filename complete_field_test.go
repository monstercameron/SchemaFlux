package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// blogPost is the struct the CompleteField tests complete a field of.
type blogPost struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Views  int      `json:"views"`
	Tags   []string `json:"tags"`
	Secret string   `json:"-"`
	note   string
}

func samplePost() blogPost {
	return blogPost{Title: "AI in Healthcare", Body: "Artificial intelligence is transforming", Views: 12, Tags: []string{"ai", "health"}, Secret: "never sent", note: "kept"}
}

func TestCompleteFieldCompletesTheNamedFieldAndReturnsACopy(t *testing.T) {
	t.Parallel()
	model := answeringWith("how doctors read scans.")
	post := samplePost()
	got, err := schemaflux.CompleteField(operationClient(model), post, "Body")
	if err != nil {
		t.Fatal(err)
	}
	if got.Field != "Body" || got.Original != "Artificial intelligence is transforming" ||
		got.Completed != "Artificial intelligence is transforming how doctors read scans." || got.Length != 24 {
		t.Errorf("got %+v", got)
	}
	if got.Data.Body != got.Completed || got.Data.Title != post.Title || got.Data.Views != 12 || got.Data.Secret != "never sent" || got.Data.note != "kept" {
		t.Errorf("the copy lost a field: %+v", got.Data)
	}
	if post.Body != "Artificial intelligence is transforming" {
		t.Errorf("the caller's value was changed: %q", post.Body)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
}

func TestCompleteFieldShowsTheOtherFieldsAsContextUnderTheirJSONNames(t *testing.T) {
	t.Parallel()
	model := answeringWith("more")
	if _, err := schemaflux.CompleteField(operationClient(model), samplePost(), "Body"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, `"title: AI in Healthcare"`)
	wantText(t, req, `"views: 12"`)
	wantText(t, req, `"tags: ai, health"`)
	wantText(t, req, `Complete this text: "Artificial intelligence is transforming"`)
	wantNoText(t, req, `"body: `)
}

func TestCompleteFieldDoesNotSendAFieldTheTypeTagsAsNotSerialised(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-json-dash-5d2c"
	post := samplePost()
	post.Secret = canary
	model := answeringWith("more")
	if _, err := schemaflux.CompleteField(operationClient(model), post, "Body"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	for name, text := range map[string]string{"system": req.System, "user": req.User, "model": req.Model, "cache key": req.CacheKey, "schema name": req.SchemaName} {
		if strings.Contains(text, canary) {
			t.Errorf("a field tagged json:\"-\" reached the request: %s holds the canary", name)
		}
	}
	if strings.Contains(requestText(req), "Secret") {
		t.Errorf("the name of a field tagged json:\"-\" reached the request: %s", sentTo(model))
	}
}

func TestCompleteFieldWithNoOptionsAsksForTheSmartTierInCreativeMode(t *testing.T) {
	t.Parallel()
	model := answeringWith("more")
	if _, err := schemaflux.CompleteField(operationClient(model), samplePost(), "Body"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.7) > 1e-6 || req.MaxTokens != 4000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.7, 4000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	wantText(t, req, "approximately 100 characters")
}

func TestCompleteFieldEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.CompleteOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Background comes after the fields of the struct", schemaflux.CompleteOptions{Background: []string{"house style: plain"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `"house style: plain"`)
			if strings.Index(req.User, "house style") < strings.Index(req.User, "title: AI") {
				t.Errorf("the caller's background comes before the struct's own fields: %s", excerpt(req.User))
			}
		}},
		{"MaxLength", schemaflux.CompleteOptions{MaxLength: 400}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "approximately 400 characters")
		}},
		{"StopSequences", schemaflux.CompleteOptions{StopSequences: []string{"END"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `["END"]`)
		}},
		{"Steering", schemaflux.CompleteOptions{Options: schemaflux.Options{Steering: "write as a pirate"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "write as a pirate")
		}},
		{"Model", schemaflux.CompleteOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.CompleteOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"MaxOutputTokens", schemaflux.CompleteOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.CompleteOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith("more")
			if _, err := schemaflux.CompleteField(operationClient(model), samplePost(), "Body", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestCompleteFieldTakesAPointerAndReturnsAPointer(t *testing.T) {
	t.Parallel()
	post := samplePost()
	got, err := schemaflux.CompleteField(operationClient(answeringWith("more")), &post, "Body")
	if err != nil {
		t.Fatal(err)
	}
	if got.Data == &post || got.Data.Body != got.Completed || post.Body != "Artificial intelligence is transforming" {
		t.Errorf("the copy is the caller's value or was not completed: %+v", got.Data)
	}
}

func TestCompleteFieldFindsAFieldPromotedFromAnEmbeddedStruct(t *testing.T) {
	t.Parallel()
	type exportedHeader struct{ Subject string }
	type exportedMail struct {
		exportedHeader
		To string
	}
	got, err := schemaflux.CompleteField(operationClient(answeringWith("more")), exportedMail{exportedHeader{Subject: "Hello"}, "ada"}, "Subject")
	if err != nil || got.Data.Subject != got.Completed || !strings.HasPrefix(got.Completed, "Hello ") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompleteFieldRefusesWhatCannotBeCompletedBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	type withNumber struct{ Count int }
	type withPointer struct {
		*blogPost
	}
	cases := []struct {
		name string
		call func(ctx context.Context) error
		kind schemaflux.Kind
	}{
		{"a blank field name", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "")
			return err
		}, schemaflux.ErrInvalidRequest},
		{"a field name of only space", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "  ")
			return err
		}, schemaflux.ErrInvalidRequest},
		{"a field that does not exist", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "Missing")
			return err
		}, schemaflux.ErrConfiguration},
		{"a field that is not exported", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "note")
			return err
		}, schemaflux.ErrConfiguration},
		{"a field that is not a string", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "Views")
			return err
		}, schemaflux.ErrConfiguration},
		{"a field that is empty", func(ctx context.Context) error {
			post := samplePost()
			post.Body = ""
			_, err := schemaflux.CompleteField(ctx, post, "Body")
			return err
		}, schemaflux.ErrConfiguration},
		{"a field that holds only space", func(ctx context.Context) error {
			post := samplePost()
			post.Body = " \n"
			_, err := schemaflux.CompleteField(ctx, post, "Body")
			return err
		}, schemaflux.ErrConfiguration},
		{"data that is not a struct", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, "just text", "Body")
			return err
		}, schemaflux.ErrConfiguration},
		{"data that is a number", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, withNumber{Count: 1}, "Count")
			return err
		}, schemaflux.ErrConfiguration},
		{"a field promoted through a nil embedded pointer", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, withPointer{}, "Body")
			return err
		}, schemaflux.ErrConfiguration},
		{"a negative MaxLength", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "Body", schemaflux.CompleteOptions{MaxLength: -1})
			return err
		}, schemaflux.ErrConfiguration},
		{"a negative output ceiling", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "Body", schemaflux.CompleteOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
			return err
		}, schemaflux.ErrConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith("more")
			err := tc.call(operationClient(model))
			wantError(t, err, tc.kind, "completefield")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestCompleteFieldReturnsNothingBesideAnError(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.CompleteField(operationClient(answeringWith("")), samplePost(), "Body")
	wantError(t, err, schemaflux.ErrInvalidAnswer, "completefield")
	if got.Data.Title != "" || got.Field != "" || got.Original != "" || got.Completed != "" || got.Length != 0 {
		t.Errorf("a failed call left %+v beside the error", got)
	}
}

func TestCompleteFieldMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	model := answeringWith("more")
	first := schemaflux.CompleteOptions{MaxLength: 40, Background: []string{"kept note"}, Options: schemaflux.Options{Model: "first", MaxOutputTokens: 11}}
	second := schemaflux.CompleteOptions{MaxLength: 60, Options: schemaflux.Options{Model: "second"}}
	if _, err := schemaflux.CompleteField(operationClient(model), samplePost(), "Body", first, second); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "second" || req.MaxTokens != 11 {
		t.Errorf("model %q ceiling %d, want second and 11", req.Model, req.MaxTokens)
	}
	wantText(t, req, "approximately 60 characters")
	wantText(t, req, "kept note")
}

func TestCompleteFieldDoesNotChangeTheCallersBackgroundList(t *testing.T) {
	t.Parallel()
	background := make([]string, 1, 8)
	background[0] = "caller note"
	spare := background[:2]
	if _, err := schemaflux.CompleteField(operationClient(answeringWith("more")), samplePost(), "Body", schemaflux.CompleteOptions{Background: background}); err != nil {
		t.Fatal(err)
	}
	if len(background) != 1 || background[0] != "caller note" || spare[1] != "" {
		t.Errorf("the caller's list was written to: %q, spare %q", background, spare)
	}
}

func TestCompleteFieldReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]schemaflux.Kind{
		"unavailable":  schemaflux.ErrUnavailable,
		"rate limited": schemaflux.ErrRateLimited,
		"unauthorized": schemaflux.ErrUnauthorized,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind})
			_, err := schemaflux.CompleteField(operationClient(&model), samplePost(), "Body")
			wantError(t, err, kind, "completefield")
		})
	}
}

func TestCompleteFieldWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith("more")
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.CompleteField(ctx, samplePost(), "Body")
	wantError(t, err, schemaflux.ErrCanceled, "completefield")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestCompleteFieldRecordsItselfAsCompletefieldAndNotAsComplete(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	if _, err := schemaflux.CompleteField(operationClient(answeringWith("more"), recordingTo(&recorder)), samplePost(), "Body"); err != nil {
		t.Fatal(err)
	}
	if r := onlyRecord(t, &recorder); r.Op != "completefield" || r.Delivered != schemaflux.ContractPromptOnly {
		t.Errorf("record = %q delivered %v", r.Op, r.Delivered)
	}
	var refused schemaflux.Recorder
	_, err := schemaflux.CompleteField(operationClient(answeringWith("more"), recordingTo(&refused)), samplePost(), "")
	wantError(t, err, schemaflux.ErrInvalidRequest, "completefield")
	if r := onlyRecord(t, &refused); r.Op != "completefield" || r.Requests != 0 || r.Err != err {
		t.Errorf("a refusal before sending: record = %q requests %d err %v", r.Op, r.Requests, r.Err)
	}
}

func TestCompleteFieldResultCarriesTheJSONTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.CompleteField(operationClient(answeringWith("more")), samplePost(), "Title")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshaledText(got)
	for _, want := range []string{`"data":{`, `"field":"Title"`, `"original":"AI in Healthcare"`, `"completed":"AI in Healthcare more"`, `"length":5`} {
		if err != nil || !strings.Contains(encoded, want) {
			t.Errorf("%s is missing from %s (%v)", want, encoded, err)
		}
	}
}

func TestCompleteFieldKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-f07a19"
	type canaryPost struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Hidden string `json:"-"`
	}
	opts := schemaflux.CompleteOptions{
		Options:       schemaflux.Options{Steering: "steer " + canary},
		Background:    []string{"note " + canary},
		StopSequences: []string{"stop " + canary},
	}
	post := canaryPost{Title: "title " + canary, Body: "body " + canary, Hidden: "hidden " + canary}
	call := func(o schemaflux.CompleteOptions, field string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, post, field, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("and then " + canary) }, true, call(opts, "Body"))
	})
	t.Run("an answer that adds nothing", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("body " + canary) }, true, call(opts, "Body"))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts, "Body"))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MaxOutputTokens = -1
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("x") }, true, call(bad, "Body"))
	})
	t.Run("a canary in a field tagged as not serialised does not reach the request", func(t *testing.T) {
		t.Parallel()
		model := answeringWith("more")
		if _, err := schemaflux.CompleteField(operationClient(model), post, "Body"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(requestText(model.Last()), "hidden "+canary) {
			t.Errorf("the value of a json:\"-\" field was sent: %s", sentTo(model))
		}
	})
}
