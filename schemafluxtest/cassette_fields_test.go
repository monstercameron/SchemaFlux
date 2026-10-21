package schemafluxtest_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// These tests keep the cassette in step with the root's Request and Response.
// They read the two types by reflection, so a field the root gains is found
// here and not by a reader of a failed replay: either the cassette stores it, or
// it is listed below with the reason it is not.

// cassetteRequestFields maps each field of schemaflux.Request to the field of
// CassetteRequest that holds it.
var cassetteRequestFields = map[string]string{
	"Model": "Model", "System": "System", "User": "User", "Temperature": "Temperature", "MaxTokens": "MaxTokens",
	"Format": "Format", "Schema": "Schema", "SchemaName": "SchemaName", "Effort": "Effort",
	"EffortPreferred": "EffortPreferred", "WebSearch": "WebSearch", "Decision": "Decision",
	"Tools": "Tools", "Messages": "Messages",
}

// cassetteRequestNotStored are the fields of Request a cassette does not keep,
// and why.
var cassetteRequestNotStored = map[string]string{
	"CacheKey": "a hint to the provider's cache, derived from the prompts, which do not change the answer",
}

// cassetteResponseNotStored are the fields of Response a cassette does not keep.
var cassetteResponseNotStored = map[string]string{
	"Timing": "measures the HTTP call of the recording; a replay makes none and would claim it did",
}

// cassetteResponseStoredAs maps the fields of Response that a cassette keeps under
// another name to that name: the calls of a reply are CassetteToolCall values.
var cassetteResponseStoredAs = map[string]string{"ToolCalls": "ToolCalls"}

func fieldNames(t reflect.Type) []string {
	var names []string
	for i := range t.NumField() {
		names = append(names, t.Field(i).Name)
	}
	return names
}

func TestEveryFieldOfARequestIsStoredOrListedAsNotStored(t *testing.T) {
	t.Parallel()
	for _, name := range fieldNames(reflect.TypeFor[schemaflux.Request]()) {
		_, stored := cassetteRequestFields[name]
		_, listed := cassetteRequestNotStored[name]
		switch {
		case stored && listed:
			t.Errorf("Request.%s is both stored and listed as not stored", name)
		case !stored && !listed:
			t.Errorf("Request has gained the field %s: store it in CassetteRequest (and in the file, and in the match) or list it as not stored, with the reason", name)
		}
	}
	cassette := reflect.TypeFor[schemafluxtest.CassetteRequest]()
	covered := map[string]bool{}
	for from, to := range cassetteRequestFields {
		if _, ok := reflect.TypeFor[schemaflux.Request]().FieldByName(from); !ok {
			t.Errorf("the list names Request.%s, which does not exist", from)
		}
		if _, ok := cassette.FieldByName(to); !ok {
			t.Errorf("the list maps Request.%s to CassetteRequest.%s, which does not exist", from, to)
		}
		covered[to] = true
	}
	for name := range cassetteRequestNotStored {
		if _, ok := reflect.TypeFor[schemaflux.Request]().FieldByName(name); !ok {
			t.Errorf("the list of fields not stored names Request.%s, which does not exist", name)
		}
	}
	for _, name := range fieldNames(cassette) {
		if !covered[name] {
			t.Errorf("CassetteRequest.%s is filled from no field of Request", name)
		}
	}
}

func TestEveryFieldOfAResponseIsStoredOrListedAsNotStored(t *testing.T) {
	t.Parallel()
	response := reflect.TypeFor[schemaflux.Response]()
	cassette := reflect.TypeFor[schemafluxtest.CassetteResponse]()
	for _, name := range fieldNames(response) {
		_, listed := cassetteResponseNotStored[name]
		_, stored := cassette.FieldByName(name)
		if to, renamed := cassetteResponseStoredAs[name]; renamed {
			_, stored = cassette.FieldByName(to)
		}
		switch {
		case stored && listed:
			t.Errorf("Response.%s is both stored and listed as not stored", name)
		case !stored && !listed:
			t.Errorf("Response has gained the field %s: store it in CassetteResponse (and in the file) or list it as not stored, with the reason", name)
		}
	}
	for name := range cassetteResponseNotStored {
		if _, ok := response.FieldByName(name); !ok {
			t.Errorf("the list of fields not stored names Response.%s, which does not exist", name)
		}
	}
	for _, name := range fieldNames(cassette) {
		if _, ok := response.FieldByName(name); !ok {
			t.Errorf("CassetteResponse.%s is filled from no field of Response", name)
		}
	}
}

