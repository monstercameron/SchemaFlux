package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

type Person struct {
	Name string
	Age  int
}

func mockLLMResponse(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
	return `{"mock": "response"}`, nil
}

func setupMockClient() {
	setLLMCaller(mockLLMResponse)
}
