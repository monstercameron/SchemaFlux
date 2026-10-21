package requesttracking

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
)

var defaultsPinned = Config{
	Enabled:               true,
	RequestIDStrategy:     IDStrategyAuto,
	CorrelationIDStrategy: CorrelationStrategyInherit,
	RequestIDHeader:       "X-Request-ID",
	CorrelationIDHeader:   "X-Correlation-ID",
}

// The five variables, one at a time and all at once, are not read once the
// defaults are pinned.
func TestPinDefaultsIgnoresTheEnvironment(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"nothing set", nil},
		{"tracking off", map[string]string{"SCHEMAFLUX_REQUEST_TRACKING": "false"}},
		{"a request strategy", map[string]string{"SCHEMAFLUX_REQUEST_ID_STRATEGY": "uuid"}},
		{"a request strategy that is none", map[string]string{"SCHEMAFLUX_REQUEST_ID_STRATEGY": "none"}},
		{"a correlation strategy", map[string]string{"SCHEMAFLUX_CORRELATION_ID_STRATEGY": "generate"}},
		{"a request header", map[string]string{"SCHEMAFLUX_REQUEST_ID_HEADER": "X-Mine"}},
		{"a correlation header", map[string]string{"SCHEMAFLUX_CORRELATION_ID_HEADER": "X-Mine-Too"}},
		{"all five", map[string]string{
			"SCHEMAFLUX_REQUEST_TRACKING": "false", "SCHEMAFLUX_REQUEST_ID_STRATEGY": "timestamp",
			"SCHEMAFLUX_CORRELATION_ID_STRATEGY": "none", "SCHEMAFLUX_REQUEST_ID_HEADER": "A", "SCHEMAFLUX_CORRELATION_ID_HEADER": "B",
		}},
		{"values that cannot be used", map[string]string{
			"SCHEMAFLUX_REQUEST_TRACKING": "maybe", "SCHEMAFLUX_REQUEST_ID_STRATEGY": "carrier-pigeon",
		}},
		{"blank values", map[string]string{"SCHEMAFLUX_REQUEST_ID_HEADER": "  ", "SCHEMAFLUX_CORRELATION_ID_STRATEGY": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetConfig(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			reloadEnvironment()
			problems := recordEnvProblems(t)
			PinDefaults()
			if got := GetConfig(); !reflect.DeepEqual(got, defaultsPinned) {
				t.Errorf("GetConfig = %+v, want the defaults %+v", got, defaultsPinned)
			}
			if len(*problems) != 0 {
				t.Errorf("a variable was read and reported: %v", *problems)
			}
		})
	}
}

func TestPinDefaultsIsIdempotentAndYieldsToConfigure(t *testing.T) {
	resetConfig(t)
	PinDefaults()
	PinDefaults()
	if got := GetConfig(); !reflect.DeepEqual(got, defaultsPinned) {
		t.Fatalf("GetConfig = %+v", got)
	}

	custom := Config{Enabled: true, RequestIDStrategy: IDStrategyUUID, RequestIDHeader: "X-Custom"}
	Configure(custom)
	want := GetConfig()
	PinDefaults()
	if got := GetConfig(); !reflect.DeepEqual(got, want) {
		t.Errorf("PinDefaults replaced a configuration: %+v, want %+v", got, want)
	}
	if want.RequestIDHeader != "X-Custom" {
		t.Errorf("Configure did not take: %+v", want)
	}

	t.Run("Configure after PinDefaults replaces it", func(t *testing.T) {
		resetConfig(t)
		PinDefaults()
		Configure(custom)
		if got := GetConfig(); got.RequestIDHeader != "X-Custom" {
			t.Errorf("GetConfig = %+v", got)
		}
	})
	t.Run("the environment is read again after a reload when nothing is pinned", func(t *testing.T) {
		resetConfig(t)
		t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-Env")
		reloadEnvironment()
		if got := GetConfig().RequestIDHeader; got != "X-Env" {
			t.Errorf("RequestIDHeader = %q", got)
		}
	})
}

// A pinned configuration survives config.ReloadEnvironment, which is what a
// program that never reads the environment needs from it.
func TestPinDefaultsSurvivesAReloadOfTheEnvironment(t *testing.T) {
	resetConfig(t)
	PinDefaults()
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-After")
	config.ReloadEnvironment()
	if got := GetConfig(); got.RequestIDHeader != "X-Request-ID" {
		t.Errorf("RequestIDHeader = %q after a reload", got.RequestIDHeader)
	}
}

func TestPinDefaultsMakesIdsAsTheDefaultsDo(t *testing.T) {
	resetConfig(t)
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "false")
	reloadEnvironment()
	PinDefaults()
	ctx, metadata := Ensure(context.Background(), "", "")
	if metadata.RequestID == "" {
		t.Error("no request id with the defaults pinned over a variable that says off")
	}
	if got := FromContext(ctx); got.RequestID != metadata.RequestID {
		t.Errorf("the id is not on the context")
	}
}
