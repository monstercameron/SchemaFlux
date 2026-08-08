package schemafluxtest_test

// Tests filling coverage gaps left by provider_test.go and cassette_test.go:
// the branches those files exercise indirectly through Extract but never hit
// directly (As, Shaped, ReplyFunc, Contexts/ContextN, Install's refusal and
// no-previous-client paths), plus Save's other refusal path — a credential
// that reaches a field redaction never touches.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// A provider name the library does not know resolves to no model at all, and
// every call errors -- this is FL-001, and As is how a test opts into it.
func TestAsWithAnUnknownNameResolvesToNoModel(t *testing.T) {
	provider := schemafluxtest.New().As("a-provider-that-does-not-exist").
		Reply(`{"queue":"billing","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	_, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("an unknown provider name must not resolve to a usable model")
	}
}

// Shaped answers from the request's own declared shape rather than the fixed
// "{}" default, which is what lets a test that only cares about the request
// skip scripting a body per operation.
func TestShapedAnswersFromTheRequestsOwnSchema(t *testing.T) {
	provider := schemafluxtest.New().Shaped()
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("a shaped reply should satisfy the type's own schema: %v", err)
	}
	_ = result

	if len(provider.Requests()) != 1 {
		t.Fatalf("CallCount = %d, want 1", len(provider.Requests()))
	}
}

// A scripted Reply always wins over Shaped -- it is a fallback, not a mode.
func TestReplyStillWinsOverShaped(t *testing.T) {
	provider := schemafluxtest.New().Shaped().Reply(`{"queue":"billing","priority":"high"}`)
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "billing" {
		t.Errorf("Shaped overrode the scripted reply: %+v", result)
	}
}

// Fail and FailThen with a nil error still fail -- the default message is
// there so a test that does not care what the error says still gets one.
func TestFailAndFailThenDefaultTheirError(t *testing.T) {
	t.Run("Fail", func(t *testing.T) {
		provider := schemafluxtest.New().Fail(nil)
		defer schemafluxtest.Install(t, provider)()

		_, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions())
		if err == nil {
			t.Fatal("Fail(nil) must still fail every call")
		}
	})

	t.Run("FailThen", func(t *testing.T) {
		provider := schemafluxtest.New().FailThen(1, nil, `{"queue":"billing","priority":"low"}`)
		defer schemafluxtest.Install(t, provider)()

		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
			t.Fatal("the first call should have failed")
		}
		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil {
			t.Fatalf("the second call should have recovered: %v", err)
		}
	})
}

// Reply with no arguments resets the double to its constructor default,
// rather than leaving whatever was scripted before in place.
func TestReplyWithNoArgumentsResetsToTheDefault(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"billing","priority":"low"}`)
	provider.Reply()
	defer schemafluxtest.Install(t, provider)()

	// "{}" cannot satisfy triage's required fields, so the call must fail --
	// proving the old scripted body is gone, not just unread.
	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
		t.Fatal("Reply() with no arguments should have reverted to the unusable default")
	}
}

// LastRequest on a provider that has never been called returns the zero
// value rather than panicking, so a test asserting "nothing was sent yet"
// has something to assert against.
func TestLastRequestBeforeAnyCallIsTheZeroValue(t *testing.T) {
	provider := schemafluxtest.New()
	if got := provider.LastRequest(); got.Model != "" || got.UserPrompt != "" {
		t.Errorf("LastRequest before any call = %+v, want the zero value", got)
	}
}

// ReplyFunc answers from the request itself, which is what a caller that
// batches work into multiple calls needs: each call gets an answer shaped
// for what THAT call asked, not a fixed script that cannot know.
func TestReplyFuncAnswersFromTheRequest(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
		if call == 0 {
			return `{"queue":"first","priority":"low"}`, nil
		}
		return "", errors.New("no more scripted calls")
	})
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage]("one", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "first" {
		t.Errorf("result = %+v", result)
	}
}

// ReplyFunc's error return fails that call, same as a scripted error would.
func TestReplyFuncCanFailACall(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
		return "", errors.New("this call is scripted to fail")
	})
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
		t.Fatal("ReplyFunc returning an error must fail the call")
	}
}

// FailThen's failures still apply even when ReplyFunc is scripted; scripted
// errors are checked before the function, so "fail twice, then answer from
// the request" composes.
func TestFailThenAppliesBeforeReplyFunc(t *testing.T) {
	provider := schemafluxtest.New().
		FailThen(1, errors.New("rate limited"), "unused").
		ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
			return `{"queue":"from-func","priority":"low"}`, nil
		})
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
		t.Fatal("the scripted failure should have applied first")
	}
	result, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "from-func" {
		t.Errorf("ReplyFunc did not answer the second call: %+v", result)
	}
}

