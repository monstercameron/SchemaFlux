// Package schemaflux turns language-model calls into typed Go calls. A caller
// hands it Go values and names the Go type it wants back, and gets a value of
// that type or an error; the library owns the prompt, the declared shape of the
// answer, the parsing, the refusal of an answer that does not conform, the
// retries and the cost. What happened during a call (the model, the tokens, the
// cost, the time, the repairs) is not part of the answer: a Recorder placed on
// the context collects it, as net/http/httptrace collects the events of a
// request.
package schemaflux
