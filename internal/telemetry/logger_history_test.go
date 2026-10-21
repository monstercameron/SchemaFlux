package telemetry

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// The history is a ring of the newest BufferSize entries, oldest first.
func TestTheHistoryKeepsTheNewestEntriesInOrder(t *testing.T) {
	for _, size := range []int{1, 2, 3, 7, 10} {
		for _, records := range []int{0, 1, size - 1, size, size + 1, size * 3, size*3 + 2} {
			if records < 0 {
				continue
			}
			t.Run(fmt.Sprintf("size %d, %d records", size, records), func(t *testing.T) {
				log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: true, BufferSize: size, DisableStderr: true})
				for i := 0; i < records; i++ {
					log.Info("record", "n", i)
				}

				entries := log.Entries()
				wantLen := records
				if wantLen > size {
					wantLen = size
				}
				if len(entries) != wantLen {
					t.Fatalf("%d entries, want %d", len(entries), wantLen)
				}
				for i, entry := range entries {
					want := int64(records - wantLen + i)
					if entry.Attributes["n"] != want {
						t.Fatalf("entry %d carries n=%v, want %d (oldest first)", i, entry.Attributes["n"], want)
					}
				}
			})
		}
	}
}

func TestResettingTheHistoryStartsTheRingOver(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: true, BufferSize: 3, DisableStderr: true})
	for i := 0; i < 8; i++ {
		log.Info("record", "n", i)
	}
	log.ResetEntries()
	if got := log.Entries(); len(got) != 0 {
		t.Fatalf("%d entries after a reset", len(got))
	}
	log.Info("after", "n", 100)
	log.Info("after", "n", 101)
	entries := log.Entries()
	if len(entries) != 2 || entries[0].Attributes["n"] != int64(100) || entries[1].Attributes["n"] != int64(101) {
		t.Errorf("entries after a reset and two records: %+v", entries)
	}
}

// A reset starts the ring over: the next records are read back oldest first
// whatever position the ring had reached when it was cleared. Two records after
// the reset would not show it; filling the ring again does.
func TestResettingTheHistoryAfterTheRingWrappedKeepsTheNextRecordsInOrder(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: true, BufferSize: 3, DisableStderr: true})
	for i := 0; i < 8; i++ { // 5 overwrites: the ring's start is now 2
		log.Info("record", "n", i)
	}
	log.ResetEntries()
	for i := 100; i < 103; i++ {
		log.Info("after", "n", i)
	}
	entries := log.Entries()
	if len(entries) != 3 {
		t.Fatalf("%d entries, want 3", len(entries))
	}
	for i, entry := range entries {
		if want := int64(100 + i); entry.Attributes["n"] != want {
			t.Errorf("entry %d carries n=%v, want %d (oldest first)", i, entry.Attributes["n"], want)
		}
	}
}

func TestAnEntriesSnapshotIsNotAffectedByLaterRecords(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: true, BufferSize: 2, DisableStderr: true})
	log.Info("a", "n", 1)
	log.Info("b", "n", 2)
	snapshot := log.Entries()
	log.Info("c", "n", 3)
	if snapshot[0].Message != "a" || snapshot[1].Message != "b" {
		t.Errorf("a snapshot changed after a later record: %+v", snapshot)
	}
}

func TestTheHistoryIsSafeUnderConcurrentLogging(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: true, BufferSize: 16, DisableStderr: true})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				log.Info("record", "n", i)
				_ = log.Entries()
			}
		}()
	}
	wg.Wait()
	if got := len(log.Entries()); got != 16 {
		t.Errorf("%d entries, want the 16 the ring holds", got)
	}
}

// Attributes added with WithFields come out in key order, whatever order the map
// iterates in, so two runs write the same line.
func TestWithFieldsWritesAttributesInKeyOrder(t *testing.T) {
	fields := map[string]any{"zeta": 1, "alpha": 2, "mid": 3, "beta": 4, "omega": 5, "gamma": 6}
	for run := 0; run < 20; run++ {
		var out bytes.Buffer
		log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Output: &out, Capture: false})
		log.WithFields(fields).Info("line")

		text := out.String()
		last := -1
		for _, key := range []string{"alpha=", "beta=", "gamma=", "mid=", "omega=", "zeta="} {
			at := strings.Index(text, key)
			if at < 0 || at < last {
				t.Fatalf("run %d: attributes out of key order in %q", run, text)
			}
			last = at
		}
	}
}

// Nested groups share a slice with the handler they came from; adding a key to
// a group must not write into that shared slice's spare capacity.
func TestGroupedAttributesDoNotShareTheirGroupSlice(t *testing.T) {
	groups := make([]string, 1, 8)
	groups[0] = "outer"
	first := map[string]any{}
	second := map[string]any{}

	addAttr(first, groups, slog.Group("a", slog.String("x", "1")))
	addAttr(second, groups, slog.Group("b", slog.String("x", "2")))

	if first["outer.a.x"] != "1" || second["outer.b.x"] != "2" {
		t.Errorf("group keys came out as %v and %v", first, second)
	}
	if len(groups) != 1 || cap(groups) != 8 || groups[:2][1] != "" {
		t.Errorf("the caller's group slice was written to: %v", groups[:2])
	}
}
