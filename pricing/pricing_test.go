package pricing_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/pricing"
)

// near reports whether two dollar figures agree to a billionth of a dollar.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRegisterAddsAPriceTheTableDidNotHave(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		model func(name string) pricing.Model
		usage schemaflux.Usage
		want  float64
	}{
		{"input and output",
			func(n string) pricing.Model { return pricing.Model{Provider: "acme", Name: n, Input: 2, Output: 8} },
			schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}, 10},
		{"input only",
			func(n string) pricing.Model { return pricing.Model{Provider: "acme", Name: n, Input: 2} },
			schemaflux.Usage{Input: 500_000, Output: 1_000_000}, 1},
		{"output only",
			func(n string) pricing.Model { return pricing.Model{Provider: "acme", Name: n, Output: 8} },
			schemaflux.Usage{Input: 1_000_000, Output: 250_000}, 2},
		{"a cached rate prices the cached tokens apart",
			func(n string) pricing.Model {
				return pricing.Model{Provider: "acme", Name: n, Input: 2, Output: 8, Cached: 0.5}
			},
			schemaflux.Usage{Input: 1_000_000, Cached: 400_000}, 1.4},
		{"no cached rate charges them at the input rate",
			func(n string) pricing.Model { return pricing.Model{Provider: "acme", Name: n, Input: 2, Output: 8} },
			schemaflux.Usage{Input: 1_000_000, Cached: 400_000}, 2},
		{"a reasoning rate prices the reasoning tokens apart",
			func(n string) pricing.Model {
				return pricing.Model{Provider: "acme", Name: n, Input: 2, Output: 8, Reasoning: 12}
			},
			schemaflux.Usage{Output: 1_000_000, Reasoning: 300_000}, 9.2},
		{"no reasoning rate charges them at the output rate",
			func(n string) pricing.Model { return pricing.Model{Provider: "acme", Name: n, Input: 2, Output: 8} },
			schemaflux.Usage{Output: 1_000_000, Reasoning: 300_000}, 8},
		{"the name is not case sensitive",
			func(n string) pricing.Model {
				return pricing.Model{Provider: "acme", Name: strings.ToUpper(n), Input: 1, Output: 1}
			},
			schemaflux.Usage{Input: 1_000_000}, 1},
		{"white space around the name is ignored",
			func(n string) pricing.Model {
				return pricing.Model{Provider: "acme", Name: "  " + n + "\t", Input: 1, Output: 1}
			},
			schemaflux.Usage{Input: 1_000_000}, 1},
		{"a rate of a hundredth of a cent per million",
			func(n string) pricing.Model {
				return pricing.Model{Provider: "acme", Name: n, Input: 0.0001, Output: 0.0001}
			},
			schemaflux.Usage{Input: 1_000_000}, 0.0001},
		{"the highest rate allowed",
			func(n string) pricing.Model {
				return pricing.Model{Provider: "acme", Name: n, Input: 1000, Output: 1000}
			},
			schemaflux.Usage{Input: 1_000_000}, 1000},
		{"a provider with white space is kept",
			func(n string) pricing.Model { return pricing.Model{Provider: " acme ", Name: n, Input: 1, Output: 1} },
			schemaflux.Usage{Output: 2_000_000}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			name := uniqueModel(t)
			if _, err := pricing.Register(c.model(name)); err != nil {
				t.Fatalf("Register returned %v", err)
			}
			got := pricing.Price(c.usage, name, "acme")
			if !got.Priced || !near(got.Total, c.want) {
				t.Fatalf("Price = %+v, want a priced %v", got, c.want)
			}
			if got.Source != name || got.Currency != "USD" {
				t.Fatalf("Source = %q, Currency = %q", got.Source, got.Currency)
			}
		})
	}

	t.Run("registering again replaces the price", func(t *testing.T) {
		t.Parallel()
		name := uniqueModel(t)
		_, _ = pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1, Output: 1})
		_, _ = pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 3, Output: 3})
		if got := pricing.Price(schemaflux.Usage{Input: 1_000_000}, name, "acme"); !near(got.Total, 3) {
			t.Fatalf("Total = %v, want the second price", got.Total)
		}
	})
	t.Run("the model is listed afterwards, in lower case", func(t *testing.T) {
		t.Parallel()
		name := uniqueModel(t)
		_, _ = pricing.Register(pricing.Model{Provider: "acme", Name: strings.ToUpper(name), Input: 1, Output: 1})
		if !slices.Contains(pricing.Models(), name) {
			t.Fatalf("Models() lacks %q", name)
		}
	})
}

