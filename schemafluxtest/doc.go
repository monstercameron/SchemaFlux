// Package schemafluxtest is to this library what net/http/httptest is to
// net/http: a fake model that needs no key, no network and no money, so that
// code which calls the library can be tested.
//
// # A first test
//
// A [Provider] is ready to use as it is declared. Script what it answers, hand
// it to a client, and read afterwards what it was sent:
//
//	func TestInvoiceReachesTheModel(t *testing.T) {
//		t.Parallel()
//		var model schemafluxtest.Provider
//		model.Reply(`{"number":"A-17","total":120.5}`)
//		ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: &model})
//
//		invoice, err := schemaflux.Extract[Invoice](ctx, "Invoice A-17 for 120.50")
//		if err != nil {
//			t.Fatal(err)
//		}
//		if invoice.Total != 120.5 {
//			t.Errorf("total = %v", invoice.Total)
//		}
//		if !strings.Contains(model.Last().User, "A-17") {
//			t.Error("the invoice text did not reach the model")
//		}
//	}
//
// Every operation is a function of a context, so the client travels on the
// context and nothing is global. Code that takes its context from its caller
// tests in parallel, as the example does.
//
// # What the fake is
//
// A Provider is named "test". It knows one model, "test-model", which it gives
// to every tier. It answers "{}" until it is scripted, and it retries nothing:
// it tells the library not to retry, so a test that scripts one failure sees
// one request and one failure. It costs nothing: its answers carry no token
// count unless the test sets one with SetUsage.
//
// A Provider is scripted with Reply (a body, or a sequence of bodies, of which
// the last repeats), Fail and FailThen (an error, which may carry a kind), Shape
// (an answer that fits the request's own schema), Delay (a wait that ends with
// the request's context), Respond (any answer, chosen by the request) and
// RespondContext (the same, given the request's context, with Wait to make one
// request slow and let a cancellation end its wait). It
// records every request it receives, in the order they arrived: Requests, Last
// and Calls read them. It is also a provider.Streamer: Stream delivers the same
// scripted answer in pieces, so a streamed operation is tested with no stub of
// its own.
//
// A Provider is safe for concurrent use. It holds no state outside itself, so
// two fakes in two parallel tests never see each other's requests, and one fake
// used from fifty goroutines counts every request once.
//
// # Telling operations apart
//
// A script can depend on which operation made the request. A Request does not
// carry the operation's name, so a fake tells operations apart by what the
// request does carry: SchemaName, where the operation declares a schema (Extract
// names it for the Go type it fills), and System, the system prompt, which is
// fixed for each operation and never holds the caller's data. ReplyWhen is the
// helper for it:
//
//	model.ReplyWhen("invoice", `{"number":"A-17","total":120.5}`)
//	model.ReplyWhen("summar", "Short.")
//	model.Reply(`{}`) // every other request
//
// It matches text in the schema name or in the system prompt, without regard to
// case. The user message is the caller's data, and it is not matched: a test
// that answers by what the caller sent uses Respond, which is given the whole
// Request.
//
// # Reading a script's failure
//
// A fake never answers with an empty result when its script does not cover a
// request. A request that no ReplyWhen claims, in a Provider that has no other
// answer, and a Decider asked a question it has no answer for, fail with an
// *Error of kind ErrConfiguration, which is the test's own set-up. The message
// says what the request asked (its model, its schema name, the start of its
// system prompt, the size of its user message, never the user message itself)
// and what was scripted, in at most 600 characters.
//
// # Decisions
//
// An operation that decides (Is, Which, Rate and the others) sends its
// questions on Request.Decision beside its prompts. A Provider ignores the
// field, as a model that generates text does, and answers the prompts. It
// answers a decision in the form the library reads from a text model: a JSON
// object with one member for each question, named q1, q2 and so on in the order
// asked, whose value is true or false for a yes or no, the number of the chosen
// option counted from one for a choice, and the level for a scale; and a
// "confidence" member, a number from 0 to 1, for the probability the model
// reports. For three questions:
//
//	model.Reply(`{"q1":true,"q2":3,"q3":4,"confidence":0.67}`)
//
// A [Decider] stands in for a decision model instead: it answers the typed
// questions with typed answers (Yes, No, Chose, Level), one for each question in
// order, and writes no text. It declares the limits a decision model declares,
// and refuses a request that carries no decision or carries one past them, as a
// decision model does.
//
// # Process state
//
// The fakes themselves hold none. Two things a test may want to set up do:
// pricing.Register adds a price to the process's table, and provider.DeclareRoute
// declares where a route runs, and neither has an undo. A parallel test that
// needs either gives its fake a name of its own (SetName) and calls on a model
// name of its own (the client's Model, or the call's), and registers prices and
// routes for those: a price is looked up by the model's name alone, so a price
// registered for "test-model" is shared with every other test in the process.
// Until a price is registered the fake's model is unpriced, a Record's Cost is
// not Priced, and a client's Budget never refuses a call on it.
//
// Code that calls on the default client has no context to carry the fake, and
// is tested with Install, which puts a fake in as the default client's provider
// for the length of a test. It changes the process, so it cannot be used in a
// parallel test.
//
// Tests inside the library's own internal packages cannot import this package;
// they keep the fixtures of internal/testfixtures.
package schemafluxtest
