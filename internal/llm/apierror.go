package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A non-200 was reported by embedding the raw response body into a
// fmt.Errorf. Two problems with that, and they pull in opposite directions.
//
// The first is recoverability: a caller who wants to branch on "was this a
// 429 or a 400" had to substring-match an English sentence. The status code
// was right there and was thrown away.
//
// The second is disclosure. Neither the body nor the message inside it is ours.
// A provider's error can quote the request back -- a content filter naming the
// passage it objected to, a validation error echoing the schema it could not
// parse, and, from some gateways and proxies, the API key the request carried --
// and this library exists to run invoices, tickets, and medical notes through a
// model. The text then lands in the caller's logs, which is a place their users'
// records were never meant to reach.
//
// The first version of this file kept the vendor's structured `message` in the
// error's text and withheld only the raw body, on the argument that a message
// is what the provider wrote about the request. That argument failed against a
// gateway whose 401 message quotes the key: the key was in the error returned
// to the caller and in two of the library's own log lines. So the split is now
// by who wrote the words:
//
//   - what the error prints is the provider's fixed vocabulary and this
//     library's own words: the provider, the status, the model, the vendor's
//     `type`, `code` and `param` (bounded and printable, since a server chooses
//     them), the wait it asked for, and the fact that a message and a body were
//     withheld and how long each is.
//   - the vendor's message and the raw body are kept on the value, for a caller
//     who asks, with the provider's own key already removed and credential shapes
//     redacted, and are printed only by Detail() or while SCHEMAFLUX_ERROR_DETAIL
//     is set. One gate, one meaning: closed prints neither, open prints both.
//
// A body that is not a structured error -- an HTML proxy page, a truncated
// stream, an echo -- has no message to extract, so the error says the body was
// withheld rather than pretending there was nothing to report.

// maxMessageLength caps the retained vendor message, in runes. A provider that
// echoes a large input into its message field would otherwise be kept whole;
// truncating keeps the diagnostic without letting an unbounded quote through.
const maxMessageLength = 400

// maxClassifierLength and maxParamLength bound the vendor's `type`, `code` and
// `param`, in runes. They are meant to be enum values and property paths, but a
// server chooses them, and an unbounded one is a message by another name.
const (
	maxClassifierLength = 64
	maxParamLength      = 128
)

// errorDetailEnvVar opens the gate on the vendor's message and the raw body, for
// a developer debugging against their own data.
const errorDetailEnvVar = "SCHEMAFLUX_ERROR_DETAIL"

// APIError reports a provider request that failed with an HTTP status.
//
// Message and Body are the vendor's words. They are exported so that a caller
// can read them deliberately, and they are the reason this type has its own
// formatting, logging and JSON encoding: left to reflection, a caller who
// logged the struct, or marshalled it, would write both out. Everything that
// renders an APIError goes through render, which follows the gate.
type APIError struct {
	// Provider names who rejected the request.
	Provider string

	// Model is the model the request named, which is often the thing at fault.
	Model string

	// StatusCode is the HTTP status. This is the field a caller branches on,
	// and the reason this type exists rather than a formatted string.
	StatusCode int

	// Message is the vendor's own explanation, extracted from a structured
	// error body, bounded, with the provider's configured key removed. Empty
	// when the body was not a structured error. It is the vendor's free text and
	// is not printed by Error() unless the gate is open: use Detail().
	Message string

	// Type, Code, and Param are the vendor's structured classifiers, bounded and
	// printable. Param is the vendor's path to the offending parameter and can be
	// a property name of the caller's own schema, which is the caller's type and
	// not data.
	Type  string
	Code  string
	Param string

	// Body is the raw response body, retained for a caller who needs it, with the
	// provider's configured key removed and credential shapes redacted. It is not
	// printed by Error() unless the gate is open: use Detail().
	Body string
}

// Error prints the provider's fixed vocabulary and this library's words. The
// vendor's message and body are named, with their lengths, and not printed
// unless SCHEMAFLUX_ERROR_DETAIL is set.
func (e *APIError) Error() string {
	return e.render(0, detailEnabled())
}

// Detail returns the error with the vendor's message and the raw body, whatever
// the gate says: asking for it is the deliberate act the gate stands for. Both
// have had the provider's key removed and credential shapes redacted.
func (e *APIError) Detail() string {
	return e.detail(0)
}

// String and GoString make an APIError held by value print as Error does. A
// value has no Error method, since that is on the pointer, so fmt and slog
// would otherwise print its fields, Message and Body among them.
func (e APIError) String() string { return (&e).Error() }

// GoString is what %#v prints. It is the same safe text, in place of the Go
// syntax of every field.
func (e APIError) GoString() string { return (&e).Error() }

// LogValue is what slog logs for an APIError that is not logged as an error,
// such as one held by value, in a struct, or in a group. It is the safe text.
func (e APIError) LogValue() slog.Value { return slog.StringValue((&e).Error()) }

// MarshalJSON writes the fields that are safe to print and, only while the gate
// is open, the message and body. Without it encoding/json would write every
// exported field, Message and Body included.
func (e APIError) MarshalJSON() ([]byte, error) {
	return json.Marshal((&e).view(0, detailEnabled()))
}

