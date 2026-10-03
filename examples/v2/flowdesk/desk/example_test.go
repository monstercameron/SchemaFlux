package desk_test

import (
	"context"
	"fmt"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/sim"
)

// One message handled from arrival to outcome. The provider is the simulator,
// so the example runs with no key and prints the same thing every time; with a
// client built from a key it is the same code against a model.
//
// The message asks when a late order will arrive, from a sender who is not the
// customer of any ticket the desk has open. The first draft of the reply
// names a day, which the desk's policies rule out, so the audit fails it and
// it is written again: one call and one wait more than the nine calls and four
// waits a plain message takes.
func ExampleDesk_Handle() {
	provider := sim.New(40 * time.Millisecond)
	client := schemaflux.NewClient("").WithProviderInstance(provider)
	ctx := client.Context(context.Background())

	support := desk.New([]desk.Ticket{
		{ID: "T-1001", Customer: "omar@example.com", Summary: "Charged twice for the March invoice on the Team plan"},
		{ID: "T-1002", Customer: "ravi@example.com", Summary: "Parcel with the standing desk frame has not arrived"},
		{ID: "T-1003", Customer: "claire@example.com", Summary: "Cannot sign in after changing the account email address"},
	})
	outcome, err := support.Handle(ctx, desk.Message{
		ID:      "M-08",
		From:    "kofi@example.com",
		Subject: "Late order",
		Body:    "My order D-3310, the monitor arm, was due last Friday. When will it get here? [ref M-08]",
	})
	if err != nil {
		fmt.Println("the desk could not handle the message:", err)
		return
	}

	fmt.Println("status:", outcome.Status)
	fmt.Println("team:", outcome.Team)
	fmt.Println("intent:", outcome.Intent)
	fmt.Println("urgency:", outcome.Urgency)
	fmt.Println("ticket:", outcome.TicketID, "new:", outcome.New)
	fmt.Println("order:", outcome.Details.OrderID)
	fmt.Println("redrafted:", outcome.Redrafted)
	fmt.Println("reply:", outcome.Reply)
	fmt.Println("next:", outcome.NextAction)
	fmt.Println("calls:", provider.Count())
	fmt.Println("waits:", provider.Waits())
	// Output:
	// status: answered
	// team: shipping
	// intent: asking a question
	// urgency: 3
	// ticket: T-M-08 new: true
	// order: D-3310
	// redrafted: true
	// reply: We are sorry order D-3310 is late. We have asked the carrier where the parcel is and will write to you with what they say. Your reference is [ref M-08].
	// next: follow up tomorrow
	// calls: 10
	// waits: 5
}