// cassetteFill sets every exported field of v, however deep, to a value that is
// not the zero value and differs from the others, so that a field a cassette
// drops is a field that does not come back.
type cassetteFill struct{ n int }

func (f *cassetteFill) fill(v reflect.Value) {
	f.n++
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("s%d", f.n))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(f.n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		// A decision kind is 1, 2 or 3: the three the library has.
		v.SetUint(uint64(f.n%3 + 1))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(f.n) + 0.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := range 2 {
			f.fill(v.Index(i))
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		f.fill(v.Elem())
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(reflect.ValueOf(fmt.Sprintf("key%d", f.n)), reflect.ValueOf(fmt.Sprintf("v%d", f.n)))
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				f.fill(v.Field(i))
			}
		}
	default:
		panic("the filler does not know " + v.Kind().String())
	}
}

func filledRequest() schemaflux.Request {
	var r schemaflux.Request
	(&cassetteFill{}).fill(reflect.ValueOf(&r).Elem())
	return r
}

func TestEveryFieldOfAResponseSurvivesRecordingSavingAndReplaying(t *testing.T) {
	t.Parallel()
	var sent schemaflux.Response
	(&cassetteFill{}).fill(reflect.ValueOf(&sent).Elem())
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return sent, nil })
	})
	request := filledRequest()
	if _, err := tape.Complete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)
	got, err := player.Complete(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}

	want := sent
	for name := range cassetteResponseNotStored {
		reflect.ValueOf(&want).Elem().FieldByName(name).SetZero()
	}
	if !reflect.DeepEqual(got, want) {
		for _, name := range fieldNames(reflect.TypeFor[schemaflux.Response]()) {
			g, w := reflect.ValueOf(got).FieldByName(name).Interface(), reflect.ValueOf(want).FieldByName(name).Interface()
			if !reflect.DeepEqual(g, w) {
				t.Errorf("Response.%s came back as %+v, sent as %+v", name, g, w)
			}
		}
	}
	// The same through memory, with no file.
	again, err := schemafluxtest.ReplayFrom(tape.Recorded()...).Complete(t.Context(), request)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("through memory: %+v, %v", again, err)
	}
}

func TestEveryFieldOfARequestIsKeptByTheTape(t *testing.T) {
	t.Parallel()
	request := filledRequest()
	var inner schemafluxtest.Provider
	tape := schemafluxtest.Record(&inner, t.TempDir())
	if _, err := tape.Complete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	kept := reflect.ValueOf(tape.Recorded()[0].Request)
	sent := reflect.ValueOf(request)
	for from, to := range cassetteRequestFields {
		got, want := kept.FieldByName(to).Interface(), sent.FieldByName(from).Interface()
		if from == "Schema" {
			if text, _ := got.(string); !strings.HasPrefix(text, "sha256:") || len(text) != len("sha256:")+64 {
				t.Errorf("the schema is kept as %q, want a sha256 digest", got)
			}
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Request.%s is kept as %+v, was %+v", from, got, want)
		}
	}
}

// cassettePerturb changes v to a value it was not.
func cassettePerturb(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "~")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint()%3 + 1)
		if v.Uint() == 0 {
			v.SetUint(1)
		}
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Map:
		v.SetMapIndex(reflect.ValueOf("another"), reflect.ValueOf("member"))
	case reflect.Slice:
		v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
	case reflect.Pointer:
		cassettePerturb(v.Elem())
	case reflect.Struct:
		cassettePerturb(v.Field(0))
	}
}

