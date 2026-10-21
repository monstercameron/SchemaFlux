package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Summarize returns the model's summary of a text. The model is scripted here,
// so the example runs offline.
func ExampleSummarize() {
	var model schemafluxtest.Provider
	model.Reply("Revenue grew twelve percent.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	short, err := schemaflux.Summarize(ctx, "The quarterly report shows revenue grew twelve percent while costs stayed flat.")
	fmt.Println(short, err)
	// Output: Revenue grew twelve percent. <nil>
}

// SummarizeDetail also returns the key points and the model's confidence, and
// measures the summary against the target length.
func ExampleSummarizeDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"text":"Revenue grew twelve percent.","key_points":["revenue up 12%","costs flat"],"confidence":0.85}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	sum, err := schemaflux.SummarizeDetail(ctx, "The quarterly report shows revenue grew twelve percent while costs stayed flat.",
		schemaflux.SummarizeOptions{TargetLength: 5, LengthUnit: "words"})
	fmt.Println(sum.Text, sum.Length, sum.KeyPoints, err)
	// Output: Revenue grew twelve percent. 4 [revenue up 12% costs flat] <nil>
}

// Rewrite returns the text rewritten as the options say.
func ExampleRewrite() {
	var model schemafluxtest.Provider
	model.Reply("Dear team, the system failed again.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	formal, err := schemaflux.Rewrite(ctx, "hey team, the thing broke again lol", schemaflux.RewriteOptions{Tone: "formal"})
	fmt.Println(formal, err)
	// Output: Dear team, the system failed again. <nil>
}

// A rewrite that uses a word in AvoidWords is refused, whatever the model says
// about its own work.
func ExampleRewrite_avoidWords() {
	var model schemafluxtest.Provider
	model.Reply("Dear team, we need more synergy.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	text, err := schemaflux.Rewrite(ctx, "hey team, we need to work together", schemaflux.RewriteOptions{AvoidWords: []string{"synergy"}})
	fmt.Printf("%q %v\n", text, errors.Is(err, schemaflux.ErrInvalidAnswer))
	// Output: "" true
}

// RewriteDetail also returns the changes the model says it made and the tone it
// says it achieved.
func ExampleRewriteDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"text":"Dear team, the system failed again.","changes_made":["formal greeting","removed slang"],"tone_achieved":"formal","confidence":0.9}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	r, err := schemaflux.RewriteDetail(ctx, "hey team, the thing broke again lol")
	fmt.Println(r.Text, r.ChangesMade, r.ToneAchieved, err)
	// Output: Dear team, the system failed again. [formal greeting removed slang] formal <nil>
}

// Translate returns the text in the language named.
func ExampleTranslate() {
	var model schemafluxtest.Provider
	model.Reply("Buenos dias, equipo.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	es, err := schemaflux.Translate(ctx, "Good morning, team.", "Spanish")
	fmt.Println(es, err)
	// Output: Buenos dias, equipo. <nil>
}

// TranslateDetail also returns the language the model took the text to be in and
// alternatives for ambiguous phrases.
func ExampleTranslateDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"text":"Buenos dias, equipo.","source_language_detected":"English","confidence":0.95,"alternatives":[{"phrase":"team","alternative":"grupo","context":"informal"}]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	t, err := schemaflux.TranslateDetail(ctx, "Good morning, team.", "Spanish")
	fmt.Println(t.Text, t.SourceLanguageDetected, t.Alternatives[0].Alternative, err)
	// Output: Buenos dias, equipo. English grupo <nil>
}

// Expand returns a longer, more detailed version of the text.
func ExampleExpand() {
	var model schemafluxtest.Provider
	model.Reply("Revenue grew twelve percent over the previous quarter, driven by new subscriptions.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	long, err := schemaflux.Expand(ctx, "Revenue grew.")
	fmt.Println(long, err)
	// Output: Revenue grew twelve percent over the previous quarter, driven by new subscriptions. <nil>
}

// ExpandDetail also returns what the model says it added, and measures how many
// times longer the text became.
func ExampleExpandDetail() {
	var model schemafluxtest.Provider
	model.Reply(`{"text":"Revenue grew twelve percent over the previous quarter, driven by new subscriptions.","added_content":["the size of the growth","its cause"],"confidence":0.8}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	e, err := schemaflux.ExpandDetail(ctx, "Revenue grew.", schemaflux.ExpandOptions{Factor: 3})
	fmt.Printf("%.1f %v %v\n", e.ExpansionRatio, e.AddedContent, err)
	// Output: 6.4 [the size of the growth its cause] <nil>
}

// Compress returns the smaller value with the sizes of both, measured here.
func ExampleCompress() {
	var model schemafluxtest.Provider
	model.Reply(`{"compressed":"Revenue up 12%, costs flat.","preserved_info":["the growth"],"removed_info":["the wording"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Compress(ctx, "Revenue grew twelve percent while costs stayed flat.")
	fmt.Println(res.Compressed, res.OriginalSize, res.CompressedSize, err)
	// Output: Revenue up 12%, costs flat. 52 27 <nil>
}

// Normalize returns the value in its consistent form with the fields that
// actually differ, worked out here from the answer.
func ExampleNormalize() {
	var model schemafluxtest.Provider
	model.Reply(`{"normalized":"Hello world","changes":[{"field":"$","original":"  hELLO   wORLD ","normalized":"Hello world","reason":"case and spacing","confidence":0.9}],"warnings":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Normalize(ctx, "  hELLO   wORLD ", schemaflux.NormalizeOptions{Case: "sentence"})
	fmt.Println(res.Normalized, res.ChangedFields, res.TotalChanges, err)
	// Output: Hello world [$] 1 <nil>
}

// Format writes a value in the form the template describes.
func ExampleFormat() {
	var model schemafluxtest.Provider
	model.Reply("| item | count |\n|---|---|\n| pens | 3 |")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	rows := []struct {
		Item  string `json:"item"`
		Count int    `json:"count"`
	}{{"pens", 3}}
	table, err := schemaflux.Format(ctx, rows, "markdown table")
	fmt.Println(table, err)
	// Output:
	// | item | count |
	// |---|---|
	// | pens | 3 | <nil>
}

// Merge combines several values of one type into one, by the strategy named in
// plain language.
func ExampleMerge() {
	type profile struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"name":"Ada Lovelace","phone":"+1 555 0100"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	merged, err := schemaflux.Merge(ctx, []profile{{"Ada L.", "5550100"}, {"Ada Lovelace", ""}}, "prefer the newest")
	fmt.Println(merged.Name, merged.Phone, err)
	// Output: Ada Lovelace +1 555 0100 <nil>
}
