package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// streamingClient is a context whose client is a scripted model that streams its
// answer in pieces, and a recorder that keeps the Record of each call. The model
// is scripted here, so the examples run offline.
func streamingClient(answer string) (context.Context, *schemaflux.Recorder) {
	var model schemafluxtest.Provider
	model.Reply(answer)
	recorder := &schemaflux.Recorder{}
	return schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Recorder: recorder}), recorder
}

// StreamSummarize delivers a summary as the model writes it. Ranging over All
// gives each piece; the Record of the call is added when the stream ends, so it
// is in the recorder when the loop is over.
func ExampleStreamSummarize() {
	ctx, recorder := streamingClient("Revenue grew twelve percent.")

	stream, err := schemaflux.StreamSummarize(ctx, "The quarterly report shows revenue grew twelve percent while costs stayed flat.")
	if err != nil {
		fmt.Println(err)
		return
	}
	var pieces []string
	for piece, err := range stream.All() {
		if err != nil {
			fmt.Println(err)
			return
		}
		pieces = append(pieces, piece)
	}
	fmt.Printf("%q\n", pieces)
	fmt.Println(recorder.Records()[0].Op, stream.Err())
	// Output:
	// ["Reve" "nue " "grew" " twe" "lve " "perc" "ent."]
	// streamsummarize <nil>
}

// StreamRewrite rewrites a text as it arrives. Text waits for the whole answer.
func ExampleStreamRewrite() {
	ctx, _ := streamingClient("Dear team, the figures are attached.")

	stream, err := schemaflux.StreamRewrite(ctx, "hey, figures attached", schemaflux.RewriteOptions{Tone: "formal"})
	if err != nil {
		fmt.Println(err)
		return
	}
	text, err := stream.Text()
	fmt.Println(text, err)
	// Output: Dear team, the figures are attached. <nil>
}

// StreamTranslate translates a text as it arrives, into the language named. The
// options are those of Translate.
func ExampleStreamTranslate() {
	ctx, _ := streamingClient("Los ingresos crecieron.")

	stream, err := schemaflux.StreamTranslate(ctx, "Revenue grew.", "Spanish", schemaflux.TranslateOptions{Dialect: "Mexican"})
	if err != nil {
		fmt.Println(err)
		return
	}
	var text string
	for piece, err := range stream.All() {
		if err != nil {
			fmt.Println(err)
			return
		}
		text += piece
	}
	fmt.Println(text)
	// Output: Los ingresos crecieron.
}

// StreamExpand expands a text as it arrives.
func ExampleStreamExpand() {
	ctx, _ := streamingClient("Revenue grew twelve percent over the year, while costs stayed flat.")

	stream, err := schemaflux.StreamExpand(ctx, "Revenue grew.", schemaflux.ExpandOptions{Factor: 3})
	if err != nil {
		fmt.Println(err)
		return
	}
	text, err := stream.Text()
	fmt.Println(text, err)
	// Output: Revenue grew twelve percent over the year, while costs stayed flat. <nil>
}

// A TextStream reports every error as an *Error: here the provider fails, and the
// error is of kind ErrRateLimited whichever method reads it.
func ExampleTextStream() {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Message: "slow down"})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	stream, err := schemaflux.StreamSummarize(ctx, "A long text.")
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = stream.Text()
	var e *schemaflux.Error
	fmt.Println(errors.As(err, &e), errors.Is(err, schemaflux.ErrRateLimited), stream.Ended(), stream.Err() == err)
	// Output: true true true true
}
