package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Classify maps a provider failure onto the shared taxonomy.
//
// This is the one place that decides what kind of failure something is, so the
// answer is the same whichever operation asked. The order matters: a typed
// error is read for its status, and only an error carrying no type at all falls
// through to matching text -- which is what the whole taxonomy exists to stop
// callers doing, and is kept here so a provider that has not been taught to
// return typed errors yet still lands somewhere sensible.
func Classify(err error) types.ErrorKind {
	if err == nil {
		return types.KindUnknown
	}

	// Already classified upstream.
	if kind := types.KindOf(err); kind != types.KindUnknown {
		return kind
	}

	if errors.Is(err, context.Canceled) {
		return types.KindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return types.KindTimeout
	}

	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		return types.KindRateLimited
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return kindForStatus(apiErr.StatusCode)
	}

	// A transport failure that never reached the provider.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return types.KindTimeout
		}
		return types.KindProviderUnavailable
	}

	return kindFromText(err.Error())
}

func kindForStatus(status int) types.ErrorKind {
	switch status {
	case http.StatusUnauthorized:
		return types.KindAuthentication
	case http.StatusForbidden:
		return types.KindPermission
	case http.StatusTooManyRequests:
		return types.KindRateLimited
	case http.StatusRequestEntityTooLarge:
		return types.KindContextTooLarge
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return types.KindTimeout
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusConflict:
		return types.KindProviderUnavailable
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusNotFound:
		return types.KindInvalidRequest
	}

	switch {
	case status >= 500:
		return types.KindProviderUnavailable
	case status >= 400:
		return types.KindInvalidRequest
	}
	return types.KindUnknown
}

// kindFromText is the fallback for errors that carry no type.
//
// It is deliberately narrow. Substring matching is what P-007 removed from the
// retry classifier, after a 500 whose body happened to contain
// "invalid_request_error" was classified permanent and cost the caller every
// retry they were entitled to -- so this only matches phrases this library
// itself produces, never a vendor's prose.
func kindFromText(message string) types.ErrorKind {
	lowered := strings.ToLower(message)

	switch {
	case strings.Contains(lowered, "no llm provider configured"),
		strings.Contains(lowered, "no default model for provider"),
		strings.Contains(lowered, "provider has no model mapping"):
		return types.KindConfiguration
	case strings.Contains(lowered, "context deadline exceeded"):
		return types.KindTimeout
	case strings.Contains(lowered, "context canceled"):
		return types.KindCanceled
	case strings.Contains(lowered, "budget"):
		return types.KindBudgetExceeded
	}

	return types.KindUnknown
}

// ClassifyCompletion classifies a response the provider returned successfully
// but which cannot be used.
//
// A 200 is not a logical success: a truncated body arrives with the same status
// as a complete one, and the finish reason is the only thing that says so.
func ClassifyCompletion(resp CompletionResponse) types.ErrorKind {
	switch strings.ToLower(resp.FinishReason) {
	// "max_output_tokens" is the Responses API's actual incomplete reason and
	// was missing from this list, so the one provider this library targets
	// first reported a truncated answer as KindUnknown -- ST-003's whole point
	// is that a cut-off answer is not a parse failure, and against real OpenAI
	// it was neither. The existing provider test only asserted the raw
	// FinishReason string and never fed it through here, which is how a list
	// of four spellings missed the one that ships.
	case "length", "max_tokens", "max_output_tokens", "incomplete", "truncated":
		return types.KindOutputTruncated
	case "content_filter":
		return types.KindPolicyViolation
	}

	if strings.TrimSpace(resp.Content) == "" {
		return types.KindMalformedOutput
	}
	return types.KindUnknown
}
