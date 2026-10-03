package op_test

import (
	"fmt"

	"github.com/monstercameron/schemaflux/v2/op"
)

func ExampleRunEach() {
	items, err := op.RunEach(clientOn(echoing()), invoiceDescriptor(), []string{"A-1", "B-2"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, item := range items {
		value, err := item.Value()
		if err == nil {
			fmt.Println(value.Number)
		}
	}
	// Output:
	// A-1
	// B-2
}
