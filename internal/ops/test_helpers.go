package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

type Person struct {
	Name string
	Age  int
}

// mockLLMResponse answers with a body shaped like Person, the target these
// tests extract into. It used to return `{"mock": "response"}`, which shares no
// field with any target -- so every test using it was asserting that an answer
// about something else parses successfully, which is the fail-open behaviour
// ParseJSONStrict now rejects.
func mockLLMResponse(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
	return `{"Name": "Mock Person", "Age": 30}`, nil
}

func setupMockClient() {
	setLLMCaller(mockLLMResponse)
}