func TestAReplayDoesNotAnswerARequestThatDiffersInAnyFieldItStores(t *testing.T) {
	t.Parallel()
	recordedRequest := filledRequest()
	var inner schemafluxtest.Provider
	inner.Reply("answer")
	tape := schemafluxtest.Record(&inner, t.TempDir())
	if _, err := tape.Complete(t.Context(), recordedRequest); err != nil {
		t.Fatal(err)
	}
	recorded := tape.Recorded()

	// The request itself is answered.
	if _, err := schemafluxtest.ReplayFrom(recorded...).Complete(t.Context(), filledRequest()); err != nil {
		t.Fatalf("a request identical to the recorded one was refused: %v", err)
	}
	for _, name := range fieldNames(reflect.TypeFor[schemaflux.Request]()) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			varied := filledRequest()
			cassettePerturb(reflect.ValueOf(&varied).Elem().FieldByName(name))
			_, err := schemafluxtest.ReplayFrom(recorded...).Complete(t.Context(), varied)
			if reason, notStored := cassetteRequestNotStored[name]; notStored {
				if err != nil {
					t.Errorf("Request.%s is not stored (%s), and a request that differs only in it was refused: %v", name, reason, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a request that differs in Request.%s was answered", name)
			}
			if cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), "differs in "+name) {
				t.Errorf("the refusal does not name %s: %v", name, err)
			}
		})
	}
}

func TestAReplayDoesNotAnswerADecisionThatDiffersInAnyFieldOfItsQuestions(t *testing.T) {
	t.Parallel()
	request := filledRequest()
	var inner schemafluxtest.Provider
	tape := schemafluxtest.Record(&inner, t.TempDir())
	if _, err := tape.Complete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	recorded := tape.Recorded()

	decision := reflect.TypeFor[schemaflux.Decision]()
	question := reflect.TypeFor[schemaflux.DecisionQuestion]()
	type variation struct {
		name string
		edit func(*schemaflux.Decision)
	}
	var variations []variation
	for _, name := range fieldNames(decision) {
		if name == "Questions" {
			continue
		}
		variations = append(variations, variation{"Decision." + name, func(d *schemaflux.Decision) {
			cassettePerturb(reflect.ValueOf(d).Elem().FieldByName(name))
		}})
	}
	for _, name := range fieldNames(question) {
		variations = append(variations, variation{"DecisionQuestion." + name, func(d *schemaflux.Decision) {
			cassettePerturb(reflect.ValueOf(&d.Questions[1]).Elem().FieldByName(name))
		}})
	}
	variations = append(variations,
		variation{"one question fewer", func(d *schemaflux.Decision) { d.Questions = d.Questions[:1] }},
		variation{"no decision", nil},
	)
	for _, v := range variations {
		varied := filledRequest()
		if v.edit == nil {
			varied.Decision = nil
		} else {
			v.edit(varied.Decision)
		}
		if _, err := schemafluxtest.ReplayFrom(recorded...).Complete(t.Context(), varied); err == nil {
			t.Errorf("a request that differs in %s was answered", v.name)
		}
	}
	if len(variations) < 10 {
		t.Errorf("only %d variations of a decision were tried", len(variations))
	}
}

func TestASchemaIsMatchedByItsContentAndNotItsIdentity(t *testing.T) {
	t.Parallel()
	schema := func() map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "integer"}}}
	}
	record := func(s map[string]any) []schemafluxtest.Cassette {
		var inner schemafluxtest.Provider
		tape := schemafluxtest.Record(&inner, t.TempDir())
		request := cassetteRequest("x")
		request.Schema = s
		if _, err := tape.Complete(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		return tape.Recorded()
	}
	recorded := record(schema())
	if got := recorded[0].Request.Schema; !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("schema digest = %q", got)
	}
	changed := schema()
	changed["properties"].(map[string]any)["b"] = map[string]any{"type": "number"}
	cases := []struct {
		name   string
		schema map[string]any
		match  bool
	}{
		{"a new map of the same content", schema(), true},
		{"a changed property", changed, false},
		{"no schema", nil, false},
		{"an empty schema", map[string]any{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := cassetteRequest("x")
			request.Schema = tc.schema
			_, err := schemafluxtest.ReplayFrom(recorded...).Complete(t.Context(), request)
			if (err == nil) != tc.match {
				t.Errorf("matched = %v, want %v (%v)", err == nil, tc.match, err)
			}
		})
	}
	if none := record(nil)[0].Request.Schema; none != "" {
		t.Errorf("a request with no schema kept the digest %q", none)
	}
	if empty := record(map[string]any{})[0].Request.Schema; empty != "" {
		t.Errorf("a request with an empty schema kept the digest %q", empty)
	}
}
