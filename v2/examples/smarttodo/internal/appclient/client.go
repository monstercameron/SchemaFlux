package appclient

import (
	"context"
	"errors"
	"strings"
	"sync"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
)

var current struct {
	sync.RWMutex
	client *schemaflux.Client
}

// UseKey replaces the app's client after the provider accepts its configuration.
// Each request captures one client, so changing the key cannot change a call
// already in progress.
func UseKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("an API key is required")
	}
	p, err := provider.New("openai", provider.Config{APIKey: key})
	if err != nil {
		return err
	}
	client := schemaflux.Client{Provider: p, Recorder: &schemaflux.Recorder{}}
	current.Lock()
	current.client = &client
	current.Unlock()
	return nil
}

// UseEnvironment configures the app from the provider's normal environment.
func UseEnvironment() error {
	p, err := provider.New("openai", provider.Config{})
	if err != nil {
		return err
	}
	client := schemaflux.Client{Provider: p, Recorder: &schemaflux.Recorder{}}
	current.Lock()
	current.client = &client
	current.Unlock()
	return nil
}

// Context attaches the app's configured client to a request context. With no
// key yet, operations receive the plain context and return a configuration
// error; the UI can continue to show its key entry screen.
func Context(ctx context.Context) context.Context {
	current.RLock()
	client := current.client
	current.RUnlock()
	if client == nil {
		return ctx
	}
	return schemaflux.WithClient(ctx, *client)
}

// Usage reports only requests and prices the library actually recorded. A
// false Priced means Cost.Total is only a floor and should not be shown as a
// complete session bill.
func Usage() (schemaflux.Record, bool) {
	current.RLock()
	client := current.client
	current.RUnlock()
	if client == nil || client.Recorder == nil {
		return schemaflux.Record{}, false
	}
	return client.Recorder.Total(), true
}

// ProbeKey checks a key with the same provider a subsequent app call will use.
// It does not install the candidate until the request succeeds.
func ProbeKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("an API key is required")
	}
	p, err := provider.New("openai", provider.Config{APIKey: key})
	if err != nil {
		return err
	}
	probe := schemaflux.WithClient(ctx, schemaflux.Client{Provider: p})
	_, err = schemaflux.Generate[string](probe, "Reply with ok.")
	return err
}
