package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func TestExtractThroughOpenAIValidatesAndSendsItsStrictSchema(t *testing.T) {
	var sent []byte
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.openai.com" {
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
		}
		var err error
		sent, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		const reply = `{"model":"gpt-6-luna","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"name\":\"Ada\"}"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(reply)), Request: req}, nil
	})
	model, err := New("openai", Config{APIKey: "test", HTTPClient: &http.Client{Transport: transport}, Retries: -1})
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model, Model: "gpt-6-luna", Retries: -1})
	type person struct {
		Name string `json:"name"`
	}
	got, err := schemaflux.Extract[person](ctx, "Ada")
	if err != nil || got.Name != "Ada" {
		t.Fatalf("Extract = %+v, %v", got, err)
	}
	var wire struct {
		Text struct {
			Format struct {
				Strict bool           `json:"strict"`
				Schema map[string]any `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(sent, &wire); err != nil {
		t.Fatal(err)
	}
	if !wire.Text.Format.Strict || wire.Text.Format.Schema["type"] != "object" {
		t.Fatalf("Extract did not send a strict object schema: %s", sent)
	}
}