// apiErrorView is what MarshalJSON writes.
type apiErrorView struct {
	Provider     string `json:"provider"`
	Model        string `json:"model,omitempty"`
	Status       int    `json:"status"`
	Type         string `json:"type,omitempty"`
	Code         string `json:"code,omitempty"`
	Param        string `json:"param,omitempty"`
	RetryAfter   string `json:"retry_after,omitempty"`
	MessageBytes int    `json:"message_bytes,omitempty"`
	BodyBytes    int    `json:"body_bytes,omitempty"`
	Message      string `json:"message,omitempty"`
	Body         string `json:"body,omitempty"`
}

func (e *APIError) view(retryAfter time.Duration, open bool) apiErrorView {
	message, body := e.words()
	view := apiErrorView{
		Provider:     e.Provider,
		Model:        e.Model,
		Status:       e.StatusCode,
		Type:         printableBounded(e.Type, maxClassifierLength),
		Code:         printableBounded(e.Code, maxClassifierLength),
		Param:        printableBounded(e.Param, maxParamLength),
		MessageBytes: len(message),
		BodyBytes:    len(body),
	}
	if retryAfter > 0 {
		view.RetryAfter = retryAfter.String()
	}
	if open {
		view.Message, view.Body = message, body
	}
	return view
}

// words returns the vendor's message and body, trimmed and with credential shapes
// redacted. An APIError built by this package already holds neither the
// provider's key nor a credential; one built by hand, such as a test double's,
// is covered here.
func (e *APIError) words() (message, body string) {
	return types.RedactCredentials(strings.TrimSpace(e.Message)),
		types.RedactCredentials(strings.TrimSpace(e.Body))
}

// header is the part of the text that is always the same: who, what status, for
// which model, and the vendor's classifiers. retryAfter is the wait a throttled
// answer asked for, and it goes next to the status because it is the part a
// reader acts on.
func (e *APIError) header(retryAfter time.Duration) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "%s API error (status %d", e.Provider, e.StatusCode)
	if retryAfter > 0 {
		fmt.Fprintf(&builder, ", retry after %s", retryAfter)
	}
	if e.Model != "" {
		fmt.Fprintf(&builder, ", model %s", e.Model)
	}
	// Ordered rather than ranged over a map, so the message is stable.
	for _, field := range []struct{ label, value string }{
		{"type", printableBounded(e.Type, maxClassifierLength)},
		{"code", printableBounded(e.Code, maxClassifierLength)},
		{"param", printableBounded(e.Param, maxParamLength)},
	} {
		if field.value != "" {
			fmt.Fprintf(&builder, ", %s %s", field.label, field.value)
		}
	}
	builder.WriteString(")")
	return builder.String()
}

// render is the text of the error: the header, then either the vendor's words,
// when the gate is open, or a note that they were withheld and how long they are.
func (e *APIError) render(retryAfter time.Duration, open bool) string {
	text := e.header(retryAfter)
	message, body := e.words()
	if message == "" && body == "" {
		return text
	}

	if open {
		var parts []string
		if message != "" {
			parts = append(parts, "message: "+message)
		}
		if body != "" {
			parts = append(parts, "body: "+body)
		}
		return text + ": " + strings.Join(parts, "; ")
	}

	// Saying nothing here would read as "the provider explained nothing", which
	// is a different and more confusing situation.
	var withheld []string
	if message != "" {
		withheld = append(withheld, fmt.Sprintf("message withheld (%d bytes)", len(message)))
	}
	if body != "" {
		withheld = append(withheld, fmt.Sprintf("body withheld (%d bytes)", len(body)))
	}
	return fmt.Sprintf("%s: %s; set %s=1 or call Detail() to read what was withheld", text, strings.Join(withheld, ", "), errorDetailEnvVar)
}

// detail is the header followed by the message and the body, one to a line.
func (e *APIError) detail(retryAfter time.Duration) string {
	text := e.header(retryAfter)
	message, body := e.words()
	if message != "" {
		text += "\nmessage: " + message
	}
	if body != "" {
		text += "\nbody: " + body
	}
	return text
}