// Contexts records the context each call arrived on, in order -- the only
// place a per-call deadline set by the caller's own code is observable from
// outside that code.
func TestContextsRecordsEachCallsContext(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"a","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	type keyT struct{}
	ctx := context.WithValue(context.Background(), keyT{}, "marker")
	opts := schemaflux.NewExtractOptions()
	opts.OpOptions.Context = ctx

	if _, err := schemaflux.Extract[triage]("x", opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	contexts := provider.Contexts()
	if len(contexts) != 1 {
		t.Fatalf("Contexts() len = %d, want 1", len(contexts))
	}
	if got := contexts[0].Value(keyT{}); got != "marker" {
		t.Errorf("the recorded context lost the caller's value: %v", got)
	}

	if got := provider.ContextN(0).Value(keyT{}); got != "marker" {
		t.Errorf("ContextN(0) lost the caller's value: %v", got)
	}
}

// A ContextN call for an index that never happened returns Background
// rather than nil, so an assertion on it reports a missing deadline instead
// of panicking somewhere unrelated.
func TestContextNOutOfRangeReturnsBackground(t *testing.T) {
	provider := schemafluxtest.New()
	if got := provider.ContextN(0); got != context.Background() {
		t.Errorf("ContextN(0) on an uncalled provider = %v, want context.Background()", got)
	}
	if got := provider.ContextN(-1); got != context.Background() {
		t.Errorf("ContextN(-1) = %v, want context.Background()", got)
	}
}

// EstimateCost implements the Provider interface and always reports zero: a
// double costs nothing, no matter what it is asked to price.
func TestEstimateCostIsAlwaysZero(t *testing.T) {
	provider := schemafluxtest.New()
	if cost := provider.EstimateCost(schemaflux.CompletionRequest{Model: "gpt-5.6-sol"}); cost != 0 {
		t.Errorf("EstimateCost() = %v, want 0", cost)
	}
}

// A context already cancelled before Complete is even called is reported
// immediately, with no delay configured -- the check does not depend on
// Slow being set.
func TestCompleteReportsAnAlreadyCancelledContext(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Complete(ctx, schemaflux.CompletionRequest{Model: "gpt-5.6-sol"})
	if err == nil {
		t.Fatal("Complete against an already-cancelled context must fail")
	}
}

// Once Shaped clears the scripted replies, a Fail with no reply list left
// still fails every call past the scripted failure count, not just the
// first -- the double does not quietly start succeeding once the script
// runs out.
func TestFailKeepsFailingPastTheScriptedCount(t *testing.T) {
	provider := schemafluxtest.New().Shaped().Fail(errors.New("still down"))
	defer schemafluxtest.Install(t, provider)()

	for i := 0; i < 3; i++ {
		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
			t.Fatalf("call %d should have failed", i+1)
		}
	}
}

// --- install.go ---

// fatalRecorder is the minimal TestingT double schemafluxtest.Install needs.
// It never actually stops the goroutine the way *testing.T's Fatalf does, so
// a test can observe that Fatalf was called instead of the process dying.
type fatalRecorder struct {
	setenv []struct{ key, value string }
	fatal  string
}

func (f *fatalRecorder) Helper()        {}
func (f *fatalRecorder) Cleanup(func()) {}
func (f *fatalRecorder) Setenv(k, v string) {
	f.setenv = append(f.setenv, struct{ key, value string }{k, v})
}
func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.fatal = strings.TrimSpace(fmt.Sprintf(format, args...))
}

// Install refuses a nil provider rather than installing an unusable one and
// leaving every subsequent call in the test to fail somewhere less obvious.
func TestInstallRefusesANilProvider(t *testing.T) {
	recorder := &fatalRecorder{}
	restore := schemafluxtest.Install(recorder, nil)
	defer restore()

	if recorder.fatal == "" {
		t.Fatal("Install(t, nil) did not call Fatalf")
	}
	if !strings.Contains(recorder.fatal, "New()") {
		t.Errorf("the refusal does not point at the fix: %q", recorder.fatal)
	}
}

// When there was no previous client, restoring sets the default back to nil
// rather than to whatever Install last saw -- a test running first in a
// process with no client configured must not leave one installed after it
// ends.
func TestInstallWithNoPreviousClientRestoresToNil(t *testing.T) {
	before := schemaflux.GetDefaultClient()
	schemaflux.SetDefaultClient(nil)
	defer schemaflux.SetDefaultClient(before)

	func() {
		provider := schemafluxtest.New()
		restore := schemafluxtest.Install(t, provider)
		defer restore()

		if schemaflux.GetDefaultClient() == nil {
			t.Fatal("Install did not set a client")
		}
	}()

	if schemaflux.GetDefaultClient() != nil {
		t.Error("restoring after a nil previous client should leave it nil")
	}
}

