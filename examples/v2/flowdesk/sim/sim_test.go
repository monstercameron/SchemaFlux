package sim

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
)

func TestMain(m *testing.M) {
	schemaflux.ConfigureLogging(schemaflux.LoggerConfig{Level: schemaflux.LogError, DisableStderr: true})
	os.Exit(m.Run())
}

// caseByID returns one labelled case.
func caseByID(t *testing.T, id string) Case {
	t.Helper()
	for _, labelled := range Cases() {
		if labelled.Message.ID == id {
			return labelled
		}
	}
	t.Fatalf("there is no labelled case %s", id)
	return Case{}
}

// askOf is the question a field of the desk's own types asks.
func askOf[T any](t *testing.T, field string) string {
	t.Helper()
	asked, found := reflect.TypeFor[T]().FieldByName(field)
	if !found {
		t.Fatalf("%s has no field %s", reflect.TypeFor[T](), field)
	}
	return asked.Tag.Get("ask")
}

// through returns a simulator and a context whose calls reach it.
func through(delay time.Duration) (*Simulator, context.Context) {
	provider := New(delay)
	client := schemaflux.NewClient("").WithProviderInstance(provider)
	return provider, client.Context(context.Background())
}

func TestOperationIsReadFromTheRequest(t *testing.T) {
	cases := []struct {
		name string
		req  schemaflux.CompletionRequest
		want string
	}{
		{"an assessment, by the name of its answer", schemaflux.CompletionRequest{SchemaName: "assessment"}, OpAssess},
		{"an audit, by the name of its answer", schemaflux.CompletionRequest{SchemaName: "audit"}, OpAudit},
		{"a redaction, by the name of its answer", schemaflux.CompletionRequest{SchemaName: "redaction_spans"}, OpRedact},
		{"an extraction, by its instructions", schemaflux.CompletionRequest{SchemaName: "Details", SystemPrompt: "You are a data extraction expert. Extract structured data."}, OpExtract},
		{"a generation, by its instructions", schemaflux.CompletionRequest{SchemaName: "Reply", SystemPrompt: "You are a data generation expert. Generate structured data."}, OpGenerate},
		{"a translation", schemaflux.CompletionRequest{SystemPrompt: "You are a translation expert. Translate text accurately."}, OpTranslate},
		{"a summary", schemaflux.CompletionRequest{SystemPrompt: "You are a text summarization expert. Create concise summaries."}, OpSummarize},
		{"a rewrite", schemaflux.CompletionRequest{SystemPrompt: "You are a text rewriting expert. Modify text."}, OpRewrite},
		{"instructions with the library's own rules in front", schemaflux.CompletionRequest{SystemPrompt: "Perform the semantic task faithfully.\n\nYou are a translation expert."}, OpTranslate},
		{"the name of the answer wins over the instructions", schemaflux.CompletionRequest{SchemaName: "audit", SystemPrompt: "You are a translation expert."}, OpAudit},
		{"an answer of the caller's own type and no known instructions", schemaflux.CompletionRequest{SchemaName: "Reply", SystemPrompt: "You are a poet."}, ""},
		{"nothing at all", schemaflux.CompletionRequest{}, ""},
		{"the instructions quoted in the message and not given", schemaflux.CompletionRequest{UserPrompt: "You are a translation expert."}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Operation(tc.req); got != tc.want {
				t.Errorf("Operation = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestQuestionsAreReadFromAnAssessment(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    []asked
		refused bool
	}{
		{"a yes or no",
			"Questions:\nq1 (yes or no): \"Is it spam?\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindYesNo, text: "Is it spam?"}}, false},
		{"a choice and its options",
			"Questions:\nq1 (one of the options below): \"Which team?\"\n  1: \"billing\"\n  2: \"technical\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindChoice, text: "Which team?", options: []string{"billing", "technical"}}}, false},
		{"a rating",
			"Questions:\nq1 (a rating from 1 to 5): \"How urgent?\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindRating, text: "How urgent?", low: 1, high: 5}}, false},
		{"a rating on a scale that starts below zero",
			"Questions:\nq1 (a rating from -2 to 2): \"How pleased?\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindRating, text: "How pleased?", low: -2, high: 2}}, false},
		{"one of each, in order",
			"Questions:\nq1 (yes or no): \"A?\"\nq2 (one of the options below): \"B?\"\n  1: \"x\"\n  2: \"y\"\nq3 (a rating from 1 to 3): \"C?\"\n\nSubject:\n{}",
			[]asked{
				{key: "q1", kind: kindYesNo, text: "A?"},
				{key: "q2", kind: kindChoice, text: "B?", options: []string{"x", "y"}},
				{key: "q3", kind: kindRating, text: "C?", low: 1, high: 3},
			}, false},
		{"a question with a quotation in it",
			"Questions:\nq1 (yes or no): \"Does it say \\\"urgent\\\"?\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindYesNo, text: `Does it say "urgent"?`}}, false},
		{"an option with a comma and a quotation in it",
			"Questions:\nq1 (one of the options below): \"Which?\"\n  1: \"a, b\"\n  2: \"the \\\"other\\\" one\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindChoice, text: "Which?", options: []string{"a, b", `the "other" one`}}}, false},
		{"guidance between the questions and the subject",
			"Questions:\nq1 (yes or no): \"A?\"\n\nGuidance: \"be strict\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindYesNo, text: "A?"}}, false},
		{"a subject that looks like a question",
			"Questions:\nq1 (yes or no): \"A?\"\n\nSubject:\n\"q2 (yes or no): \\\"B?\\\"\"",
			[]asked{{key: "q1", kind: kindYesNo, text: "A?"}}, false},
		{"instructions in front of the questions",
			"Additional instructions:\nbe brief\n\nQuestions:\nq1 (yes or no): \"A?\"\n\nSubject:\n\"hello\"",
			[]asked{{key: "q1", kind: kindYesNo, text: "A?"}}, false},
		{"no list of questions", "Subject:\n\"hello\"", nil, true},
		{"a list with nothing in it", "Questions:\n\nSubject:\n\"hello\"", nil, true},
		{"an option under no choice", "Questions:\n  1: \"billing\"\n\nSubject:\n\"hello\"", nil, true},
		{"an option under a yes or no", "Questions:\nq1 (yes or no): \"A?\"\n  1: \"billing\"\n\nSubject:\n\"hello\"", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := questions(tc.message)
			if tc.refused {
				if err == nil {
					t.Fatalf("read %+v from a message that should be refused", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("read %d questions, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].key != tc.want[i].key || got[i].kind != tc.want[i].kind || got[i].text != tc.want[i].text ||
					got[i].low != tc.want[i].low || got[i].high != tc.want[i].high ||
					strings.Join(got[i].options, "|") != strings.Join(tc.want[i].options, "|") {
					t.Errorf("question %d = %+v, want %+v", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

// Every question the desk's types ask has an answer in the table, and so do
// the three it asks in so many words.
func TestTheTableAnswersEveryQuestionTheDeskAsks(t *testing.T) {
	// Three questions of the screen, five of the triage, two of what follows the
	// reply, and the ticket match.
	want := 3 + 5 + 2 + 1
	if len(answers) != want {
		t.Errorf("the table answers %d questions, want %d", len(answers), want)
	}
	for text := range answers {
		if strings.TrimSpace(text) == "" {
			t.Error("the table has an answer to a blank question: a field of Screen or Triage has no `ask` tag")
		}
	}
}

// Each operation the desk makes, through the library and against the
// simulator: the answer is in the shape the operation reads, and it is the
// label's.
func TestTheSimulatorAnswersEachOperationThroughTheLibrary(t *testing.T) {
	plain := caseByID(t, "M-02")
	spanish := caseByID(t, "M-03")
	personal := caseByID(t, "M-04")
	follows := caseByID(t, "M-05")
	redrafted := caseByID(t, "M-08")
	policies := desk.New(nil).Policies
	options := make([]string, 0, 3)
	for _, ticket := range OpenTickets() {
		options = append(options, ticket.Option())
	}
	// The ticket match is asked of the sender and the text together.
	asking := func(c Case) map[string]string {
		return map[string]string{"from": c.Message.From, "text": c.Message.Text()}
	}

	t.Run("Is", func(t *testing.T) {
		_, ctx := through(0)
		question := askOf[desk.Triage](t, "NeedsHuman")
		yes, err := schemaflux.Is(ctx, caseByID(t, "M-07").Message.Body, question)
		if err != nil || !yes {
			t.Errorf("Is = %t, %v; want true", yes, err)
		}
		no, err := schemaflux.Is(ctx, plain.Message.Body, question)
		if err != nil || no {
			t.Errorf("Is = %t, %v; want false", no, err)
		}
	})
	t.Run("Which", func(t *testing.T) {
		_, ctx := through(0)
		team, err := schemaflux.Which(ctx, spanish.English, "Which team should handle this message?",
			[]string{"billing", "technical", "shipping", "account"})
		if err != nil || team != "shipping" {
			t.Errorf("Which = %q, %v; want shipping", team, err)
		}
	})
	t.Run("Which, with the options in another order", func(t *testing.T) {
		_, ctx := through(0)
		team, err := schemaflux.Which(ctx, spanish.English, "Which team should handle this message?",
			[]string{"shipping", "account", "billing", "technical"})
		if err != nil || team != "shipping" {
			t.Errorf("Which = %q, %v; want shipping", team, err)
		}
	})
	t.Run("WhichOrNone, with a match", func(t *testing.T) {
		_, ctx := through(0)
		option, found, err := schemaflux.WhichOrNone(ctx, asking(follows), matchQuestion, options)
		if err != nil || !found || option != options[0] {
			t.Errorf("WhichOrNone = %q, %t, %v; want the first ticket", option, found, err)
		}
	})
	t.Run("WhichOrNone, with none", func(t *testing.T) {
		_, ctx := through(0)
		option, found, err := schemaflux.WhichOrNone(ctx, asking(plain), matchQuestion, options)
		if err != nil || found || option != "" {
			t.Errorf("WhichOrNone = %q, %t, %v; want none", option, found, err)
		}
	})
	t.Run("Rate", func(t *testing.T) {
		_, ctx := through(0)
		urgency, err := schemaflux.Rate(ctx, follows.Message.Body, askOf[desk.Triage](t, "Urgency"), 1, 5)
		if err != nil || urgency != 4 {
			t.Errorf("Rate = %d, %v; want 4", urgency, err)
		}
	})
	t.Run("Assess, on a message", func(t *testing.T) {
		provider, ctx := through(0)
		screen, err := schemaflux.Assess[desk.Screen](ctx, spanish.Message)
		if err != nil || screen != spanish.Screen {
			t.Errorf("Assess = %+v, %v; want %+v", screen, err, spanish.Screen)
		}
		if provider.Count() != 1 {
			t.Errorf("%d calls for one assessment", provider.Count())
		}
	})
	t.Run("Assess, on text", func(t *testing.T) {
		_, ctx := through(0)
		triage, err := schemaflux.Assess[desk.Triage](ctx, follows.Message.Body)
		if err != nil || triage != follows.Triage {
			t.Errorf("Assess = %+v, %v; want %+v", triage, err, follows.Triage)
		}
	})
	t.Run("Audit, of a draft that breaks a policy", func(t *testing.T) {
		_, ctx := through(0)
		audit, err := schemaflux.Audit(desk.Reply{Body: redrafted.Draft}, schemaflux.AuditOptions{Policies: policies, Context: ctx})
		if err != nil {
			t.Fatal(err)
		}
		if audit.Summary.PassesAudit || len(audit.Findings) != 1 || audit.Findings[0].Policy != policies[0] {
			t.Errorf("audit passes: %t, findings: %+v; want one finding under the first policy", audit.Summary.PassesAudit, audit.Findings)
		}
		if audit.Findings[0].Recommendation == "" {
			t.Error("the finding says nothing about how to fix the draft, and the redraft is written from that")
		}
	})
	t.Run("Audit, of the redraft", func(t *testing.T) {
		_, ctx := through(0)
		audit, err := schemaflux.Audit(desk.Reply{Body: redrafted.Redraft}, schemaflux.AuditOptions{Policies: policies, Context: ctx})
		if err != nil || !audit.Summary.PassesAudit || len(audit.Findings) != 0 {
			t.Errorf("audit passes: %t, findings: %+v, err: %v; want a pass", audit.Summary.PassesAudit, audit.Findings, err)
		}
	})
	t.Run("Audit, of a draft that breaks nothing", func(t *testing.T) {
		_, ctx := through(0)
		audit, err := schemaflux.Audit(desk.Reply{Body: plain.Draft}, schemaflux.AuditOptions{Policies: policies, Context: ctx})
		if err != nil || !audit.Summary.PassesAudit {
			t.Errorf("audit passes: %t, err: %v; want a pass", audit.Summary.PassesAudit, err)
		}
	})
	t.Run("Translate, into English", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewTranslateOptions().WithTargetLanguage("English")
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		english, err := schemaflux.Translate(spanish.Message.Text(), opts)
		if err != nil || english != spanish.English {
			t.Errorf("Translate = %q, %v", english, err)
		}
	})
	t.Run("Translate, back into the customer's language", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewTranslateOptions().WithTargetLanguage("Spanish")
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		sent, err := schemaflux.Translate(spanish.Draft, opts)
		if err != nil || sent != spanish.Sent {
			t.Errorf("Translate = %q, %v", sent, err)
		}
		if !strings.Contains(sent, "[ref M-03]") {
			t.Error("the translation lost the reference")
		}
	})
	t.Run("RedactLLM", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewRedactLLMOptions()
		opts.Context = ctx
		redacted, err := schemaflux.RedactLLM(personal.Message.Body, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, sensitive := range personal.Personal {
			if strings.Contains(redacted.Text, sensitive.Text) {
				t.Errorf("the %s is still in the text", sensitive.Category)
			}
		}
		if len(redacted.Spans) != len(personal.Personal) {
			t.Errorf("%d spans, want %d", len(redacted.Spans), len(personal.Personal))
		}
		if !strings.Contains(redacted.Text, "[ref M-04]") {
			t.Error("the redaction lost the reference")
		}
	})
	t.Run("RedactLLM, with nothing to redact", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewRedactLLMOptions()
		opts.Context = ctx
		redacted, err := schemaflux.RedactLLM(plain.Message.Body, opts)
		if err != nil || redacted.Text != plain.Message.Body {
			t.Errorf("RedactLLM changed a text with no personal data in it: %v", err)
		}
	})
	t.Run("Extract", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		details, err := schemaflux.Extract[desk.Details](plain.Message.Body, opts)
		if err != nil || details != plain.Details {
			t.Errorf("Extract = %+v, %v; want %+v", details, err, plain.Details)
		}
	})
	t.Run("Generate", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewGenerateOptions().WithSeedData(map[string]string{"message": plain.Message.Body})
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		reply, err := schemaflux.Generate[desk.Reply]("Write the support desk's reply to this customer message.", opts)
		if err != nil || reply.Body != plain.Draft {
			t.Errorf("Generate = %q, %v", reply.Body, err)
		}
	})
	t.Run("Summarize", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewSummarizeOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		handoff, err := schemaflux.Summarize(plain.Message.Body, opts)
		if err != nil || handoff != plain.Handoff {
			t.Errorf("Summarize = %q, %v", handoff, err)
		}
	})
	t.Run("Rewrite", func(t *testing.T) {
		_, ctx := through(0)
		opts := schemaflux.NewRewriteOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		redraft, err := schemaflux.Rewrite(redrafted.Draft, opts)
		if err != nil || redraft != redrafted.Redraft {
			t.Errorf("Rewrite = %q, %v", redraft, err)
		}
	})
}

// A request the simulator cannot answer from the labels is an error. It does
// not answer with a default: a default is a "no", a first option or an empty
// text, and each of those reads as an answer.
func TestTheSimulatorRefusesWhatItCannotAnswer(t *testing.T) {
	assessment := func(question, subject string) schemaflux.CompletionRequest {
		return schemaflux.CompletionRequest{SchemaName: "assessment", UserPrompt: "Questions:\n" + question + "\n\nSubject:\n" + subject}
	}
	cases := []struct {
		name string
		req  schemaflux.CompletionRequest
		want string
	}{
		{"an operation the desk does not make",
			schemaflux.CompletionRequest{SchemaName: "similarity", UserPrompt: "[ref M-02]"}, "no operation the desk makes"},
		{"a request with no reference in it",
			assessment(`q1 (yes or no): "`+askOf[desk.After](t, "Again")+`"`, `"hello"`), "no message reference"},
		{"a reference to a case that is not labelled",
			assessment(`q1 (yes or no): "`+askOf[desk.After](t, "Again")+`"`, `"[ref M-99]"`), "no labelled case M-99"},
		{"a question the labels do not answer",
			assessment(`q1 (yes or no): "Is the customer left-handed?"`, `"[ref M-02]"`), "no answer to question q1"},
		{"a yes or no put as a choice",
			assessment("q1 (one of the options below): \""+askOf[desk.Triage](t, "Answerable")+"\"\n  1: \"yes\"\n  2: \"no\"", `"[ref M-02]"`), "does not answer"},
		{"a choice whose options do not include the label",
			assessment("q1 (one of the options below): \"Which team should handle this message?\"\n  1: \"sales\"\n  2: \"legal\"", `"[ref M-02]"`), "not one of its 2 options"},
		{"a rating on a scale the label is off",
			assessment(`q1 (a rating from 6 to 9): "`+askOf[desk.Triage](t, "Urgency")+`"`, `"[ref M-02]"`), "off its scale"},
		{"an answer asked for a message that does not say enough",
			schemaflux.CompletionRequest{SystemPrompt: "You are a data generation expert.", UserPrompt: "Write the reply. [ref M-06]"}, "wrong kind of reply"},
		{"a question back asked for a message that says enough",
			schemaflux.CompletionRequest{SystemPrompt: "You are a data generation expert.", UserPrompt: "ask, politely, for exactly what is missing. [ref M-02]"}, "wrong kind of reply"},
		{"a translation that names no language",
			schemaflux.CompletionRequest{SystemPrompt: "You are a translation expert.", UserPrompt: "Translate this text:\n[ref M-03]"}, "names no language"},
		{"a translation into a language the label does not name",
			schemaflux.CompletionRequest{SystemPrompt: "You are a translation expert.", UserPrompt: "Translate to French\n\nTranslate this text:\n[ref M-03]"}, "does not name"},
		{"a translation of a message that is already in English",
			schemaflux.CompletionRequest{SystemPrompt: "You are a translation expert.", UserPrompt: "Translate to English\n\nTranslate this text:\n" + caseByID(t, "M-02").Message.Text()}, "no English text"},
		{"a translation into English of something that is not the message's text",
			schemaflux.CompletionRequest{SystemPrompt: "You are a translation expert.", UserPrompt: "Translate to English\n\nTranslate this text:\n" + caseByID(t, "M-03").Message.Body}, "not of the message's text"},
		{"a translation into the customer's language of something that is not the reply",
			schemaflux.CompletionRequest{SystemPrompt: "You are a translation expert.", UserPrompt: "Translate to Spanish\n\nTranslate this text:\nsomething else [ref M-03]"}, "not of its reply"},
		{"a ticket match that does not carry the sender",
			assessment("q1 (one of the options below): \""+matchQuestion+"\"\n  1: \"none of these\"", `{"text":"[ref M-05]"}`), "does not carry the sender"},
		{"a rewrite of a draft that broke nothing",
			schemaflux.CompletionRequest{SystemPrompt: "You are a text rewriting expert.", UserPrompt: "Rewrite this text:\n[ref M-02]"}, "no redraft"},
		{"a summary of spam, which is never handed off",
			schemaflux.CompletionRequest{SystemPrompt: "You are a text summarization expert.", UserPrompt: "Summarize this text:\n[ref M-01]"}, "no hand-off note"},
		{"an audit with no data",
			schemaflux.CompletionRequest{SchemaName: "audit", UserPrompt: "Policies:\np1: \"no promises\"\n[ref M-08]"}, "no data"},
		{"an audit of a first draft without the policy it breaks",
			schemaflux.CompletionRequest{SchemaName: "audit", UserPrompt: "Policies:\np1: \"be polite\"\n\nData:\n{\"body\":\"We are sorry order D-3310 is late. It will be delivered by Thursday. Your reference is [ref M-08].\"}"}, "was not given"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := New(0)
			resp, err := provider.Complete(context.Background(), tc.req)
			if err == nil {
				t.Fatalf("answered %q", resp.Content)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one that says %q", err, tc.want)
			}
			if resp.Content != "" {
				t.Errorf("a refused request was answered as well: %q", resp.Content)
			}
			if provider.Count() != 1 {
				t.Errorf("%d calls recorded, want the one that was refused", provider.Count())
			}
		})
	}
}

func TestTheSimulatorTakesItsDelayAndStopsWithItsContext(t *testing.T) {
	request := schemaflux.CompletionRequest{SystemPrompt: "You are a text summarization expert.", UserPrompt: "Summarize this text:\n[ref M-02]"}

	// These two run on a synctest bubble's clock, so the time an answer takes
	// and the time it takes to notice a context has ended are held exactly.
	t.Run("an answer takes the delay", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const delay = 30 * time.Millisecond
			provider := New(delay)
			started := time.Now()
			if _, err := provider.Complete(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if took := time.Since(started); took != delay {
				t.Errorf("answered in %v, want its delay of %v", took, delay)
			}
			calls := provider.Calls()
			if len(calls) != 1 || calls[0].Operation != OpSummarize || calls[0].Duration() != delay {
				t.Errorf("calls recorded: %+v", calls)
			}
		})
	})
	t.Run("a context that ends first ends the call", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := New(time.Minute)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			started := time.Now()
			resp, err := provider.Complete(ctx, request)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want the context's", err)
			}
			if resp.Content != "" {
				t.Errorf("a call that was abandoned was answered: %q", resp.Content)
			}
			if took := time.Since(started); took != 20*time.Millisecond {
				t.Errorf("returned after %v, want the context's 20ms and not the delay of a minute", took)
			}
			if calls := provider.Calls(); len(calls) != 1 || calls[0].End.IsZero() || calls[0].Duration() != 20*time.Millisecond {
				t.Errorf("the abandoned call was not recorded as ended at the context's deadline: %+v", calls)
			}
		})
	})
	t.Run("a context that has already ended", func(t *testing.T) {
		provider := New(time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := provider.Complete(ctx, request); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the context's", err)
		}
	})
	t.Run("the name is one the library has a model for", func(t *testing.T) {
		provider := New(0)
		retries, _ := provider.RetryPolicy()
		if provider.Name() != "local" || retries != 0 || provider.EstimateCost(request) != 0 {
			t.Errorf("name %q, %d retries, cost %v", provider.Name(), retries, provider.EstimateCost(request))
		}
	})
}

func TestTheLogCountsWaitsFromTheOrderOfEvents(t *testing.T) {
	t.Run("nothing recorded", func(t *testing.T) {
		var log Log
		if log.Count() != 0 || log.Waits() != 0 || log.MaxInFlight() != 0 || log.Busy() != 0 || len(log.Calls()) != 0 {
			t.Errorf("an empty log reports %d calls, %d waits, %d in flight, %v busy", log.Count(), log.Waits(), log.MaxInFlight(), log.Busy())
		}
	})
	t.Run("calls one after another are a wait each", func(t *testing.T) {
		var log Log
		for i := 0; i < 5; i++ {
			log.Begin("assess")()
		}
		if log.Count() != 5 || log.Waits() != 5 || log.MaxInFlight() != 1 {
			t.Errorf("%d calls, %d waits, %d in flight; want 5, 5, 1", log.Count(), log.Waits(), log.MaxInFlight())
		}
	})
	t.Run("calls that overlap are one wait", func(t *testing.T) {
		var log Log
		first, second, third := log.Begin("assess"), log.Begin("extract"), log.Begin("assess")
		second()
		first()
		third()
		if log.Count() != 3 || log.Waits() != 1 || log.MaxInFlight() != 3 {
			t.Errorf("%d calls, %d waits, %d in flight; want 3, 1, 3", log.Count(), log.Waits(), log.MaxInFlight())
		}
	})
	t.Run("a group, then one, then a group", func(t *testing.T) {
		var log Log
		a, b := log.Begin("a"), log.Begin("b")
		a()
		b()
		log.Begin("c")()
		d, e, f, g := log.Begin("d"), log.Begin("e"), log.Begin("f"), log.Begin("g")
		d()
		e()
		f()
		g()
		if log.Count() != 7 || log.Waits() != 3 || log.MaxInFlight() != 4 {
			t.Errorf("%d calls, %d waits, %d in flight; want 7, 3, 4", log.Count(), log.Waits(), log.MaxInFlight())
		}
	})
	t.Run("a call that starts as another ends joins its wait", func(t *testing.T) {
		var log Log
		a := log.Begin("a")
		b := log.Begin("b")
		a()
		c := log.Begin("c")
		b()
		c()
		if log.Waits() != 1 || log.MaxInFlight() != 2 {
			t.Errorf("%d waits, %d in flight; want 1, 2", log.Waits(), log.MaxInFlight())
		}
	})
	t.Run("calls are listed in the order they started, with their operation", func(t *testing.T) {
		var log Log
		a, b := log.Begin("first"), log.Begin("second")
		b()
		a()
		calls := log.Calls()
		if len(calls) != 2 || calls[0].Operation != "first" || calls[1].Operation != "second" {
			t.Fatalf("calls: %+v", calls)
		}
		if calls[0].Start.After(calls[1].Start) || calls[0].End.Before(calls[1].End) {
			t.Errorf("the first call should start first and end last: %+v", calls)
		}
	})
	t.Run("a call in flight has no end and no duration", func(t *testing.T) {
		var log Log
		end := log.Begin("a")
		if calls := log.Calls(); !calls[0].End.IsZero() || calls[0].Duration() != 0 {
			t.Errorf("a call in flight is recorded as ended: %+v", calls[0])
		}
		end()
		if calls := log.Calls(); calls[0].End.IsZero() {
			t.Errorf("a call that ended has no end: %+v", calls[0])
		}
	})
	t.Run("the list returned is a copy", func(t *testing.T) {
		var log Log
		log.Begin("a")()
		log.Calls()[0].Operation = "changed"
		if log.Calls()[0].Operation != "a" {
			t.Error("changing the returned list changed the log")
		}
	})
	// The three that follow are timed on a synctest bubble's clock: a sleep is
	// exactly its length, and every goroutine that is started is waiting on its
	// sleep before the clock moves, so they are all in flight together.
	t.Run("busy time counts overlapping calls once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			a, b := log.Begin("a"), log.Begin("b")
			time.Sleep(20 * time.Millisecond)
			a()
			b()
			calls := log.Calls()
			if summed := calls[0].Duration() + calls[1].Duration(); log.Busy() != 20*time.Millisecond || summed != 40*time.Millisecond {
				t.Errorf("busy for %v; want 20ms, with the %v the two calls took between them being 40ms", log.Busy(), summed)
			}
		})
	})
	t.Run("busy time grows while a call is in flight", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			end := log.Begin("a")
			time.Sleep(5 * time.Millisecond)
			if log.Busy() != 5*time.Millisecond {
				t.Errorf("busy for %v after a call has been in flight for 5ms", log.Busy())
			}
			end()
		})
	})
	t.Run("many goroutines at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			var group sync.WaitGroup
			for i := 0; i < 50; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					end := log.Begin("a")
					time.Sleep(time.Millisecond)
					end()
				}()
			}
			group.Wait()
			if log.Count() != 50 || log.MaxInFlight() != 50 || log.Waits() != 1 {
				t.Errorf("%d calls, %d waits, %d in flight; want 50, 1, 50", log.Count(), log.Waits(), log.MaxInFlight())
			}
			if log.Busy() != time.Millisecond {
				t.Errorf("busy for %v, want 1ms", log.Busy())
			}
			for _, call := range log.Calls() {
				if call.End.IsZero() {
					t.Fatal("a call that ended has no end")
				}
			}
		})
	})
}