func TestRegisterRefusesAModelItCannotPrice(t *testing.T) {
	t.Parallel()
	nan, inf := math.NaN(), math.Inf(1)
	cases := []struct {
		name    string
		model   pricing.Model
		message string
	}{
		{"no name", pricing.Model{Provider: "acme", Input: 1, Output: 1}, "no name"},
		{"a name of white space", pricing.Model{Provider: "acme", Name: " \t", Input: 1, Output: 1}, "no name"},
		{"no provider", pricing.Model{Name: "x", Input: 1, Output: 1}, "no provider"},
		{"a provider of white space", pricing.Model{Provider: "  ", Name: "x", Input: 1, Output: 1}, "no provider"},
		{"an input rate that is not a number", pricing.Model{Provider: "acme", Name: "x", Input: nan, Output: 1}, "input rate that is not a number"},
		{"an output rate that is not a number", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: nan}, "output rate that is not a number"},
		{"a cached rate that is not a number", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: 1, Cached: nan}, "cached rate that is not a number"},
		{"a reasoning rate that is not a number", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: 1, Reasoning: nan}, "reasoning rate that is not a number"},
		{"a negative input rate", pricing.Model{Provider: "acme", Name: "x", Input: -1, Output: 1}, "negative input rate"},
		{"a negative output rate", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: -0.01}, "negative output rate"},
		{"a negative cached rate", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: 1, Cached: -1}, "negative cached rate"},
		{"a negative reasoning rate", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: 1, Reasoning: -1}, "negative reasoning rate"},
		{"a rate above the ceiling", pricing.Model{Provider: "acme", Name: "x", Input: 1000.01, Output: 1}, "above $1,000 per million"},
		{"an infinite rate", pricing.Model{Provider: "acme", Name: "x", Input: 1, Output: inf}, "above $1,000 per million"},
		{"a price per token given as a price per million", pricing.Model{Provider: "acme", Name: "x", Input: 1e6, Output: 1e6}, "above $1,000 per million"},
		{"nothing charged for input and output", pricing.Model{Provider: "acme", Name: "x"}, "charges nothing"},
		{"nothing charged though cached and reasoning are", pricing.Model{Provider: "acme", Name: "x", Cached: 1, Reasoning: 1}, "charges nothing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.model.Name != "" && strings.TrimSpace(c.model.Name) != "" {
				c.model.Name = uniqueModel(t)
			}
			_, err := pricing.Register(c.model)
			var e *schemaflux.Error
			if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration {
				t.Fatalf("err = %#v, want an ErrConfiguration", err)
			}
			if !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Errorf("errors.Is(err, ErrConfiguration) is false")
			}
			if e.Op != "pricing.register" {
				t.Errorf("Op = %q", e.Op)
			}
			if !strings.Contains(e.Message, c.message) {
				t.Errorf("message = %q, want it to say %q", e.Message, c.message)
			}
			if c.model.Name != "" && strings.TrimSpace(c.model.Name) != "" {
				if slices.Contains(pricing.Models(), c.model.Name) {
					t.Errorf("a refused model was put in the table")
				}
			}
		})
	}

	t.Run("the message is lower case and names the model but never its rates", func(t *testing.T) {
		t.Parallel()
		name := uniqueModel(t)
		_, err := pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: -123.456})
		msg := err.Error()
		if !strings.Contains(msg, name) || strings.Contains(msg, "123.456") || strings.Contains(msg, "pricing: invalid pricing model") {
			t.Fatalf("message = %q", msg)
		}
	})
	t.Run("a long name in a message is cut", func(t *testing.T) {
		t.Parallel()
		_, err := pricing.Register(pricing.Model{Name: strings.Repeat("n", 500), Input: 1, Output: 1})
		if n := len(err.Error()); n > 300 {
			t.Fatalf("message is %d bytes", n)
		}
	})
}

