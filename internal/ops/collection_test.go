package ops

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestCollectionRefusalsBeforeACallHaveAKind(t *testing.T) {
	things := []string{"a", "b", "c"}
	locked := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 100})
	chooseWith := func(change func(*ChooseOptions)) func() error {
		return func() error {
			options := NewChooseOptions()
			change(&options)
			_, err := Choose(things, options)
			return err
		}
	}
	filterWith := func(change func(*FilterOptions)) func() error {
		return func() error {
			options := NewFilterOptions().WithCriteria("short ones")
			change(&options)
			_, err := Filter(things, options)
			return err
		}
	}
	sortWith := func(change func(*SortOptions)) func() error {
		return func() error {
			options := NewSortOptions().WithCriteria("alphabetical")
			change(&options)
			_, err := Sort(things, options)
			return err
		}
	}
	checkRefusalsBeforeACall(t, []refusalBeforeACall{
		{"Choose with TopN of zero", chooseWith(func(o *ChooseOptions) { o.TopN = 0 }),
			"invalid options: topN must be at least 1, got 0", types.KindConfiguration},
		{"Choose with a negative TopN", chooseWith(func(o *ChooseOptions) { o.TopN = -2 }),
			"invalid options: topN must be at least 1, got -2", types.KindConfiguration},
		{"Choose with TopN above one", chooseWith(func(o *ChooseOptions) { o.TopN = 3 }),
			"invalid options: topN=3: Choose returns a single result; use Rank with WithTopK(3) for an ordered list", types.KindConfiguration},
		{"Choose with a strategy nobody knows", chooseWith(func(o *ChooseOptions) { o.Strategy = "lottery" }),
			"invalid options: invalid strategy: lottery", types.KindConfiguration},
		{"Choose with a threshold above one", chooseWith(func(o *ChooseOptions) { o.CommonOptions.Threshold = 2 }),
			"invalid options: threshold must be between 0 and 1, got 2.000000", types.KindConfiguration},
		{"Choose under a locked output ceiling", chooseWith(func(o *ChooseOptions) {
			o.CommonOptions.Context = locked
			o.OpOptions.MaxOutputTokens = 500
		}), "invalid options: locked policy: max output tokens 500 exceeds the client-scope ceiling of 100", types.KindConfiguration},
		{"Choose with a bad option beside no options", func() error {
			options := NewChooseOptions()
			options.TopN = 0
			_, err := Choose([]string(nil), options)
			return err
		}, "invalid options: topN must be at least 1, got 0", types.KindConfiguration},
		{"Filter with no criteria", filterWith(func(o *FilterOptions) { o.Criteria = "" }),
			"invalid options: filter criteria is required", types.KindConfiguration},
		{"Filter with a floor above one", filterWith(func(o *FilterOptions) { o.MinConfidence = 1.2 }),
			"invalid options: min confidence must be between 0 and 1, got 1.200000", types.KindConfiguration},
		{"Filter with a negative floor", filterWith(func(o *FilterOptions) { o.MinConfidence = -1 }),
			"invalid options: min confidence must be between 0 and 1, got -1.000000", types.KindConfiguration},
		{"Filter with a floor that is not a number", filterWith(func(o *FilterOptions) { o.MinConfidence = math.NaN() }),
			"invalid options: min confidence must be between 0 and 1, got NaN", types.KindConfiguration},
		{"Filter with an infinite floor", filterWith(func(o *FilterOptions) { o.MinConfidence = math.Inf(1) }),
			"invalid options: min confidence must be between 0 and 1, got +Inf", types.KindConfiguration},
		{"Filter with a threshold above one", filterWith(func(o *FilterOptions) { o.CommonOptions.Threshold = 7 }),
			"invalid options: threshold must be between 0 and 1, got 7.000000", types.KindConfiguration},
		{"Filter under a locked output ceiling", filterWith(func(o *FilterOptions) {
			o.CommonOptions.Context = locked
			o.OpOptions.MaxOutputTokens = 101
		}), "invalid options: locked policy: max output tokens 101 exceeds the client-scope ceiling of 100", types.KindConfiguration},
		{"Filter with a bad option beside no items", func() error {
			_, err := Filter([]string(nil), NewFilterOptions())
			return err
		}, "invalid options: filter criteria is required", types.KindConfiguration},
		{"Sort with no criteria", sortWith(func(o *SortOptions) { o.Criteria = "" }),
			"invalid options: sort criteria is required", types.KindConfiguration},
		{"Sort with a direction nobody knows", sortWith(func(o *SortOptions) { o.Direction = "sideways" }),
			"invalid options: invalid direction: sideways", types.KindConfiguration},
		{"Sort with a threshold above one", sortWith(func(o *SortOptions) { o.CommonOptions.Threshold = 8 }),
			"invalid options: threshold must be between 0 and 1, got 8.000000", types.KindConfiguration},
		{"Sort with a bad option beside one item", func() error {
			options := NewSortOptions()
			_, err := Sort([]string{"only"}, options)
			return err
		}, "invalid options: sort criteria is required", types.KindConfiguration},
	})
}

// Choose with nothing to choose from refuses with the typed error callers
// match on, and classifying the refusals beside it must leave that type alone:
// a caller that reads err.(types.ChooseError) keeps working.
func TestChooseWithNoOptionsKeepsItsOwnType(t *testing.T) {
	swapNoProvider(t)
	_, err := Choose([]string(nil), NewChooseOptions())
	chooseErr, ok := err.(types.ChooseError)
	if !ok {
		t.Fatalf("Choose with no options returned %T, want types.ChooseError", err)
	}
	if chooseErr.OptionCount != 0 || err.Error() != "selection failed: no options provided" {
		t.Errorf("the refusal changed: %+v, %q", chooseErr, err.Error())
	}
	var target types.ChooseError
	if !errors.As(err, &target) {
		t.Error("errors.As no longer reaches types.ChooseError")
	}
}