// --- cassette.go: Save's other refusal path ---

// Redaction runs on the prompt and response fields; Provider and Model are
// not prompt content and are not redacted. If a credential ever ends up
// there -- a provider misreporting its own model name, say -- Save must
// still refuse rather than write it, because the redaction pass never saw
// it in the first place.
func TestSaveRefusesACredentialInAnUnredactedField(t *testing.T) {
	directory := t.TempDir()

	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			response: schemaflux.CompletionResponse{Content: "ok"},
		}},
	}
	recorder := schemafluxtest.Record(inner, directory)

	req := sampleRequest()
	req.Model = "sk-ant-api03-LEAKEDLEAKEDLEAKEDLEAKEDLEAK" // secret-scan: allow
	if _, err := recorder.Complete(context.Background(), req); err != nil {
		t.Fatalf("recording: %v", err)
	}

	err := recorder.Save()
	if err == nil {
		t.Fatal("Save wrote a cassette with a credential-shaped model field")
	}
	if !strings.Contains(err.Error(), "refusing to write") {
		t.Errorf("the error does not explain the refusal: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(directory, "000.json")); statErr == nil {
		t.Error("Save left a file on disk despite refusing to write it")
	}
}

// A recorded failure's message is redacted the same way a response's
// content is -- the classification survives, the credential does not.
func TestSaveRedactsARecordedFailuresMessage(t *testing.T) {
	directory := t.TempDir()

	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			err: &types.OperationError{
				Kind:    types.KindRateLimited,
				Message: "the client sent sk-proj-FAILUREPATHLEAKFAILUREPATHLEAK, 429", // secret-scan: allow
			},
		}},
	}
	recorder := schemafluxtest.Record(inner, directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err == nil {
		t.Fatal("expected the scripted failure")
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
	if err != nil {
		t.Fatalf("reading the written cassette: %v", err)
	}
	if strings.Contains(string(raw), "sk-proj-FAILUREPATHLEAK") {
		t.Error("a credential in a failure message reached disk")
	}
	if !strings.Contains(string(raw), "[redacted by schemafluxtest]") {
		t.Error("the failure message was not redacted at all")
	}
	if !strings.Contains(string(raw), "429") {
		t.Error("redaction removed more of the failure message than the credential")
	}
}

// Save refuses to create the cassette directory when a plain file already
// occupies that path, rather than silently writing nothing.
func TestSaveFailsWhenTheDirectoryPathIsAFile(t *testing.T) {
	parent := t.TempDir()
	blocked := filepath.Join(parent, "cassettes")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), blocked)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if err := recorder.Save(); err == nil {
		t.Fatal("Save succeeded despite the target path being a file, not a directory")
	}
}

// Save fails cleanly when the target file's own path is blocked by an
// existing directory of the same name, rather than corrupting whatever is
// there.
func TestSaveFailsWhenTheTargetFileIsBlocked(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "000.json"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if err := recorder.Save(); err == nil {
		t.Fatal("Save succeeded despite 000.json being a directory")
	}
}

// --- cassette.go: Replay's own error paths ---

// A cassette file that is not valid JSON fails Replay with a message naming
// the file, rather than a bare json.Unmarshal error a caller cannot locate
// inside a directory of dozens.
func TestReplayFailsOnAMalformedCassetteFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "000.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := schemafluxtest.Replay(directory)
	if err == nil {
		t.Fatal("Replay accepted a malformed cassette file")
	}
	if !strings.Contains(err.Error(), "000.json") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// Player.Name reports "local", the name reserved for the in-process double;
// any other name resolves to no model, which is FL-001 working as intended.
func TestPlayerNameIsLocal(t *testing.T) {
	player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{
		Provider: "openai",
		Response: &schemafluxtest.CassetteResponse{Content: "{}"},
	})
	if got := player.Name(); got != "local" {
		t.Errorf("Player.Name() = %q, want \"local\"", got)
	}
}

// A classification stored under a name the current taxonomy does not
// recognise -- an old cassette outliving a renamed or removed kind -- falls
// back to Unknown rather than panicking or resolving to the wrong kind by
// coincidence.
func TestReplayOfAnUnrecognisedKindNameFallsBackToUnknown(t *testing.T) {
	player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{
		Provider: "openai",
		Failure: &schemafluxtest.CassetteFailure{
			Message: "something failed",
			Kind:    "SomeKindThatNoLongerExists",
		},
	}).Lenient()

	_, err := player.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a recorded failure must still fail on replay")
	}
	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("replayed failure is not an *types.OperationError: %T", err)
	}
	if opErr.Kind != types.KindUnknown {
		t.Errorf("Kind = %v, want KindUnknown for an unrecognised name", opErr.Kind)
	}
}