func TestModelsListsTheModelsWithAPrice(t *testing.T) {
	t.Parallel()
	listed := pricing.Models()

	t.Run("it is sorted and has no duplicates", func(t *testing.T) {
		t.Parallel()
		if !slices.IsSorted(listed) {
			t.Fatal("not sorted")
		}
		if len(slices.Compact(slices.Clone(listed))) != len(listed) {
			t.Fatal("duplicates")
		}
	})
	t.Run("every name is lower case", func(t *testing.T) {
		t.Parallel()
		for _, name := range listed {
			if name != strings.ToLower(name) || name != strings.TrimSpace(name) || name == "" {
				t.Errorf("name %q", name)
			}
		}
	})
	t.Run("the slice is a copy", func(t *testing.T) {
		t.Parallel()
		got := pricing.Models()
		if len(got) == 0 {
			t.Fatal("no models")
		}
		got[0] = "changed"
		if slices.Contains(pricing.Models(), "changed") {
			t.Fatal("changing the slice changed the table")
		}
	})
	t.Run("a registered model is in it", func(t *testing.T) {
		t.Parallel()
		name := uniqueModel(t)
		before := len(pricing.Models())
		_, _ = pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1, Output: 1})
		if !slices.Contains(pricing.Models(), name) || len(pricing.Models()) < before {
			t.Fatalf("Models() lacks %q", name)
		}
	})
	for _, name := range []string{
		"gpt-6-luna", "gpt-5.6-luna", "gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-4", "gpt-3.5-turbo",
		"claude-3-opus", "claude-3-haiku", "o1-mini", "gemma-4-31b",
	} {
		t.Run("the built-in table has "+name, func(t *testing.T) {
			t.Parallel()
			if !slices.Contains(listed, name) {
				t.Fatalf("Models() lacks %q", name)
			}
		})
	}
}

func TestJevIsPricedByTheTable(t *testing.T) {
	t.Parallel()
	oneMillion := schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}
	cases := []struct {
		model, provider string
	}{
		{"jev-latest", "jev"},
		{"jev-1.13.0", "jev"},
		{"~typesafe/jev-latest", "jev-openrouter"},
		{"typesafe/jev-latest", "jev-openrouter"},
		{"typesafe/jev-1.13.0", "jev-openrouter"},
		{"JEV-LATEST", "jev"},
		{" jev-latest ", "jev-litellm"},
	}
	for _, c := range cases {
		t.Run(c.model+" on "+c.provider, func(t *testing.T) {
			t.Parallel()
			got := pricing.Price(oneMillion, c.model, c.provider)
			if !got.Priced {
				t.Fatalf("Cost = %+v, want a priced one", got)
			}
			if !near(got.Input, 0.042) || !near(got.Output, 0) || !near(got.Total, 0.042) {
				t.Fatalf("Input = %v, Output = %v, Total = %v; want $0.042 per million input tokens and nothing for the answer", got.Input, got.Output, got.Total)
			}
		})
	}
	for _, name := range []string{"jev-latest", "jev-1.13.0", "~typesafe/jev-latest", "typesafe/jev-latest", "typesafe/jev-1.13.0"} {
		t.Run("Models lists "+name, func(t *testing.T) {
			t.Parallel()
			if !slices.Contains(pricing.Models(), name) {
				t.Fatalf("Models() lacks %q", name)
			}
		})
	}
	t.Run("a later version of the model is unpriced until it is added", func(t *testing.T) {
		t.Parallel()
		if got := pricing.Price(oneMillion, "jev-1.14.0", "jev"); got.Priced {
			t.Fatalf("Cost = %+v, want an unpriced one", got)
		}
	})
}

