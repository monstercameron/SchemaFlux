// Package settingtest holds the one test of the rule for putting a process-wide
// setting back, so that every function that returns a restore is held to the same
// table. See package setting for the rule.
package settingtest

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

// Subject is a function that sets a process-wide setting and returns its
// restore, described the way the table needs it.
type Subject struct {
	// Name is the name of the function under test.
	Name string

	// Set puts in force the nth distinct setting, for n from 1, and returns the
	// restore the function under test returned for that call.
	Set func(n int) (restore func())

	// Current says which setting is in force: the n of the call whose setting it
	// is, and 0 for the one that was in force before the first call.
	Current func() int
}

// Convention runs the table against subject. It leaves the setting as it found
// it, and fails the test if it does not find it there to begin with: a test that
// ran before this one did not restore what it changed.
func Convention(t *testing.T, subject Subject) {
	t.Helper()
	if got := subject.Current(); got != 0 {
		t.Fatalf("%s: setting %d was in force before the test began", subject.Name, got)
	}
	check := func(t *testing.T, want int, when string) {
		t.Helper()
		if got := subject.Current(); got != want {
			t.Errorf("%s: %s: setting %d is in force, want %d", subject.Name, when, got, want)
		}
	}
	t.Cleanup(func() {
		if got := subject.Current(); got != 0 {
			t.Errorf("%s: setting %d was left in force by the table", subject.Name, got)
		}
	})

	t.Run("a restore puts back what was there", func(t *testing.T) {
		restore := subject.Set(1)
		check(t, 1, "after the call")
		restore()
		check(t, 0, "after the restore")
	})

	t.Run("a restore called twice puts back once", func(t *testing.T) {
		first := subject.Set(1)
		first()
		second := subject.Set(2)
		first()
		check(t, 2, "after the first restore is called again while a later setting is in force")
		second()
		check(t, 0, "after the second restore")
		second()
		check(t, 0, "after the second restore is called again")
	})

	t.Run("restores in reverse order step back one at a time", func(t *testing.T) {
		first, second, third := subject.Set(1), subject.Set(2), subject.Set(3)
		check(t, 3, "after three calls")
		third()
		check(t, 2, "after the third restore")
		second()
		check(t, 1, "after the second restore")
		first()
		check(t, 0, "after the first restore")
	})

	t.Run("an earlier restore called late leaves a later setting in force", func(t *testing.T) {
		first, second := subject.Set(1), subject.Set(2)
		first()
		check(t, 2, "after the earlier restore, called first")
		second()
		check(t, 0, "after the later restore: the earlier one is not brought back")
	})

	t.Run("a restore in the middle changes nothing until the one above it runs", func(t *testing.T) {
		first, second, third := subject.Set(1), subject.Set(2), subject.Set(3)
		second()
		check(t, 3, "after the middle restore")
		third()
		check(t, 1, "after the top restore: the middle one is skipped")
		first()
		check(t, 0, "after the bottom restore")
	})

	t.Run("a restore called twice does not undo a setting made after the first call", func(t *testing.T) {
		first := subject.Set(1)
		second := subject.Set(2)
		second()
		second()
		check(t, 1, "after the second restore, twice")
		third := subject.Set(3)
		second()
		check(t, 3, "after the second restore is called a third time with a newer setting in force")
		third()
		first()
		check(t, 0, "after the rest")
	})

	t.Run("a restore that runs after everything is restored changes nothing", func(t *testing.T) {
		first := subject.Set(1)
		first()
		first()
		check(t, 0, "after the same restore twice")
		second := subject.Set(2)
		check(t, 2, "after a new call")
		first()
		check(t, 2, "after an old restore")
		second()
		check(t, 0, "at the end")
	})

	t.Run("restores in any of the orders of five calls come back to what was there", func(t *testing.T) {
		const calls = 5
		orders := permutations(calls)
		for _, order := range orders {
			restores := make([]func(), calls)
			for i := range restores {
				restores[i] = subject.Set(i + 1)
			}
			// What is in force after each restore is the newest setting whose
			// restore has not been called.
			called := make([]bool, calls)
			for _, i := range order {
				restores[i]()
				called[i] = true
				want := 0
				for j := calls - 1; j >= 0; j-- {
					if !called[j] {
						want = j + 1
						break
					}
				}
				if got := subject.Current(); got != want {
					t.Fatalf("%s: after restoring %v of %v in force is %d, want %d", subject.Name, order[:indexOf(order, i)+1], order, got, want)
				}
			}
		}
	})

	t.Run("restores from several goroutines at once, each called twice, come back to what was there", func(t *testing.T) {
		for round := 0; round < 20; round++ {
			const calls = 12
			restores := make([]func(), calls)
			for i := range restores {
				restores[i] = subject.Set(i + 1)
			}
			rng := rand.New(rand.NewSource(int64(round)))
			rng.Shuffle(len(restores), func(i, j int) { restores[i], restores[j] = restores[j], restores[i] })
			var wait sync.WaitGroup
			for _, restore := range restores {
				for twice := 0; twice < 2; twice++ {
					wait.Add(1)
					go func() {
						defer wait.Done()
						restore()
					}()
				}
			}
			wait.Wait()
			check(t, 0, fmt.Sprintf("after round %d", round))
		}
	})

	t.Run("calls from several goroutines at once are all put back", func(t *testing.T) {
		for round := 0; round < 20; round++ {
			const calls = 12
			restores := make([]func(), calls)
			var wait sync.WaitGroup
			for i := range restores {
				wait.Add(1)
				go func() {
					defer wait.Done()
					restores[i] = subject.Set(i + 1)
				}()
			}
			wait.Wait()
			if got := subject.Current(); got < 1 || got > calls {
				t.Errorf("%s: setting %d is in force after %d concurrent calls", subject.Name, got, calls)
			}
			for _, restore := range restores {
				wait.Add(1)
				go func() {
					defer wait.Done()
					restore()
				}()
			}
			wait.Wait()
			check(t, 0, fmt.Sprintf("after round %d", round))
		}
	})
}

// permutations returns every order of 0..n-1.
func permutations(n int) [][]int {
	var all [][]int
	var build func(prefix []int, rest []int)
	build = func(prefix, rest []int) {
		if len(rest) == 0 {
			all = append(all, append([]int(nil), prefix...))
			return
		}
		for i := range rest {
			next := append(append([]int(nil), rest[:i]...), rest[i+1:]...)
			build(append(prefix, rest[i]), next)
		}
	}
	rest := make([]int, n)
	for i := range rest {
		rest[i] = i
	}
	build(nil, rest)
	return all
}

func indexOf(order []int, v int) int {
	for i, x := range order {
		if x == v {
			return i
		}
	}
	return -1
}
