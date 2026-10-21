// Package provider builds the providers a client sends its requests to, lets a
// program add its own, says what a provider can do beyond answering a request,
// and declares where a route runs so that a client's data policy can be enforced.
//
// A provider is a schemaflux.Provider: a value with a Complete method that sends
// one request to a model and returns its answer, and a Name. The root package
// declares that contract; this package makes the ones the library ships.
//
// # Building a provider
//
// New builds a provider by name. A key given in code wins over the environment:
//
//	p, err := provider.New("openai", provider.Config{APIKey: key})
//	if err != nil {
//		return err
//	}
//	client := schemaflux.Client{Provider: p}
//
// With a zero Config, New reads the key from the environment, so a program that
// keeps its key there writes provider.New("openai", provider.Config{}). When no
// key is anywhere, New returns an error of kind schemaflux.ErrConfiguration that
// names the variables it looked in, never a value.
//
// The names New takes are "openai", "anthropic", "openrouter", "cerebras",
// "deepseek", "qwen" and "zai", which generate text, and "jev",
// "jev-openrouter" and "jev-litellm", which answer typed decisions (see
// Decider). There is no offline provider here: "local" and "mock", which answer
// every request with a placeholder, are refused with an error that names the
// schemafluxtest package, whose Provider is the one to use in a test and in a
// demonstration, and which fails a request nobody scripted. Names lists these and
// every name added with Register or RegisterKeyless.
//
// A provider built by this package has these settings unless the Config says
// otherwise: it sends a request that failed in transit again 3 times, waiting 1
// second before the first of those, and it gives up on one HTTP call after 30
// seconds. A zero value in Config means the default; a negative Retries means
// none. OpenAICompatible builds a provider for any server that speaks the chat
// completions dialect.
//
// # The environment
//
// New reads the environment only for what its Config leaves empty, and only for
// the provider it was asked for. In each row the first variable that is set
// wins, and SCHEMAFLUX_API_KEY is the key of any provider whose own variables
// are not set. A variable that is set to nothing counts as unset. A variable
// that is set to a value that cannot be used (a key with a control character, a
// base URL that is not an absolute http or https URL) is a configuration error
// that names the variable; it is not skipped for the next one.
//
//	provider        key variables, then the base URL variables
//	openai          SCHEMAFLUX_OPENAI_API_KEY, OPENAI_API_KEY;
//	                SCHEMAFLUX_OPENAI_BASE_URL, OPENAI_BASE_URL
//	anthropic       SCHEMAFLUX_ANTHROPIC_API_KEY, ANTHROPIC_API_KEY;
//	                SCHEMAFLUX_ANTHROPIC_BASE_URL, ANTHROPIC_BASE_URL
//	openrouter      SCHEMAFLUX_OPENROUTER_API_KEY, OPENROUTER_API_KEY;
//	                SCHEMAFLUX_OPENROUTER_BASE_URL, OPENROUTER_BASE_URL
//	cerebras        SCHEMAFLUX_CEREBRAS_API_KEY, CEREBRAS_API_KEY;
//	                SCHEMAFLUX_CEREBRAS_BASE_URL, CEREBRAS_BASE_URL
//	deepseek        SCHEMAFLUX_DEEPSEEK_API_KEY, DEEPSEEK_API_KEY;
//	                SCHEMAFLUX_DEEPSEEK_BASE_URL, DEEPSEEK_BASE_URL
//	qwen            SCHEMAFLUX_QWEN_API_KEY, QWEN_API_KEY, DASHSCOPE_API_KEY;
//	                SCHEMAFLUX_QWEN_BASE_URL, QWEN_BASE_URL, DASHSCOPE_BASE_URL
//	zai             SCHEMAFLUX_ZAI_API_KEY, ZAI_API_KEY, GLM_API_KEY;
//	                SCHEMAFLUX_ZAI_BASE_URL, ZAI_BASE_URL, GLM_BASE_URL
//	jev             SCHEMAFLUX_TYPESAFE_API_KEY, TYPESAFE_API_KEY;
//	                SCHEMAFLUX_TYPESAFE_BASE_URL, TYPESAFE_BASE_URL
//	jev-openrouter  the openrouter variables
//	jev-litellm     SCHEMAFLUX_LITELLM_API_KEY, LITELLM_API_KEY;
//	                SCHEMAFLUX_LITELLM_BASE_URL, LITELLM_BASE_URL
//
// The model variables (SCHEMAFLUX_MODEL and the three per tier) belong to the
// provider a client builds from the environment for itself, not to one built
// here: a provider built in code is never changed by a variable it was not
// given. Config.Models is where a program names the models of a provider it
// builds.
//
// # Errors
//
// A built provider returns every failure as a *schemaflux.Error. A 401 or a 403
// is ErrUnauthorized, a 429 is ErrRateLimited with the wait the provider asked
// for in RetryAfter, a 5xx is ErrUnavailable, a 408 or 504 is ErrTimeout, a 400
// is ErrInvalidRequest, and a canceled or expired context is ErrCanceled or
// ErrTimeout and matches context.Canceled or context.DeadlineExceeded with
// errors.Is. The error's text holds the kind, the route and the status, and
// none of what the provider wrote and none of the key; Detail returns the
// provider's own words, with the key already removed, for a developer reading
// a failure on data they own.
//
// # Wrapping a provider
//
// A wrapper that adds retries, a cache or a log is a Provider like any other. To
// keep what the provider underneath can do, give the wrapper an Unwrap method
// that returns it:
//
//	type logged struct{ schemaflux.Provider }
//
//	func (l logged) Unwrap() schemaflux.Provider { return l.Provider }
//
// The library looks for each optional interface (Streamer, Warmer, Retrier and
// Decider) on the provider it was given and then on what that wraps, through
// Unwrap, to a depth of 64. A wrapper with no Unwrap hides them: its provider
// cannot stream, be warmed, or say its own retry settings.
//
// # Adding a provider
//
// A program that has its own provider registers a Factory under a name, and
// New builds it from then on, with the defaults above applied to the Config it
// is given:
//
//	err := provider.Register("acme", func(cfg provider.Config) (schemaflux.Provider, error) {
//		return &acme{key: cfg.APIKey, timeout: cfg.Timeout}, nil
//	})
//
// Registering a name that is taken, a built-in one included, is an error of kind
// schemaflux.ErrConfiguration. There is no way to remove a name: register at
// start-up, once. A provider that is used in one place only needs no name:
// put the value in Client.Provider.
//
// # Declaring where a route runs
//
// A client whose data policy names the providers and regions it allows refuses a
// request to a route it cannot place. DeclareRoute says where one provider and
// model run and how long they keep what they are sent:
//
//	provider.DeclareRoute("openai", "gpt-6-luna", []string{"us"}, 30)
//
// With that declaration, a policy that allows provider "openai", region "us" and
// at most 30 days of retention allows the route. A route that is not declared is
// not known, and a policy that asks for a region refuses it. Declare routes once,
// at start-up.
package provider
