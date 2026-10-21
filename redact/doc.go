// Package redact removes sensitive data from a value or from a text. It has two
// functions, and they are not interchangeable.
//
// Value works on a Go value, in Go, with patterns, field names and struct tags.
// It asks no model, takes no context, adds no Record and is held to no floor of a
// client's data policy: it runs where it is called and nothing leaves the process.
//
//	safe, err := redact.Value(customer)
//
// Text works on free text, with a model that names what is sensitive and with
// pattern detectors written in Go that run beside it. It sends the text to the
// client's provider, takes a context, adds exactly one Record named
// "redact.text" and is held to the client's floor. Use it for what Value cannot
// know the shape of: a ticket, a note, a transcript.
//
//	res, err := redact.Text(ctx, ticket)
//
// Neither is a guarantee. Value finds what its categories describe and the names
// of the fields it knows, and Text finds what a model reports, which varies from
// call to call. Where the shape of the data is known, tag the fields and use
// Value.
package redact
