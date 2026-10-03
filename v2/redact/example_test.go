package redact_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/redact"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleValue() {
	type customer struct {
		Name     string
		Plan     string
		Password string
		Note     string
	}
	safe, err := redact.Value(customer{Name: "Ada", Plan: "gold", Password: "hunter2", Note: "write to ada@example.com"})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%+v\n", safe)
	// Output: {Name:*** Plan:gold Password:*** Note:write to ***}
}

func ExampleText() {
	var model schemafluxtest.Provider
	model.Reply(`{"spans":[{"text":"Ada Lovelace","category":"name"}]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := redact.Text(ctx, "Ada Lovelace wrote ada@example.com", redact.TextOptions{ShowLast: 4})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Text)
	fmt.Println(res.Categories["name"], res.Categories["email"], res.DetectorSpans)
	// Output:
	// ********lace wrote ***********.com
	// 1 1 1
}
