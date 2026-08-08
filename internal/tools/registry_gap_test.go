package tools

import (
	"context"
	"testing"
)

// CreateToolHandlerWithRegistry reports a marshal failure rather than
// returning a truncated or corrupt JSON body -- a tool whose Result carries
// data that cannot be marshalled (a channel, say) is a bug in that tool, and
// the handler has to surface it rather than hide it behind a JSON error the
// caller cannot see.
func TestCreateToolHandlerReportsAMarshalFailure(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&Tool{
		Name: "unmarshalable",
		Execute: func(ctx context.Context, params map[string]any) (Result, error) {
			return Result{Success: true, Data: make(chan int)}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	handler := CreateToolHandlerWithRegistry(registry)
	_, err := handler(context.Background(), "unmarshalable", map[string]any{})
	if err == nil {
		t.Fatal("a Result whose Data cannot be marshalled to JSON must fail the handler")
	}
}
