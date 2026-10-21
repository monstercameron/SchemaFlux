package requestid_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/requestid"
)

// A server takes the id of an incoming request, and passes it on to the
// services it calls. Every library call made with the context carries it.
func Example() {
	incoming := httptest.NewRequest(http.MethodGet, "/orders/7", nil)
	incoming.Header.Set("X-Request-ID", "order-7-4f2a")

	ctx := requestid.Extract(incoming.Context(), incoming.Header)
	ctx = requestid.WithCorrelation(ctx, "nightly-sync")
	fmt.Printf("%+v\n", requestid.From(ctx))

	outgoing := http.Header{}
	requestid.Inject(ctx, outgoing)
	fmt.Println(outgoing.Get("X-Request-ID"), outgoing.Get("X-Correlation-ID"))

	// Output:
	// {Request:order-7-4f2a Correlation:nightly-sync}
	// order-7-4f2a nightly-sync
}

func ExampleWith() {
	ctx := requestid.With(context.Background(), "job-1042")
	fmt.Println(requestid.From(ctx).Request)

	// The nearer id wins.
	inner := requestid.With(ctx, "job-1042/retry-2")
	fmt.Println(requestid.From(inner).Request)

	// An id that is not printable ASCII without spaces, or is longer than 128
	// bytes, is not stored; the context keeps the id it had.
	refused := requestid.With(ctx, "two words")
	fmt.Println(requestid.From(refused).Request)

	longer := requestid.With(context.Background(), strings.Repeat("x", 129))
	fmt.Printf("%q\n", requestid.From(longer).Request)

	// Output:
	// job-1042
	// job-1042/retry-2
	// job-1042
	// ""
}

func ExampleWithCorrelation() {
	ctx := requestid.With(context.Background(), "req-9")
	ctx = requestid.WithCorrelation(ctx, "checkout-flow-31")
	ids := requestid.From(ctx)
	fmt.Println(ids.Request, ids.Correlation)

	// Output:
	// req-9 checkout-flow-31
}

func ExampleFrom() {
	fmt.Printf("%+v\n", requestid.From(context.Background()))

	ctx := requestid.With(context.Background(), "req-9")
	fmt.Printf("%+v\n", requestid.From(ctx))

	// Output:
	// {Request: Correlation:}
	// {Request:req-9 Correlation:}
}

func ExampleInject() {
	ctx := requestid.With(context.Background(), "req-9")
	ctx = requestid.WithCorrelation(ctx, "checkout-flow-31")

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://billing.example/charge", nil)
	requestid.Inject(req.Context(), req.Header)
	fmt.Println(req.Header.Get("X-Request-ID"))
	fmt.Println(req.Header.Get("X-Correlation-ID"))

	// Output:
	// req-9
	// checkout-flow-31
}

func ExampleExtract() {
	h := http.Header{}
	h.Set("X-Request-ID", "req-9")
	h.Set("X-Correlation-ID", "this one has spaces and is refused")

	ctx := requestid.Extract(context.Background(), h)
	fmt.Printf("%+v\n", requestid.From(ctx))

	// Output:
	// {Request:req-9 Correlation:}
}

// Configure changes the process, and its result puts the change back.
func ExampleConfigure() {
	restore, err := requestid.Configure(requestid.Config{
		Strategy:          requestid.StrategyUUID,
		Header:            "Request-Id",
		CorrelationHeader: "Job-Id",
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	ctx := requestid.With(context.Background(), "req-9")
	ctx = requestid.WithCorrelation(ctx, "job-31")
	h := http.Header{}
	requestid.Inject(ctx, h)
	fmt.Println(h.Get("Request-Id"), h.Get("Job-Id"))

	restore()

	h = http.Header{}
	requestid.Inject(ctx, h)
	fmt.Println(h.Get("X-Request-ID"), h.Get("X-Correlation-ID"), h.Get("Request-Id") == "")

	// Output:
	// req-9 job-31
	// req-9 job-31 true
}

// A Config that cannot work is refused with an error of kind ErrConfiguration,
// and nothing changes.
func ExampleConfigure_refused() {
	restore, err := requestid.Configure(requestid.Config{Header: "not a header name"})
	fmt.Println(restore == nil, errors.Is(err, schemaflux.ErrConfiguration))
	fmt.Println(err)

	// Output:
	// true true
	// requestid.configure: configuration: Config.Header is not a valid HTTP header name
}
