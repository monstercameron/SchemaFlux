// Package op is the door for a caller's own operation. The operations of the root
// package ask a model one fixed question each; this package asks the question the
// caller writes down. A Descriptor holds it: the function that renders the
// request, the contract the answer has to satisfy, how the operation batches, and
// its name and version. The functions of this package run one, describe a run
// before it is paid for, and list the operations the library has.
//
//	var invoice = op.Must(op.Descriptor[string, Invoice]{
//		ID: op.ID{Name: "extract-invoice", Version: "1"},
//		BuildPrompt: func(text string, _ op.Options) (system, user string) {
//			return "Read the invoice. Return a JSON object with number and total.", text
//		},
//		Contract: op.Contract[Invoice]{
//			Decode: func(body string) (Invoice, error) { ... },
//		},
//	})
//
//	one, err := op.Run(ctx, invoice, emailBody)               // one input
//	all, err := op.RunMany(ctx, invoice, emails)              // many, planned
//	plan, err := op.Preflight(ctx, invoice, emails)           // what that would cost
//
// # Running
//
// Run sends one request for one input and repairs an answer that is refused,
// asking the model again with what was wrong. RunBatch sends one request for many
// items, which a descriptor allows by declaring its Batch. RunMany takes many
// items and chooses between the two: it plans the run with the planner Preflight
// returns the plan of, and carries it out. All three are named in a Record by the
// descriptor's own name, add one Record for the call, and are held to a client's
// floor by the level the descriptor delivers.
//
// # Planning
//
// Preflight makes no request. It prices what the descriptor renders for each item
// or chunk and says how many requests the run takes, for the client on its
// context. A plan that cannot be carried out says so in the plan.
//
// # Safety of the descriptor's functions
//
// The functions of a descriptor are the caller's and are called by the library
// from its own goroutines. They have to be pure: they return what they were asked
// for and do nothing else. One that panics ends the call with an error of kind
// ErrInternal and never the process, and the panic's value is not kept.
//
// # The catalogue
//
// Catalog lists every operation of the library by the name its Record carries,
// with its category and how firmly its contract holds.
package op