// printableBounded keeps the printable characters of value and cuts the result
// to limit runes. The vendor's `type`, `code` and `param` are chosen by a server,
// and one that puts a line break, a control character or a paragraph in them is
// writing a message.
func printableBounded(value string, limit int) string {
	if value == "" {
		return ""
	}
	var builder strings.Builder
	kept := 0
	for _, r := range value {
		if !unicode.IsPrint(r) {
			continue
		}
		if kept == limit {
			builder.WriteString("…")
			break
		}
		builder.WriteRune(r)
		kept++
	}
	return strings.TrimSpace(builder.String())
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

// ErrorDetailEnvVar and ErrorDetailEnabled expose ADR 0001's gate to the other
// packages in this module that have to honour it.
//
// The codex provider (PS-013) withholds the CLI's own output from its error
// string for the same reason this file withholds a response body, and it has
// to open on the same signal. Re-reading the environment variable there would
// be a second copy of the rule, free to drift from this one -- and a gate that
// is open in one package and shut in another is worse than no gate, because a
// caller cannot reason about either. These are exported from an internal
// package, so nothing about the public API changes.
const ErrorDetailEnvVar = errorDetailEnvVar

// ErrorDetailEnabled reports whether the caller has asked for withheld
// provider output to be included in error strings.
func ErrorDetailEnabled() bool { return detailEnabled() }

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

// scrubber removes what must not be kept from text a provider sent: the
// provider's own configured credentials, and then every credential shape
// types.RedactCredentials knows. It is built where the provider builds the
// error, because that is the one place that holds the key, and the error then
// never holds it, so no later reader of Message or Body has to remember to.
type scrubber struct {
	secrets *strings.Replacer
}

// newScrubber returns a scrubber that removes each of secrets, in the forms a
// server may write it: as it is, and escaped as JSON, which is how a body that
// quotes it holds it.
func newScrubber(secrets []string) scrubber {
	seen := map[string]bool{}
	var forms []string
	add := func(form string) {
		if form != "" && !seen[form] {
			seen[form] = true
			forms = append(forms, form)
		}
	}
	for _, secret := range secrets {
		for _, form := range []string{secret, strings.TrimSpace(secret)} {
			add(form)
			if form == "" {
				continue
			}
			escaped, _ := json.Marshal(form)
			add(strings.Trim(string(escaped), `"`))
			var plain bytes.Buffer
			encoder := json.NewEncoder(&plain)
			encoder.SetEscapeHTML(false)
			if encoder.Encode(form) == nil {
				add(strings.Trim(strings.TrimSpace(plain.String()), `"`))
			}
		}
	}
	if len(forms) == 0 {
		return scrubber{}
	}
	// The longest first: a replacer takes the first of its pairs that matches,
	// and a secret that is the start of another would otherwise leave the rest.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	pairs := make([]string, 0, 2*len(forms))
	for _, form := range forms {
		pairs = append(pairs, form, "[redacted]")
	}
	return scrubber{secrets: strings.NewReplacer(pairs...)}
}

// text removes the secrets and the credential shapes from text.
func (s scrubber) text(text string) string {
	if s.secrets != nil {
		text = s.secrets.Replace(text)
	}
	return types.RedactCredentials(text)
}

// classifier is text for a field the vendor chose, scrubbed, printable and bounded.
func (s scrubber) classifier(text string, limit int) string {
	return printableBounded(s.text(text), limit)
}

// configSecrets lists what a provider configured with config sends as a
// credential: its key, and the value of any extra header named for one.
func configSecrets(config ProviderConfig) []string {
	secrets := []string{config.APIKey}
	for name, value := range config.ExtraHeaders {
		lower := strings.ToLower(name)
		for _, word := range []string{"auth", "key", "token", "secret"} {
			if strings.Contains(lower, word) {
				secrets = append(secrets, value)
				break
			}
		}
	}
	return secrets
}

// NewAPIError builds the typed error, extracting whatever the body explains.
// It is exported so a test double can produce the same shape a real provider
// does -- a fake whose errors are a different type than the real ones tests
// the wrong thing.
//
// secrets are the credentials the request carried. They are removed from the
// message, the body and the classifiers here, before anything is stored: a
// gateway that quotes the key back in its message would otherwise have the key
// in the error for as long as the error lives.
func NewAPIError(provider, model string, statusCode int, body string, secrets ...string) *APIError {
	scrub := newScrubber(secrets)
	apiErr := &APIError{
		Provider:   provider,
		Model:      model,
		StatusCode: statusCode,
		Body:       scrub.text(body),
	}

	var decoded vendorError
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return apiErr
	}

	if decoded.Error != nil {
		apiErr.fill(scrub, decoded.Error.Message, decoded.Error.Type, scalarString(decoded.Error.Code), scalarString(decoded.Error.Param))
		return apiErr
	}

	apiErr.fill(scrub, decoded.Message, decoded.Type, scalarString(decoded.Code), scalarString(decoded.Param))
	return apiErr
}

// fill sets the vendor's message and classifiers. The message is scrubbed before
// it is cut, so that a key the cut would split is not left half there.
func (e *APIError) fill(scrub scrubber, message, errType, code, param string) {
	e.Message = truncateMessage(scrub.text(message))
	e.Type = scrub.classifier(errType, maxClassifierLength)
	e.Code = scrub.classifier(code, maxClassifierLength)
	e.Param = scrub.classifier(param, maxParamLength)
}

// truncateMessage trims the message and cuts it to maxMessageLength runes. The
// limit counts runes and not bytes: a cut at a byte offset can fall inside a
// multi-byte character and leave a broken sequence in an error string that log
// pipelines and JSON encoders then replace or refuse.
func truncateMessage(message string) string {
	message = strings.TrimSpace(message)
	// A message of at most maxMessageLength bytes has at most that many runes,
	// so it fits without counting.
	if len(message) <= maxMessageLength {
		return message
	}
	kept := 0
	for offset := range message {
		if kept == maxMessageLength {
			return message[:offset] + "… (truncated)"
		}
		kept++
	}
	return message
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
