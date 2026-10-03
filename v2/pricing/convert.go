package pricing

import (
	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// costOf is the Cost for the internal cost the price table gave, converted as
// the root converts the cost of a call into its Record, so that Price and a
// Record's Cost agree for the same usage, the currency included. No cost is a Cost
// that is not Priced.
func costOf(c *types.CostInfo) schemaflux.Cost {
	if c == nil {
		return schemaflux.Cost{Currency: "USD"}
	}
	return bind.CostFromInternal[schemaflux.Cost](*c)
}
