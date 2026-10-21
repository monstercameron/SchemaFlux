package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Complete continues a text and returns the whole of it. The model is scripted
// here, so the example runs offline.
func ExampleComplete() {
	var model schemafluxtest.Provider
	model.Reply("jumps over the lazy dog")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Complete(ctx, "The quick brown fox")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Text)
	fmt.Println(res.Length, "characters added")
	// Output:
	// The quick brown fox jumps over the lazy dog
	// 24 characters added
}

// CompleteField continues one string field of a struct, using the others as
// context, and returns a copy: the struct that was passed is not changed.
func ExampleCompleteField() {
	type Post struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	var model schemafluxtest.Provider
	model.Reply("how doctors read scans.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	post := Post{Title: "AI in Healthcare", Body: "Artificial intelligence is transforming"}
	res, err := schemaflux.CompleteField(ctx, post, "Body", schemaflux.CompleteOptions{MaxLength: 400})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Data.Body)
	fmt.Println(post.Body)
	// Output:
	// Artificial intelligence is transforming how doctors read scans.
	// Artificial intelligence is transforming
}

// Suggest proposes values for an input and returns at most TopN of them.
func ExampleSuggest() {
	var model schemafluxtest.Provider
	model.Reply(`["Reply to the customer","Offer a refund","Escalate"]`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	next, err := schemaflux.Suggest[string](ctx, "My invoice is wrong", schemaflux.SuggestOptions{TopN: 3, Domain: "billing"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(len(next), next[0])
	// Output: 3 Reply to the customer
}
