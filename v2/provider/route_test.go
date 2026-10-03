package provider

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

func placementOf(provider, model string) (regions []string, days int, known bool) {
	regions, days, known = llm.RoutePlacementFor(provider, model)
	return slices.Clone(regions), days, known
}

func TestDeclareRouteSaysWhereARouteRunsAndWhatItKeeps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		regions     []string
		days        int
		wantRegions []string
		wantDays    int
	}{
		{"one region", []string{"us"}, 30, []string{"us"}, 30},
		{"two regions", []string{"us", "eu"}, 30, []string{"us", "eu"}, 30},
		{"three regions", []string{"us", "eu", "ap"}, 7, []string{"us", "eu", "ap"}, 7},
		{"a route that keeps nothing", []string{"us"}, 0, []string{"us"}, 0},
		{"a long retention", []string{"eu"}, 365, []string{"eu"}, 365},
		{"no region at all", []string{}, 30, []string{}, 30},
		{"nil regions", nil, 30, nil, 30},
		{"a region in capitals is kept as written", []string{"US-East"}, 1, []string{"US-East"}, 1},
		{"one day", []string{"us"}, 1, []string{"us"}, 1},
		{"a retention of ninety days", []string{"us", "ca"}, 90, []string{"us", "ca"}, 90},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := nameFor(t)
			DeclareRoute(name, "model-1", tt.regions, tt.days)
			regions, days, known := placementOf(name, "model-1")
			if !known {
				t.Fatal("the route is not known after it was declared")
			}
			if !slices.Equal(regions, tt.wantRegions) || (regions == nil) != (tt.wantRegions == nil) {
				t.Errorf("regions = %#v, want %#v", regions, tt.wantRegions)
			}
			if days != tt.wantDays {
				t.Errorf("retention = %d, want %d", days, tt.wantDays)
			}
		})
	}
}

func TestARouteThatWasNotDeclaredIsNotKnown(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	if _, _, known := placementOf(name, "model-1"); known {
		t.Error("an undeclared route is known")
	}
	DeclareRoute(name, "model-1", []string{"us"}, 1)
	if _, _, known := placementOf(name, "model-2"); known {
		t.Error("declaring one model declared another")
	}
	if _, _, known := placementOf(name+"-other", "model-1"); known {
		t.Error("declaring one provider declared another")
	}
}

func TestALaterDeclarationReplacesTheEarlierOne(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	DeclareRoute(name, "m", []string{"us"}, 30)
	DeclareRoute(name, "m", []string{"eu", "ap"}, 0)
	regions, days, _ := placementOf(name, "m")
	if !slices.Equal(regions, []string{"eu", "ap"}) || days != 0 {
		t.Errorf("got %v and %d days, want [eu ap] and 0", regions, days)
	}
}

func TestDeclareRouteIgnoresCaseAndSpaceInTheNames(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	DeclareRoute("  "+name+" ", " Model-X ", []string{"us"}, 5)
	for _, spelling := range [][2]string{{name, "model-x"}, {" " + name, "MODEL-X "}, {name, " Model-X"}} {
		if regions, days, known := placementOf(spelling[0], spelling[1]); !known || !slices.Equal(regions, []string{"us"}) || days != 5 {
			t.Errorf("(%q, %q) gave %v, %d, %v", spelling[0], spelling[1], regions, days, known)
		}
	}
}

func TestDeclareRouteKeepsACopyOfTheRegions(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	regions := []string{"us", "eu"}
	DeclareRoute(name, "m", regions, 1)
	regions[0] = "changed"
	regions = append(regions, "late")
	got, _, _ := placementOf(name, "m")
	if !slices.Equal(got, []string{"us", "eu"}) {
		t.Errorf("changing the caller's slice changed the declaration: %v", got)
	}
}

func TestDeclareRouteIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	prefix := nameFor(t)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() { DeclareRoute(fmt.Sprintf("%s-%d", prefix, i), "m", []string{"us"}, i) })
		wg.Go(func() { placementOf(fmt.Sprintf("%s-%d", prefix, i), "m") })
	}
	wg.Wait()
	for i := range 64 {
		if _, days, known := placementOf(fmt.Sprintf("%s-%d", prefix, i), "m"); !known || days != i {
			t.Errorf("route %d: known %v, retention %d", i, known, days)
		}
	}
}

func TestADeclaredRouteIsWhatADataPolicyChecks(t *testing.T) {
	t.Parallel()
	name := nameFor(t)
	DeclareRoute(name, "m-us", []string{"us"}, 30)
	DeclareRoute(name, "m-both", []string{"us", "eu"}, 30)
	DeclareRoute(name, "m-long", []string{"us"}, 400)
	DeclareRoute(name, "m-nowhere", []string{}, 0)
	tests := []struct {
		name   string
		policy types.DataPolicy
		model  string
		allow  bool
	}{
		{"a policy that allows the provider, the region and the retention", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30}, "m-us", true},
		{"a policy with room to spare", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us", "eu"}, MaxRetentionDays: 90}, "m-us", true},
		{"a route that may run in two regions needs both allowed", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30}, "m-both", false},
		{"both allowed", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us", "eu"}, MaxRetentionDays: 30}, "m-both", true},
		{"a region the policy does not allow", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 30}, "m-us", false},
		{"a retention above the ceiling", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30}, "m-long", false},
		{"a provider the policy does not name", types.DataPolicy{AllowedProviders: []string{"other"}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30}, "m-us", false},
		{"a route that was never declared", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30}, "m-undeclared", false},
		{"a route declared to run nowhere", types.DataPolicy{AllowedProviders: []string{name}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30}, "m-nowhere", false},
		{"a policy that allows nothing", types.DataPolicy{}, "m-us", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := ops.WithExecConfig(t.Context(), &ops.ExecConfig{DataPolicy: tt.policy, DataPolicyDeclared: true})
			err := ops.CheckRoutePolicy(ctx, name, tt.model)
			if tt.allow && err != nil {
				t.Errorf("the policy refused the route: %v", err)
			}
			if !tt.allow && !errors.Is(err, ops.ErrDataPolicyRefusal) {
				t.Errorf("the policy allowed the route, or refused it with %v", err)
			}
		})
	}
}