func TestPriceCountsEveryTokenOnce(t *testing.T) {
	t.Parallel()
	// $2 input, $0.50 cached, $8 output, $12 reasoning, per million tokens.
	name := uniqueModel(t)
	if _, err := pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 2, Output: 8, Cached: 0.5, Reasoning: 12}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name                      string
		usage                     schemaflux.Usage
		total, input, out, cached float64
		reasoning                 float64
	}{
		{"nothing", schemaflux.Usage{}, 0, 0, 0, 0, 0},
		{"input only", schemaflux.Usage{Input: 1_000_000}, 2, 2, 0, 0, 0},
		{"output only", schemaflux.Usage{Output: 1_000_000}, 8, 0, 8, 0, 0},
		{"cached is part of the input", schemaflux.Usage{Input: 1_000_000, Cached: 400_000}, 1.4, 1.2, 0, 0.2, 0},
		{"reasoning is part of the output", schemaflux.Usage{Output: 1_000_000, Reasoning: 300_000}, 9.2, 0, 5.6, 0, 3.6},
		{"all of it", schemaflux.Usage{Input: 1_000_000, Output: 1_000_000, Cached: 1_000_000, Reasoning: 1_000_000}, 12.5, 0, 0, 0.5, 12},
		{"more cached than input is held at the input", schemaflux.Usage{Input: 1_000_000, Cached: 2_000_000}, 0.5, 0, 0, 0.5, 0},
		{"more reasoning than output is held at the output", schemaflux.Usage{Output: 1_000_000, Reasoning: 5_000_000}, 12, 0, 0, 0, 12},
		{"a negative count is read as zero", schemaflux.Usage{Input: -7, Output: 1_000_000, Cached: -3}, 8, 0, 8, 0, 0},
		{"a total that disagrees is not read", schemaflux.Usage{Input: 1_000_000, Total: 99}, 2, 2, 0, 0, 0},
		{"a small call", schemaflux.Usage{Input: 1000, Output: 200}, 0.0036, 0.002, 0.0016, 0, 0},
		{"cache writes are not a separate charge", schemaflux.Usage{Input: 1_000_000, CacheWrite: 500_000}, 2, 2, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := pricing.Price(c.usage, name, "acme")
			if !got.Priced || !near(got.Total, c.total) {
				t.Fatalf("Total = %v (priced %v), want %v", got.Total, got.Priced, c.total)
			}
			if c.input != 0 && !near(got.Input, c.input) || c.out != 0 && !near(got.Output, c.out) ||
				c.cached != 0 && !near(got.Cached, c.cached) || c.reasoning != 0 && !near(got.Reasoning, c.reasoning) {
				t.Fatalf("Cost = %+v", got)
			}
			if got.Estimated {
				t.Error("a price of a measured count is not an estimate")
			}
		})
	}
}

func TestPriceReportsAnUnpricedModelAsUnknownAndNotFree(t *testing.T) {
	t.Parallel()
	usage := schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}
	cases := []struct {
		name            string
		model, provider string
	}{
		{"a model nobody priced", "pricing-test-never-registered", "acme"},
		{"an empty model", "", "openai"},
		{"a model named like a priced one's sibling", "gpt-5-pro", "openai"},
		{"a sibling with a size", "gpt-6-luna-mini", "openai"},
		{"a model of a priced family with a minor version", "gpt-5.4-mini", "openai"},
		{"a priced model on a transport not billed per token", "gpt-5", "codex"},
		{"the same transport in other case", "gpt-5", " Codex "},
		{"a cheap model on that transport", "gpt-5-nano", "codex"},
		{"a dated snapshot of an unpriced model", "pricing-test-never-registered-2025-08-07", "acme"},
		{"only a version", "2025-08-07", "openai"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := pricing.Price(usage, c.model, c.provider)
			if got.Priced || got.Total != 0 || got.Input != 0 || got.Output != 0 || got.Source != "" {
				t.Fatalf("Cost = %+v, want an unpriced one with no figures", got)
			}
			if got.Currency != "USD" {
				t.Errorf("Currency = %q", got.Currency)
			}
		})
	}
}

func TestPriceFindsADatedSnapshotOfAPricedModel(t *testing.T) {
	t.Parallel()
	base := uniqueModel(t)
	_, _ = pricing.Register(pricing.Model{Provider: "acme", Name: base, Input: 4, Output: 4})
	usage := schemaflux.Usage{Input: 1_000_000}
	for _, suffix := range []string{"-2025-08-07", "-20240229", "-2026-01-01"} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			got := pricing.Price(usage, base+suffix, "acme")
			if !got.Priced || !near(got.Total, 4) || got.Source != base {
				t.Fatalf("Cost = %+v, want the base model's price with its name as the source", got)
			}
		})
	}
	t.Run("the model is looked up without regard to case", func(t *testing.T) {
		t.Parallel()
		if got := pricing.Price(usage, strings.ToUpper(base), "acme"); !got.Priced {
			t.Fatalf("Cost = %+v", got)
		}
	})
	t.Run("a built-in model has its table price", func(t *testing.T) {
		t.Parallel()
		got := pricing.Price(schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}, "gpt-5", "openai")
		if !near(got.Input, 1.25) || !near(got.Output, 10) || got.Source != "gpt-5" {
			t.Fatalf("Cost = %+v", got)
		}
	})
	t.Run("the provider does not change the price of a model", func(t *testing.T) {
		t.Parallel()
		a := pricing.Price(usage, base, "one")
		b := pricing.Price(usage, base, "two")
		if a != b {
			t.Fatalf("%+v != %+v", a, b)
		}
	})
}
