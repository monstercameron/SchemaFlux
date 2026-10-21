package requestid

import (
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/setting"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Strategy says how a request id is made for a call whose context carries none.
// The zero value is StrategyAuto.
type Strategy uint8

const (
	// StrategyAuto makes "req_" and sixteen hexadecimal digits. It is the zero
	// Strategy.
	StrategyAuto Strategy = iota

	// StrategyUUID makes a random version 4 UUID, in its 8-4-4-4-12 form.
	StrategyUUID

	// StrategyTimestamp makes "req_", the time in nanoseconds, the process id and
	// a counter, joined by underscores.
	StrategyTimestamp

	// StrategyTrace uses the trace id of the span on the call's context, when the
	// program has set up tracing through the telemetry/otel subpackage, and makes
	// the id StrategyAuto would when there is none.
	StrategyTrace

	// StrategyNone makes no request id.
	StrategyNone
)

// Correlation says how a correlation id is made for a call whose context
// carries none. The zero value is CorrelationInherit.
type Correlation uint8

const (
	// CorrelationInherit uses the request id of the call as its correlation id,
	// and is the zero Correlation. With no request id to use it makes the id
	// CorrelationGenerate would. A correlation id the context carries is always
	// kept, which is the sense of the name.
	CorrelationInherit Correlation = iota

	// CorrelationGenerate makes "corr_" and sixteen hexadecimal digits.
	CorrelationGenerate

	// CorrelationNone makes no correlation id.
	CorrelationNone
)

// Config says how ids are made and which headers carry them. The zero Config is
// the default behaviour: ids are made, as StrategyAuto and CorrelationInherit
// make them, and travel in X-Request-ID and X-Correlation-ID.
type Config struct {
	// Disabled stops the library making ids for calls whose context carries
	// none. Ids that the context carries are still used, and still written by
	// Inject. The zero value, false, makes ids.
	Disabled bool

	// Strategy says how a request id is made when the context carries none. The
	// zero value is StrategyAuto.
	Strategy Strategy

	// Correlation says how a correlation id is made when the context carries
	// none. The zero value is CorrelationInherit.
	Correlation Correlation

	// Header is the name of the HTTP header that carries the request id, matched
	// without regard to case. It must be a valid header field name and differ
	// from CorrelationHeader. The empty string means "X-Request-ID".
	Header string

	// CorrelationHeader is the name of the HTTP header that carries the
	// correlation id, under the same rules as Header. The empty string means
	// "X-Correlation-ID".
	CorrelationHeader string
}

const (
	defaultHeader            = "X-Request-ID"
	defaultCorrelationHeader = "X-Correlation-ID"
)

// tracking converts c to the configuration of internal/requesttracking, filling
// every default so that nothing is left for that package to read from its
// environment. It returns an error, whose text begins "requestid: " and names the
// field but not the value the caller gave, if c cannot work.
func (c Config) tracking() (requesttracking.Config, error) {
	cfg := requesttracking.Config{
		Enabled:             !c.Disabled,
		RequestIDHeader:     defaultHeader,
		CorrelationIDHeader: defaultCorrelationHeader,
	}
	switch c.Strategy {
	case StrategyAuto:
		cfg.RequestIDStrategy = requesttracking.IDStrategyAuto
	case StrategyUUID:
		cfg.RequestIDStrategy = requesttracking.IDStrategyUUID
	case StrategyTimestamp:
		cfg.RequestIDStrategy = requesttracking.IDStrategyTimestamp
	case StrategyTrace:
		cfg.RequestIDStrategy = requesttracking.IDStrategyTrace
	case StrategyNone:
		cfg.RequestIDStrategy = requesttracking.IDStrategyNone
	default:
		return requesttracking.Config{}, misconfigured("Config.Strategy is not one of the Strategy constants")
	}
	switch c.Correlation {
	case CorrelationInherit:
		cfg.CorrelationIDStrategy = requesttracking.CorrelationStrategyInherit
	case CorrelationGenerate:
		cfg.CorrelationIDStrategy = requesttracking.CorrelationStrategyGenerate
	case CorrelationNone:
		cfg.CorrelationIDStrategy = requesttracking.CorrelationStrategyNone
	default:
		return requesttracking.Config{}, misconfigured("Config.Correlation is not one of the Correlation constants")
	}
	if c.Header != "" {
		if !isHeaderName(c.Header) {
			return requesttracking.Config{}, misconfigured("Config.Header is not a valid HTTP header name")
		}
		cfg.RequestIDHeader = c.Header
	}
	if c.CorrelationHeader != "" {
		if !isHeaderName(c.CorrelationHeader) {
			return requesttracking.Config{}, misconfigured("Config.CorrelationHeader is not a valid HTTP header name")
		}
		cfg.CorrelationIDHeader = c.CorrelationHeader
	}
	if strings.EqualFold(cfg.RequestIDHeader, cfg.CorrelationIDHeader) {
		return requesttracking.Config{}, misconfigured("Config.Header and Config.CorrelationHeader cannot share one header name, compared without regard to case")
	}
	return cfg, nil
}

// isHeaderName reports whether name is an HTTP token: one or more of the
// letters, digits and the marks !#$%&'*+-.^_`|~. A name with any other byte, a
// space or a colon among them, is not a header field name that net/http will
// send.
func isHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0:
		default:
			return false
		}
	}
	return true
}

