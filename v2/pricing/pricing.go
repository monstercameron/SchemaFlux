package pricing

import (
	"math"
	"strings"
	"sync"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/setting"
	inner "github.com/monstercameron/schemaflux/pricing"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// maxRate is the highest rate Register accepts, in US dollars per million
// tokens. The dearest public model is a small fraction of it. It catches the
// mistake that actually happens, a price given per token or per thousand
// tokens, and an implausibly large one, which would make every cost priced from
// the card too large to be believed.
const maxRate = 1000.0

// A Model is the price of one model, in US dollars per million tokens, as
// providers publish their prices. Give Register the rates of a model the
// library does not price, or rates that differ from the table's.
//
// Every rate is zero or more and no more than $1,000 per million tokens, and a
// model that charges nothing for both Input and Output is not registered: a
// model priced at nothing reports real spending as free, and the library
// reports it unpriced instead.
type Model struct {
	// Provider is the provider that sells the model: "openai", "anthropic". It
	// is required. A model is priced by its name alone, whichever provider a
	// call went to, so the provider here says who set the price and takes no
	// part in the lookup.
	Provider string

	// Name is the model's name as a request names it, such as "gpt-5". It is
	// required. It is looked up without regard to case, and without leading or
	// trailing white space.
	Name string

	// Input is the price of a million input tokens that were not read from the
	// provider's prompt cache. Zero is a price of nothing for them.
	Input float64

	// Output is the price of a million output tokens, reasoning tokens
	// included unless Reasoning says otherwise. Zero is a price of nothing for
	// them.
	Output float64

	// Cached is the price of a million input tokens read from the provider's
	// prompt cache. Zero means the model has no cheaper rate for them, and they
	// are charged at Input: a discount nobody published is not one to assume.
	Cached float64

	// Reasoning is the price of a million output tokens spent reasoning, for a
	// model that charges for them apart from the rest of its output. Zero means
	// they are charged at Output.
	Reasoning float64
}

// longest is the most bytes of a model name or a provider name that Register
// accepts. The longest name a provider publishes is a fraction of it.
const longest = 128

// priceLayers holds, for each model name, the prices that calls to Register have
// put in force for it, so that a restore puts back the price the table held
// before: the built-in one, a price registered by the old root, or none.
var (
	priceLayersMu sync.Mutex
	priceLayers   = map[string]*setting.Layers[*inner.PricingModel]{}
)

// layersOf is the layers of prices for the model called name, made when it is
// first registered.
func layersOf(name string) *setting.Layers[*inner.PricingModel] {
	priceLayersMu.Lock()
	defer priceLayersMu.Unlock()
	layers, ok := priceLayers[name]
	if !ok {
		layers = &setting.Layers[*inner.PricingModel]{
			Apply: func(card *inner.PricingModel) { inner.ReplacePricingModel(name, card) },
			Base: func() *inner.PricingModel {
				if card, ok := inner.PricingModelNamed(name); ok {
					return &card
				}
				return nil
			},
		}
		priceLayers[name] = layers
	}
	return layers
}

// Register adds the price of a model to the process's price table, or replaces
// the one the table has. A call on that model is priced at it from now on,
// whichever client makes the call.
//
// It changes process state.
//
// The returned function undoes this call: it puts back what was in force when
// the call was made, unless something has been set since, in which case that
// later setting stays in force until it is restored too, and then what is in
// force is the newest setting not yet restored, or the original if there is none.
// It does nothing when called a second time, may be called in any order relative
// to the restore functions of other calls, and may be called from several
// goroutines. What is put back for the name is the price the table held, the
// built-in one or none. A test that registers a price still gives it a model name
// of its own when it runs in parallel, because a price is looked up by the name
// alone.
//
// A Model with no name or no provider, a name or a provider of more than 128
// bytes, a rate that is not a number, is negative or is above $1,000 per million
// tokens, and a Model that charges nothing for both Input and Output, are refused
// with an *Error of kind ErrConfiguration, and the table is not changed. The
// restore is then nil.
func Register(m Model) (restore func(), err error) {
	name := strings.TrimSpace(strings.ToLower(m.Name))
	refuse := func(message string) (func(), error) {
		return nil, &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Op: "pricing.register", Message: message}
	}
	if name == "" {
		return refuse("the model has no name")
	}
	if len(name) > longest {
		return refuse("the model name is longer than 128 bytes")
	}
	provider := strings.TrimSpace(m.Provider)
	if provider == "" {
		return refuse("model " + quote(name) + " has no provider")
	}
	if len(provider) > longest {
		return refuse("the provider name of model " + quote(name) + " is longer than 128 bytes")
	}
	for _, rate := range []struct {
		article, name string
		value         float64
	}{{"an", "input", m.Input}, {"an", "output", m.Output}, {"a", "cached", m.Cached}, {"a", "reasoning", m.Reasoning}} {
		switch {
		case math.IsNaN(rate.value):
			return refuse("model " + quote(name) + " has " + rate.article + " " + rate.name + " rate that is not a number")
		case rate.value < 0:
			return refuse("model " + quote(name) + " has a negative " + rate.name + " rate")
		case rate.value > maxRate:
			return refuse("model " + quote(name) + " has " + rate.article + " " + rate.name + " rate above $1,000 per million tokens; rates are in US dollars per million tokens")
		}
	}
	card := inner.PricingModel{
		Provider:                provider,
		Model:                   name,
		PricePerPromptToken:     m.Input / 1000,
		PricePerCompletionToken: m.Output / 1000,
		PriceCachedToken:        m.Cached / 1000,
		PriceReasoningToken:     m.Reasoning / 1000,
		Currency:                "USD",
	}
	// The rates are tested as the table keeps them, per thousand tokens: one so
	// small that it divides to nothing is a price of nothing too.
	if card.PricePerPromptToken == 0 && card.PricePerCompletionToken == 0 {
		return refuse("model " + quote(name) + " charges nothing for input and output, which would report real spending as free; leave it unregistered and it is reported unpriced")
	}
	return layersOf(name).Set(&card), nil
}

// Models returns the names of the models the table has a price for, in lower
// case and sorted, built-in and registered alike. A model that is absent is
// reported unpriced and not free, though a dated snapshot of one that is listed
// is priced as the one it is a snapshot of.
func Models() []string {
	return inner.RegisteredPricingModels()
}

// Price returns what u cost on model, at the table's price for it, as the
// Cost a Record carries. provider is the provider the call went to, and only
// matters for one that is not billed per token.
//
// Every token is priced once: Usage.Cached is part of Usage.Input and is
// charged at the model's cached rate where it has one, and Usage.Reasoning is
// part of Usage.Output and is charged at the model's reasoning rate where it
// has one. A count above its total is held at the total, and a negative count is
// read as zero.
//
// A model the table has no price for, and a provider that is not billed per
// token, give a Cost that is not Priced, with a Total of zero. That zero is not a
// price: it means the cost is not known, and a figure summed over it is a floor.
// The Currency is "USD", as in a Record's Cost. A count of any size is held: a Total left at zero is the sum of
// Input and Output, held at the largest count there is where it would pass it, and
// a negative count is read as zero. Price does not change the ledger; the library
// records its own calls.
func Price(u schemaflux.Usage, model, provider string) schemaflux.Cost {
	usage := bind.UsageToInternal(u)
	return costOf(inner.CalculateCost(&usage, model, provider))
}

// quote is s in quotation marks, cut to a length a message can carry.
func quote(s string) string {
	const longest = 80
	if runes := []rune(s); len(runes) > longest {
		s = string(runes[:longest]) + "..."
	}
	return `"` + s + `"`
}
