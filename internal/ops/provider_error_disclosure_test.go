package ops

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A gateway in front of a provider can answer a rejected request with a message
// that quotes the request back: the key it was sent, a fragment of the prompt,
// the instructions. The library's rule is that nothing it returns in an error's
// text and nothing it logs carries a credential, the caller's data or the
// provider's free text. These tests run a real provider against such a gateway
// through the call path, with every log level on, and look for the three markers
// in the error the caller gets and in every line the library wrote.

const (
	// The key is of no shape types.RedactCredentials knows, so only removing the
	// configured key itself keeps it out.
	gatewayKey          = "gw-key-7f3a91c2d41d8cd98f00b204e9800998"
	gatewayUserMarker   = "USER-PROMPT-MARKER-4c1e9b"
	gatewaySystemMarker = "SYSTEM-PROMPT-MARKER-82ad07"
)

// gatewayMessage is what the gateway writes as its message.
const gatewayMessage = "Incorrect API key provided: " + gatewayKey +
	". Refused near \"" + gatewayUserMarker + "\" under \"" + gatewaySystemMarker + "\"."

func gatewayLeaks(text string) []string {
	var found []string
	for name, marker := range map[string]string{
		"the API key":       gatewayKey,
		"the user prompt":   gatewayUserMarker,
		"the system prompt": gatewaySystemMarker,
	} {
		if strings.Contains(text, marker) {
			found = append(found, name)
		}
	}
	return found
}

// gatewayProvider is the real OpenAI provider pointed at a loopback server that
// answers every request with status and a structured error carrying the message.
// It retries, so the retry line is written too.
func gatewayProvider(t *testing.T, status int) (provider llm.Provider, requests func() int) {
	t.Helper()
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":%q}}`, gatewayMessage)
	}))
	t.Cleanup(server.Close)

	built, err := llm.NewOpenAIProvider(llm.ProviderConfig{
		APIKey:       gatewayKey,
		BaseURL:      server.URL,
		Timeout:      5 * time.Second,
		MaxRetries:   2,
		RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	return built, func() int { return int(received.Load()) }
}

func gatewayOptions() types.OpOptions {
	return types.OpOptions{
		Context:        context.Background(),
		Mode:           types.TransformMode,
		Intelligence:   types.Fast,
		ResponseFormat: "json",
	}
}

func TestAFailedProviderCallLeavesTheVendorsWordsOutOfTheErrorAndTheLog(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized, http.StatusBadRequest,
		http.StatusTooManyRequests, http.StatusInternalServerError,
	} {
		t.Run(fmt.Sprintf("CallLLM with a %d", status), func(t *testing.T) {
			logs := captureLogs(t)
			provider, requests := gatewayProvider(t, status)

			_, err := CallLLM(context.Background(), provider,
				"Instructions: "+gatewaySystemMarker+" Return only valid JSON.",
				"The record: "+gatewayUserMarker, gatewayOptions())
			if err == nil {
				t.Fatal("the call succeeded")
			}
			if got, ok := llm.StatusCodeFrom(err); !ok || got != status {
				t.Fatalf("StatusCodeFrom = %d, %v; want %d", got, ok, status)
			}
			if retryable := status == http.StatusTooManyRequests || status == http.StatusInternalServerError; retryable && requests() != 3 {
				t.Fatalf("the provider was called %d times; a retryable status is attempted three times, so the retry line is not witnessed", requests())
			}

			if leaks := gatewayLeaks(err.Error()); len(leaks) > 0 {
				t.Errorf("the returned error carries %s: %s", strings.Join(leaks, " and "), err)
			}
			written := logs()
			if leaks := gatewayLeaks(written); len(leaks) > 0 {
				t.Errorf("the log carries %s:\n%s", strings.Join(leaks, " and "), written)
			}
			if !strings.Contains(written, "LLM request failed") {
				t.Errorf("the failure line was not written, so nothing was witnessed:\n%s", written)
			}
			// The line says what failed in the library's words: the kind of the
			// failure, and nothing the vendor or the error's text said.
			kind := map[int]string{
				http.StatusUnauthorized:        types.KindAuthentication.String(),
				http.StatusBadRequest:          types.KindInvalidRequest.String(),
				http.StatusTooManyRequests:     types.KindRateLimited.String(),
				http.StatusInternalServerError: types.KindProviderUnavailable.String(),
			}[status]
			if !strings.Contains(written, "error:"+kind+" ") {
				t.Errorf("the failure line does not carry the kind %q:\n%s", kind, written)
			}
			if strings.Contains(written, "withheld") || strings.Contains(written, "invalid_api_key") {
				t.Errorf("the failure line carries the text of the error or the vendor's code:\n%s", written)
			}
		})
	}
}

// Through an operation, so the error a caller of the public surface receives
// has been wrapped and logged by the layers above the call as well.
func TestAnOperationOverAFailingProviderLeavesTheVendorsWordsOutToo(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("Complete with a %d", status), func(t *testing.T) {
			logs := captureLogs(t)
			provider, _ := gatewayProvider(t, status)

			opts := NewCompleteOptions()
			_, err := Complete(WithProvider(context.Background(), provider), "The record: "+gatewayUserMarker, opts)
			if err == nil {
				t.Fatal("the operation succeeded")
			}
			if leaks := gatewayLeaks(err.Error()); len(leaks) > 0 {
				t.Errorf("the returned error carries %s: %s", strings.Join(leaks, " and "), err)
			}
			written := logs()
			// The record is the operation's own input, so the line that names its
			// length is allowed; a marker can only be in the log if something
			// copied the vendor's message or the key.
			for _, marker := range []string{gatewayKey, gatewaySystemMarker} {
				if strings.Contains(written, marker) {
					t.Errorf("the log carries %q:\n%s", marker, written)
				}
			}
			if strings.Contains(written, "Incorrect API key") {
				t.Errorf("the log carries the vendor's message:\n%s", written)
			}
		})
	}
}

// The control: with the gate open the vendor's words are meant to appear in the
// text of the error a caller asks for them in, and the key is still not there. No
// log line follows the gate: a line carries the kind of a failure and nothing of
// the error's text, whatever the variable says, so the library never turns a
// setting of the environment into a leak in a log.
func TestTheOpenGateShowsTheVendorsWordsInTheErrorAndNeverInTheLogOrTheKey(t *testing.T) {
	t.Setenv(llm.ErrorDetailEnvVar, "1")
	logs := captureLogs(t)
	provider, _ := gatewayProvider(t, http.StatusUnauthorized)

	_, err := CallLLM(context.Background(), provider, "Return only valid JSON.", "The record.", gatewayOptions())
	if err == nil {
		t.Fatal("the call succeeded")
	}
	if !strings.Contains(err.Error(), "Incorrect API key provided") {
		t.Errorf("the error does not carry the vendor's message with the gate open:\n%s", err)
	}
	written := logs()
	for label, text := range map[string]string{"the error": err.Error(), "the log": written} {
		if strings.Contains(text, gatewayKey) {
			t.Errorf("%s carries the key with the gate open:\n%s", label, text)
		}
	}
	if !strings.Contains(written, "LLM request failed") {
		t.Fatalf("the failure line was not written, so nothing was witnessed:\n%s", written)
	}
	for _, marker := range []string{"Incorrect API key provided", gatewayUserMarker, gatewaySystemMarker, "invalid_api_key"} {
		if strings.Contains(written, marker) {
			t.Errorf("the log carries %q with the gate open:\n%s", marker, written)
		}
	}
}