// What a label calls for, as an outcome.
func TestWantIsTheOutcomeTheLabelsCallFor(t *testing.T) {
	cases := []struct {
		id   string
		want desk.Outcome
	}{
		{"M-01", desk.Outcome{MessageID: "M-01", Status: desk.StatusDiscarded}},
		{"M-14", desk.Outcome{MessageID: "M-14", Status: desk.StatusDiscarded}},
	}
	for _, tc := range cases {
		t.Run(tc.id+" is discarded and nothing else", func(t *testing.T) {
			if got := caseByID(t, tc.id).Want(); got != tc.want {
				t.Errorf("Want = %+v, want %+v", got, tc.want)
			}
		})
	}

	checks := []struct {
		name string
		id   string
		ok   func(desk.Outcome, Case) bool
	}{
		{"a new message opens a ticket of its own", "M-02", func(o desk.Outcome, c Case) bool { return o.TicketID == "T-M-02" && o.New }},
		{"a follow-up goes to the open ticket", "M-05", func(o desk.Outcome, c Case) bool { return o.TicketID == "T-1001" && !o.New }},
		{"a sound draft is the reply", "M-02", func(o desk.Outcome, c Case) bool { return o.Reply == c.Draft && !o.Redrafted }},
		{"a redraft is the reply", "M-08", func(o desk.Outcome, c Case) bool { return o.Reply == c.Redraft && o.Redrafted }},
		{"a translated reply is the reply", "M-03", func(o desk.Outcome, c Case) bool { return o.Reply == c.Sent && !o.Redrafted }},
		{"a redraft that is translated is sent translated", "M-13", func(o desk.Outcome, c Case) bool { return o.Reply == c.Sent && o.Redrafted }},
		{"the language is the label's", "M-12", func(o desk.Outcome, c Case) bool { return o.Language == "French" }},
		{"a German customer is answered in German", "M-15", func(o desk.Outcome, c Case) bool {
			return o.Language == "German" && o.Reply == c.Sent && c.Sent != c.Draft
		}},
		{"a Portuguese customer is answered in Portuguese", "M-16", func(o desk.Outcome, c Case) bool {
			return o.Language == "Portuguese" && o.Reply == c.Sent && c.Sent != c.Draft
		}},
		{"a customer in a language the desk cannot name is answered in English", "M-17", func(o desk.Outcome, c Case) bool {
			return o.Language == "another language" && o.Reply == c.Draft && c.Sent == ""
		}},
		{"a message from the customer of an open ticket follows it up", "M-12", func(o desk.Outcome, c Case) bool { return o.TicketID == "T-1003" && !o.New }},
		{"a message about the same thing from another sender opens a ticket of its own", "M-15", func(o desk.Outcome, c Case) bool { return o.TicketID == "T-M-15" && o.New }},
		{"the triage is the label's", "M-07", func(o desk.Outcome, c Case) bool {
			return o.Team == "billing" && o.Intent == "requesting an action" && o.Urgency == 5 && o.Status == desk.StatusHeld
		}},
		{"a question back is the reply", "M-06", func(o desk.Outcome, c Case) bool { return o.Reply == c.Draft && o.Status == desk.StatusAsked }},
		{"the details, the hand-off and what comes next are the label's", "M-07", func(o desk.Outcome, c Case) bool {
			return o.Details == c.Details && o.Handoff == c.Handoff && o.NextAction == c.NextAction && o.WillWriteAgain == c.WritesAgain
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			labelled := caseByID(t, check.id)
			if got := labelled.Want(); !check.ok(got, labelled) {
				t.Errorf("Want = %+v", got)
			}
		})
	}
}
