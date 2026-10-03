package llm

import (
	"fmt"
	"io"
	"net/http"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Provider responses are read under a ceiling. BaseURL is chosen by the caller
// and may name a server that is hostile or broken, and a body read with
// io.ReadAll or a json.Decoder is held in memory whole, so a server that never
// stops sending took the process's memory with it.
const (
	// maxResponseBodyBytes bounds a buffered answer. The largest answer a model
	// returns is its output ceiling, about 128 thousand tokens, which is half a
	// megabyte of text; the rest of the body is usage and bookkeeping. 16 MiB is
	// thirty times that, and far below anything that threatens memory.
	maxResponseBodyBytes = 16 << 20

	// maxErrorBodyBytes bounds the body of a non-200 answer. Only its structured
	// message is read; the rest is kept on the error for a caller who asks for it.
	// A vendor error can quote the input back, and the input is at most one
	// context window, so a megabyte holds the quote and the message around it.
	maxErrorBodyBytes = 1 << 20

	// maxEmbeddingBodyBytes bounds an embeddings answer, which is mostly numbers.
	// The API takes up to 2048 inputs and its largest model returns 3072
	// dimensions, which as JSON floats of about 22 bytes each is about 140 MB, so
	// the ceiling sits above that rather than refusing a batch the API allows.
	maxEmbeddingBodyBytes = 192 << 20

	// maxStreamEventBytes bounds one server-sent event: the longest line of it,
	// and the total of its data lines. A "response.completed" event carries the
	// whole answer as a single line, so this has to hold the largest answer plus
	// the usage block around it; 4 MiB is eight times a model's output ceiling.
	maxStreamEventBytes = 4 << 20
)

// readBounded reads body to its end, and refuses one longer than limit rather
// than returning the first limit bytes of it as if they were the whole answer.
// Reading to the end is also what lets the connection go back to the pool.
func readBounded(body io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, types.Newf(types.KindMalformedOutput, "llm.read",
			"the response body is longer than the %d bytes this library reads", limit)
	}
	return raw, nil
}

// readErrorBody returns what a non-200 answer says. A body longer than the
// ceiling is cut there, because a cut error body is still an error body: what
// is wanted from it is the status, which is already known, and the message.
func readErrorBody(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	return string(raw)
}

// statusError builds the typed error for a response whose status is not 200:
// the one place every provider turns a failed HTTP answer into an error, so that
// they cannot disagree about what a status means. The config is the provider's
// own: it is where the key is, and the key is removed from the error here.
//
// A throttled or unavailable answer (see carriesRetryAfter) carries the wait the
// server asked for. Classify reads the kind from the status, so a 503 here is
// reported as the provider being unavailable and a 429 as a rate limit.
func statusError(provider, model string, resp *http.Response, config ProviderConfig) error {
	body := readErrorBody(resp.Body)
	if carriesRetryAfter(resp.StatusCode) {
		return rateLimitError(provider, model, resp, body, config)
	}
	return NewAPIError(provider, model, resp.StatusCode, body, configSecrets(config)...)
}

// incompleteAnswerError is the error for an answer that arrived with no text
// because the provider stopped it. The identical request would be stopped the
// same way, so the kind is one the retry loop does not repeat. The vendor's text
// is not carried: it is the vendor's, and it can quote the input.
func incompleteAnswerError(provider, model, reason string) error {
	switch reason {
	case "max_output_tokens", "max_tokens", "length":
		return &types.OperationError{
			Kind:     types.KindOutputTruncated,
			Op:       "llm.complete",
			Provider: provider,
			Model:    model,
			Message:  "the answer was cut off at the token ceiling before any text was produced; raise MaxTokens, or lower the reasoning effort so that reasoning does not use the whole ceiling",
		}
	case "content_filter":
		return &types.OperationError{
			Kind:     types.KindPolicyViolation,
			Narrow:   types.NarrowRefusal,
			Op:       "llm.complete",
			Provider: provider,
			Model:    model,
			Message:  "the answer was withheld by the provider's content filter",
		}
	}
	return &types.OperationError{
		Kind:     types.KindMalformedOutput,
		Op:       "llm.complete",
		Provider: provider,
		Model:    model,
		Message:  "the provider reported the answer incomplete, for a reason this library does not recognise, and returned no text",
	}
}

// refusedAnswerError is the error for an answer the model declined to give. Like
// the content-filter case of incompleteAnswerError it carries the refusal mark,
// which is what tells a refusal from the other policy violations (a data policy
// that forbids a route) to a boundary that reports them as different kinds.
func refusedAnswerError(provider, model string) error {
	return &types.OperationError{
		Kind:     types.KindPolicyViolation,
		Narrow:   types.NarrowRefusal,
		Op:       "llm.complete",
		Provider: provider,
		Model:    model,
		Message:  fmt.Sprintf("%s declined to answer", provider),
	}
}

// streamEventTooLongError is the error for a server-sent event past
// maxStreamEventBytes, whether in one line or in the lines of one event.
func streamEventTooLongError() error {
	return types.Newf(types.KindMalformedOutput, "llm.stream",
		"a server-sent event is longer than the %d bytes this library reads", maxStreamEventBytes)
}
