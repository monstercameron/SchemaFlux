package op_test

import (
	"fmt"

	"github.com/monstercameron/schemaflux/v2/op"
)

func ExampleRunStages() {
	ctx, _ := exampleContext("cleaned", "verified")
	value, err := op.RunStages(ctx, "source", []op.Stage[string]{{Descriptor: stageDescriptor("clean")}, {Descriptor: stageDescriptor("verify")}})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(value)
	// Output: verified
}