// layers holds the configurations that calls to Configure have put in force, so
// that what is in force always matches the newest one nobody has restored. It
// writes the tracking code's configuration under its own lock.
var layers = setting.Layers[requesttracking.Config]{
	Apply: requesttracking.Configure,
	Base:  func() requesttracking.Config { return defaults },
}

// defaults is the configuration a process has when no Configure is in force.
var defaults = func() requesttracking.Config {
	cfg, err := Config{}.tracking()
	if err != nil {
		panic(err) // the zero Config is valid; this is a fault in the package, not in a caller's value
	}
	return cfg
}()

func init() {
	// internal/requesttracking reads five SCHEMAFLUX_REQUEST_* variables itself
	// when nothing has configured it. Configuring it here, with every field
	// given, means this package and the calls that follow it never read them.
	requesttracking.Configure(defaults)
}

// Configure sets how ids are made and which headers carry them, for the whole
// process: every call the library makes in any goroutine, with any client, uses
// the configuration until it is changed again. It is not a setting of a context
// or of a client. Call it while the program starts, or in a test, and not while
// calls are running that must see one configuration throughout.
//
// The returned function undoes this call: it puts back what was in force when
// the call was made, unless something has been set since, in which case that
// later setting stays in force until it is restored too, and then what is in
// force is the newest setting not yet restored, or the original if there is none.
// It does nothing when called a second time, may be called in any order relative
// to the restore functions of other calls, and may be called from several
// goroutines.
//
// If c cannot work, Configure returns a nil restore and an *Error of kind
// ErrConfiguration, with the operation "requestid.configure", and the
// configuration stays as it was; the restore functions of earlier calls still
// work. The error names the field that is wrong without repeating what the caller
// gave. A Config cannot work if its Strategy or Correlation is not one of the
// constants, if a header name is not a valid HTTP header name, or if one header
// name serves for both ids. The error is the root package's *Error, and is
// matched with errors.Is(err, schemaflux.ErrConfiguration) or reached with
// errors.As, once the root package is linked into the program, as it is in any
// program that makes a call; this package does not import it.
func Configure(c Config) (restore func(), err error) {
	cfg, err := c.tracking()
	if err != nil {
		return nil, err
	}
	return layers.Set(cfg), nil
}

// misconfigured is the error of a Config that cannot work: the library's own
// words, of kind configuration, converted by the root's hook to the *Error a
// caller gets from every other package. The message names the field and never
// the value.
func misconfigured(message string) error {
	const op = "requestid.configure"
	return bind.ConvertError(op, types.Newf(types.KindConfiguration, op, "%s", message))
}
