package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// A non-200 was reported by embedding the raw response body into a
// fmt.Errorf. Two problems with that, and they pull in opposite directions.
//
// The first is recoverability: a caller who wants to branch on "was this a
// 429 or a 400" had to substring-match an English sentence. The status code
// was right there and was thrown away.
//
// The second is disclosure. The body is not ours. A provider's error can
// quote the input back -- a content filter naming the passage it objected to,
// a validation error echoing the field it could not parse -- and this library
// exists to run invoices, tickets, and medical notes through a model. That
// body then lands in the caller's logs, which is a place their users' records
// were never meant to reach.
//
// The tension is real: the vendor's message is also the single most useful
// thing for debugging. "Invalid schema: exceeds maximum nesting depth" is how
// you find out what to fix, and an error that omits it just moves the cost
// onto the caller. So the split is by shape rather than by caution:
//
//   - the vendor's structured `message`, `type`, `code`, and `param` are kept
//     and printed. These are what the provider wrote ABOUT the request.
//   - the raw body is retained on the value but never printed by default.
//     That is what may contain the request itself.
//
// A body that is not a structured error -- an HTML proxy page, a truncated
// stream, an echo -- has no message to extract, so it is redacted whole and
// the error says so rather than pretending there was nothing to report.

// maxMessageLength caps the retained vendor message. A provider that echoes a
// large input into its message field defeats the split above; truncating keeps
// the diagnostic without letting an unbounded quote through.
const maxMessageLength = 400

// errorDetailEnvVar opts into printing the raw body, for a developer debugging
// against their own data.
const errorDetailEnvVar = "SCHEMAFLUX_ERROR_DETAIL"

// APIError reports a provider request that failed with an HTTP status.
type APIError struct {
	// Provider names who rejected the request.
	Provider string

	// Model is the model the request named, which is often the thing at fault.
	Model string

	// StatusCode is the HTTP status. This is the field a caller branches on,
	// and the reason this type exists rather than a formatted string.
	StatusCode int

	// Message is the vendor's own explanation, extracted from a structured
	// error body. Empty when the body was not one.
	Message string

	// Type, Code, and Param are the vendor's structured classifiers. They are
	// enum-valued rather than free text, so they are safe to print.
	Type  string
	Code  string
	Param string

	// Body is the raw response body, retained for a caller who needs it and
	// deliberately not printed by Error(). Use Detail().
	Body string
}

func (e *APIError) Error() string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "%s API error (status %d", e.Provider, e.StatusCode)
	if e.Model != "" {
		fmt.Fprintf(&builder, ", model %s", e.Model)
	}
	// Ordered rather than ranged over a map, so the message is stable.
	for _, field := range []struct{ label, value string }{
		{"type", e.Type}, {"code", e.Code}, {"param", e.Param},
	} {
		if field.value != "" {
			fmt.Fprintf(&builder, ", %s %s", field.label, field.value)
		}
	}
	builder.WriteString(")")

	switch {
	case e.Message != "":
		fmt.Fprintf(&builder, ": %s", e.Message)
	case strings.TrimSpace(e.Body) != "":
		if detailEnabled() {
			fmt.Fprintf(&builder, ": %s", strings.TrimSpace(e.Body))
		} else {
			// Saying nothing here would read as "the provider explained
			// nothing", which is a different and more confusing situation.
			fmt.Fprintf(&builder, ": response body withheld (%d bytes); set %s=1 to include it",
				len(e.Body), errorDetailEnvVar)
		}
	}

	return builder.String()
}

// Detail returns the error with the raw response body included. It is the
// escape hatch for a caller debugging against data they own.
func (e *APIError) Detail() string {
	if strings.TrimSpace(e.Body) == "" {
		return e.Error()
	}
	return fmt.Sprintf("%s\nbody: %s", e.Error(), strings.TrimSpace(e.Body))
}

// Retryable reports whether the status is worth another attempt. It is stated
// on the type so the classification does not depend on reading the message.
func (e *APIError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func detailEnabled() bool {
	value := strings.TrimSpace(os.Getenv(errorDetailEnvVar))
	return value == "1" || strings.EqualFold(value, "true")
}

// vendorError is the shape both the OpenAI and the OpenAI-compatible vendors
// use for an error body, in the two nestings they use it.
type vendorError struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Param   any    `json:"param"`
	} `json:"error"`

	// Cerebras and several compatible vendors put the fields at the top level.
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	Param   any    `json:"param"`
}

// NewAPIError builds the typed error, extracting whatever the body explains.
// It is exported so a test double can produce the same shape a real provider
// does -- a fake whose errors are a different type than the real ones tests
// the wrong thing.
func NewAPIError(provider, model string, statusCode int, body string) *APIError {
	apiErr := &APIError{
		Provider:   provider,
		Model:      model,
		StatusCode: statusCode,
		Body:       body,
	}

	var decoded vendorError
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return apiErr
	}

	if decoded.Error != nil {
		apiErr.Message = truncateMessage(decoded.Error.Message)
		apiErr.Type = decoded.Error.Type
		apiErr.Code = scalarString(decoded.Error.Code)
		apiErr.Param = scalarString(decoded.Error.Param)
		return apiErr
	}

	apiErr.Message = truncateMessage(decoded.Message)
	apiErr.Type = decoded.Type
	apiErr.Code = scalarString(decoded.Code)
	apiErr.Param = scalarString(decoded.Param)
	return apiErr
}

func truncateMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxMessageLength {
		return message
	}
	return message[:maxMessageLength] + "… (truncated)"
}

// scalarString renders a field the vendors type inconsistently -- sometimes a
// string, sometimes a number, often null -- without inventing a value for null.
func scalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case bool:
		return fmt.Sprintf("%t", typed)
	default:
		return ""
	}
}

// StatusCodeFrom recovers the HTTP status from an error produced anywhere in
// the provider layer, through whatever wrapping the call stack added.
func StatusCodeFrom(err error) (int, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode, true
	}
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		return rateLimit.StatusCode, true
	}
	return 0, false
}
