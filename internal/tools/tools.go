// Package tools provides LLM tool primitives for SchemaFlux.
// Tools are callable functions that LLMs can use to interact with external systems,
// perform calculations, and access data sources.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Tool represents a callable tool that can be used by LLMs.
type Tool struct {
	// Name is the unique identifier for the tool
	Name string `json:"name"`

	// Description explains what the tool does (shown to LLM)
	Description string `json:"description"`

	// Category groups related tools together
	Category string `json:"category"`

	// Parameters defines the JSON Schema for tool inputs
	Parameters map[string]any `json:"parameters"`

	// Execute is the function that runs the tool
	Execute func(ctx context.Context, params map[string]any) (Result, error) `json:"-"`

	// RequiresAuth indicates if the tool needs API keys or credentials
	RequiresAuth bool `json:"requires_auth"`

	// IsStub indicates if this is a stub implementation
	IsStub bool `json:"is_stub"`
}

// Result represents the output of a tool execution.
type Result struct {
	// Success indicates if the tool executed successfully
	Success bool `json:"success"`

	// Data contains the tool output
	Data any `json:"data,omitempty"`

	// Error contains error message if Success is false
	Error string `json:"error,omitempty"`

	// Metadata contains additional information about the execution
	Metadata map[string]any `json:"metadata,omitempty"`
}

// NewResult creates a successful result.
func NewResult(data any) Result {
	return Result{
		Success: true,
		Data:    data,
	}
}

// NewResultWithMeta creates a successful result with metadata.
func NewResultWithMeta(data any, meta map[string]any) Result {
	return Result{
		Success:  true,
		Data:     data,
		Metadata: meta,
	}
}

// ErrorResult creates a failed result from an error.
func ErrorResult(err error) Result {
	return Result{
		Success: false,
		Error:   err.Error(),
	}
}

// ErrorResultFromError is an alias for ErrorResult for backward compatibility.
func ErrorResultFromError(err error) Result {
	return ErrorResult(err)
}

// StubResult creates a result indicating the tool is stubbed.
func StubResult(message string) Result {
	return Result{
		Success: true,
		Data:    message,
		Metadata: map[string]any{
			"stubbed": true,
		},
	}
}

// Registry manages available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]*Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool *Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if tool.Name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q already registered", tool.Name)
	}

	r.tools[tool.Name] = tool
	return nil
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tools.
func (r *Registry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	return tools
}

// ListByCategory returns tools in a specific category.
func (r *Registry) ListByCategory(category string) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var tools []*Tool
	for _, t := range r.tools {
		if t.Category == category {
			tools = append(tools, t)
		}
	}
	return tools
}

// Execute runs a tool by name with the given parameters.
func (r *Registry) Execute(ctx context.Context, name string, params map[string]any) (Result, error) {
	tool, ok := r.Get(name)
	if !ok {
		return Result{}, fmt.Errorf("tool %q not found", name)
	}

	if tool.Execute == nil {
		return Result{}, fmt.Errorf("tool %q has no execute function", name)
	}

	return tool.Execute(ctx, params)
}

// ToOpenAIFormat converts tools to OpenAI function calling format.
func (r *Registry) ToOpenAIFormat() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var functions []map[string]any
	for _, t := range r.tools {
		fn := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		}
		functions = append(functions, fn)
	}
	return functions
}

// ToJSON serializes the registry tools to JSON.
func (r *Registry) ToJSON() ([]byte, error) {
	return json.MarshalIndent(r.List(), "", "  ")
}

// DefaultRegistry is the global tool registry.
var DefaultRegistry = NewRegistry()

// Register adds a tool to the default registry.
func Register(tool *Tool) error {
	return DefaultRegistry.Register(tool)
}

// mustRegister registers a built-in tool and panics if it cannot. The package's
// init functions used to discard Register's error, so a duplicate name left
// whichever tool happened to register first in place and the second silently
// vanished — a caller invoking that name got the other tool's behaviour. A
// collision between two built-ins is a bug in this package, and it should stop
// the program at init rather than surface as a wrong answer later.
func mustRegister(tool *Tool) {
	if err := Register(tool); err != nil {
		panic(fmt.Sprintf("tools: registering built-in tool: %v", err))
	}
}

// Unregister removes a tool from the registry. It reports whether a tool was
// removed.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return false
	}
	delete(r.tools, name)
	return true
}

// Unregister removes a tool from the default registry.
func Unregister(name string) bool {
	return DefaultRegistry.Unregister(name)
}

// Get retrieves a tool from the default registry.
func Get(name string) (*Tool, bool) {
	return DefaultRegistry.Get(name)
}

// Execute runs a tool from the default registry.
func Execute(ctx context.Context, name string, params map[string]any) (Result, error) {
	return DefaultRegistry.Execute(ctx, name, params)
}

// List returns all tools from the default registry.
func List() []*Tool {
	return DefaultRegistry.List()
}

// Categories for organizing tools.
const (
	CategoryComputation = "computation"
	CategoryHTTP        = "http"
	CategoryFile        = "file"
	CategoryDatabase    = "database"
	CategoryCache       = "cache"
	CategoryTime        = "time"
	CategorySecurity    = "security"
	CategoryData        = "data"
	CategoryFinance     = "finance"
	CategoryBusiness    = "business"
	CategoryCreative    = "creative"
	CategoryVision      = "vision"
	CategoryAudio       = "audio"
	CategoryAI          = "ai"
)

// ParameterSchema helps build JSON Schema for tool parameters.
type ParameterSchema struct {
	Type        string                     `json:"type"`
	Description string                     `json:"description,omitempty"`
	Properties  map[string]ParameterSchema `json:"properties,omitempty"`
	Required    []string                   `json:"required,omitempty"`
	Enum        []string                   `json:"enum,omitempty"`
	Default     any                        `json:"default,omitempty"`
	Minimum     *float64                   `json:"minimum,omitempty"`
	Maximum     *float64                   `json:"maximum,omitempty"`
}

// ObjectSchema creates an object parameter schema.
func ObjectSchema(properties map[string]ParameterSchema, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

// SimpleObjectSchema creates an object schema using variadic key-value pairs.
// Arguments should be in the format: name, type, description, required, name, type, description, required, ...
func SimpleObjectSchema(args ...any) map[string]any {
	properties := make(map[string]ParameterSchema)
	var required []string

	for i := 0; i+3 < len(args); i += 4 {
		name := fmt.Sprint(args[i])
		paramType := fmt.Sprint(args[i+1])
		description := fmt.Sprint(args[i+2])
		isRequired := false
		if r, ok := args[i+3].(bool); ok {
			isRequired = r
		}

		properties[name] = ParameterSchema{
			Type:        paramType,
			Description: description,
		}

		if isRequired {
			required = append(required, name)
		}
	}

	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

// StringParam creates a string parameter schema.
func StringParam(description string) ParameterSchema {
	return ParameterSchema{
		Type:        "string",
		Description: description,
	}
}

// NumberParam creates a number parameter schema.
func NumberParam(description string) ParameterSchema {
	return ParameterSchema{
		Type:        "number",
		Description: description,
	}
}

// BoolParam creates a boolean parameter schema.
func BoolParam(description string) ParameterSchema {
	return ParameterSchema{
		Type:        "boolean",
		Description: description,
	}
}

// EnumParam creates an enum parameter schema.
func EnumParam(description string, values []string) ParameterSchema {
	return ParameterSchema{
		Type:        "string",
		Description: description,
		Enum:        values,
	}
}
