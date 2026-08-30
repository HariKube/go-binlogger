//nolint:errcheck
package gobinlogger_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	gobinlogger "github.com/harikube/go-binlogger/pkg/binlogger"
)

// setupDirs creates temporary WAL and snap directories and registers cleanup.
func setupDirs(t *testing.T) (walDir, snapDir string) {
	t.Helper()
	walDir, err := os.MkdirTemp("", "binlogger-wal-*")
	if err != nil {
		t.Fatalf("failed to create temp wal dir: %v", err)
	}
	snapDir, err = os.MkdirTemp("", "binlogger-snap-*")
	if err != nil {
		t.Fatalf("failed to create temp snap dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(walDir)
		os.RemoveAll(snapDir)
	})
	return walDir, snapDir
}

// newStarted creates and starts a BinLogger with syncInterval=0 (sync-on-write).
func newStarted(t *testing.T, walDir, snapDir string) *gobinlogger.BinLogger {
	t.Helper()
	bl := gobinlogger.NewBinLogger(walDir, snapDir, 0)
	if err := bl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return bl
}

// mustLog is a test helper that calls Log and fatals on error.
func mustLog(t *testing.T, bl *gobinlogger.BinLogger, entries ...string) {
	t.Helper()
	data := make([][]byte, len(entries))
	for i, e := range entries {
		data[i] = []byte(e)
	}
	if err := bl.Log(data); err != nil {
		t.Fatalf("Log: %v", err)
	}
}

// mustSnapshot calls CreateSnapshot and fatals on error.
func mustSnapshot(t *testing.T, bl *gobinlogger.BinLogger) (prev, curr uint64, entries []string, release func(bool) error) {
	t.Helper()
	p, c, ents, rel, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	for _, e := range ents {
		entries = append(entries, string(e.Data))
	}
	return p, c, entries, rel
}

// ----------------------------------------------------------------------------
// Happy-path integration test (mirrors original TestBinLogger but uses subtests)
// ----------------------------------------------------------------------------

func TestBinLogger_HappyPath(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	t.Run("log_three_entries", func(t *testing.T) {
		mustLog(t, bl, "first entry", "second entry", "third entry")
	})

	t.Run("snapshot_0_to_3", func(t *testing.T) {
		prev, curr, entries, release := mustSnapshot(t, bl)
		defer func() {
			if err := release(true); err != nil {
				t.Errorf("release: %v", err)
			}
		}()

		if prev != 0 || curr != 3 {
			t.Errorf("indices: got (%d,%d) want (0,3)", prev, curr)
		}
		if len(entries) != 3 {
			t.Fatalf("entry count: got %d want 3", len(entries))
		}
		want := []string{"first entry", "second entry", "third entry"}
		for i, w := range want {
			if entries[i] != w {
				t.Errorf("entry[%d]: got %q want %q", i, entries[i], w)
			}
		}
	})

	t.Run("log_two_more_entries", func(t *testing.T) {
		mustLog(t, bl, "fourth entry", "fifth entry")
	})

	t.Run("snapshot_3_to_5", func(t *testing.T) {
		prev, curr, entries, release := mustSnapshot(t, bl)
		defer func() {
			if err := release(true); err != nil {
				t.Errorf("release: %v", err)
			}
		}()

		if prev != 3 || curr != 5 {
			t.Errorf("indices: got (%d,%d) want (3,5)", prev, curr)
		}
		if len(entries) != 2 {
			t.Fatalf("entry count: got %d want 2", len(entries))
		}
		want := []string{"fourth entry", "fifth entry"}
		for i, w := range want {
			if entries[i] != w {
				t.Errorf("entry[%d]: got %q want %q", i, entries[i], w)
			}
		}
	})

	t.Run("log_sixth_entry_then_close", func(t *testing.T) {
		mustLog(t, bl, "sixth entry")
		if err := bl.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("restart_and_recover", func(t *testing.T) {
		bl2 := newStarted(t, walDir, snapDir)
		mustLog(t, bl2, "seventh entry")

		prev, curr, entries, release := mustSnapshot(t, bl2)
		defer func() {
			if err := release(true); err != nil {
				t.Errorf("release: %v", err)
			}
		}()

		if prev != 5 || curr != 7 {
			t.Errorf("indices after restart: got (%d,%d) want (5,7)", prev, curr)
		}
		if len(entries) != 2 {
			t.Fatalf("entry count: got %d want 2", len(entries))
		}
		want := []string{"sixth entry", "seventh entry"}
		for i, w := range want {
			if entries[i] != w {
				t.Errorf("entry[%d]: got %q want %q", i, entries[i], w)
			}
		}

		if err := bl2.Close(); err != nil {
			t.Fatalf("Close after restart: %v", err)
		}
	})
}

// ----------------------------------------------------------------------------
// Entry index values are contiguous and correct
// ----------------------------------------------------------------------------

func TestLog_EntryIndices(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	mustLog(t, bl, "a", "b", "c")

	_, _, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	defer release(true)

	for i, e := range ents {
		want := uint64(i + 1)
		if e.Index != want {
			t.Errorf("entry[%d].Index = %d, want %d", i, e.Index, want)
		}
	}
}

// ----------------------------------------------------------------------------
// CreateSnapshot with no new entries returns zero values and nil release
// ----------------------------------------------------------------------------

func TestCreateSnapshot_NoNewEntries(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	prev, curr, entries, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev != 0 || curr != 0 {
		t.Errorf("indices: got (%d,%d) want (0,0)", prev, curr)
	}
	if entries != nil {
		t.Errorf("entries: want nil, got %v", entries)
	}
	if release != nil {
		t.Errorf("release: want nil, got non-nil")
	}
}

// CreateSnapshot called twice without new logs in between must behave
// correctly on the second call (no new entries → nil release).
func TestCreateSnapshot_ConsecutiveWithoutLog(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	mustLog(t, bl, "only entry")

	_, _, _, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := release(true); err != nil {
		t.Fatalf("release first: %v", err)
	}

	// Second call with no new entries.
	_, _, entries, release2, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if entries != nil || release2 != nil {
		t.Errorf("expected no-op snapshot; got entries=%v release=non-nil", entries)
	}
}

// ----------------------------------------------------------------------------
// release(false) must NOT trigger storage.Release (WAL GC skipped)
// ----------------------------------------------------------------------------

func TestRelease_FalsePath(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	mustLog(t, bl, "entry1", "entry2")

	_, _, _, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	// release(false) must not panic and must return nil.
	if err := release(false); err != nil {
		t.Errorf("release(false): unexpected error: %v", err)
	}

	// After release(false), we can still create another snapshot (mutex unlocked).
	mustLog(t, bl, "entry3")
	_, _, ents, rel2, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot after release(false): %v", err)
	}
	if len(ents) == 0 {
		t.Error("expected entries after release(false), got none")
	}
	_ = rel2(true)
}

// ----------------------------------------------------------------------------
// Close is idempotent
// ----------------------------------------------------------------------------

func TestClose_Idempotent(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	mustLog(t, bl, "x")

	if err := bl.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second Close must not panic and must return nil.
	if err := bl.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Concurrent writers: logMutex must prevent index gaps
// ----------------------------------------------------------------------------

func TestLog_ConcurrentWriters(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	const goroutines = 20
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := bl.Log([][]byte{[]byte(fmt.Sprintf("entry-%d", i))}); err != nil {
				t.Errorf("concurrent Log: %v", err)
			}
		}(i)
	}
	wg.Wait()

	_, _, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	defer release(true)

	if len(ents) != goroutines {
		t.Errorf("entry count: got %d want %d", len(ents), goroutines)
	}

	// Verify indices are contiguous (1..goroutines) with no gaps or duplicates.
	seen := make(map[uint64]bool, goroutines)
	for _, e := range ents {
		if e.Index == 0 || e.Index > goroutines {
			t.Errorf("index %d out of range [1,%d]", e.Index, goroutines)
		}
		if seen[e.Index] {
			t.Errorf("duplicate index %d", e.Index)
		}
		seen[e.Index] = true
	}
	for i := uint64(1); i <= goroutines; i++ {
		if !seen[i] {
			t.Errorf("missing index %d", i)
		}
	}
}

// ----------------------------------------------------------------------------
// Snapshot with multiple snapshots on disk: correct (highest-index) snapshot
// is selected on recovery (not lexicographic last).
// ----------------------------------------------------------------------------

func TestStart_SnapshotSortCorrectness(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	// Write 9 entries and snapshot, then 2 more to reach index 11.
	// This creates snapshot files like 0-9.snap and 0-11.snap.
	// Lexicographically "0-9.snap" > "0-11.snap" (since '9' > '1'), so the
	// old code would pick the wrong file.
	for i := 0; i < 9; i++ {
		mustLog(t, bl, fmt.Sprintf("entry-%d", i+1))
	}
	_, _, _, rel, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := rel(true); err != nil {
		t.Fatalf("release: %v", err)
	}

	mustLog(t, bl, "entry-10", "entry-11")
	_, _, _, rel2, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if err := rel2(true); err != nil {
		t.Fatalf("release2: %v", err)
	}

	if err := bl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restart. Must recover from index 11, not 9.
	bl2 := newStarted(t, walDir, snapDir)
	defer bl2.Close()

	// No un-snapshotted entries remain; CreateSnapshot should be a no-op.
	_, _, entries, release3, err := bl2.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot after restart: %v", err)
	}
	if entries != nil || release3 != nil {
		t.Errorf("expected no-op snapshot after restart at index 11; got %d entries", len(entries))
	}

	// A new log entry must get index 12.
	mustLog(t, bl2, "entry-12")
	prev, curr, ents, rel4, err := bl2.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer rel4(true)

	if prev != 11 || curr != 12 {
		t.Errorf("indices: got (%d,%d) want (11,12)", prev, curr)
	}
	if len(ents) != 1 || string(ents[0].Data) != "entry-12" {
		t.Errorf("unexpected entries: %v", ents)
	}
}

// ----------------------------------------------------------------------------
// Periodic sync (syncInterval > 0): goroutine must flush and not panic
// ----------------------------------------------------------------------------

func TestSyncInterval_PeriodicSync(t *testing.T) {
	walDir, snapDir := setupDirs(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	bl := gobinlogger.NewBinLogger(walDir, snapDir, 50*time.Millisecond)
	if err := bl.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	mustLog(t, bl, "sync-test-entry")

	// Wait for context to expire so the ticker goroutine exits cleanly.
	<-ctx.Done()

	// A manual close should still work (idempotent, no race).
	if err := bl.Close(); err != nil {
		t.Errorf("Close after periodic sync: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Context cancellation triggers Close via WaitGroup goroutine
// ----------------------------------------------------------------------------

func TestContextCancellation_TriggersClose(t *testing.T) {
	walDir, snapDir := setupDirs(t)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	bl := gobinlogger.NewBinLogger(walDir, snapDir, 0)
	if err := bl.Start(ctx, &wg); err != nil {
		t.Fatalf("Start: %v", err)
	}

	mustLog(t, bl, "pre-cancel-entry")

	// Trigger context cancellation; the goroutine registered by Start should
	// call Close and signal Done on the WaitGroup.
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("WaitGroup never reached zero after context cancellation")
	}

	// A second explicit Close must be idempotent.
	if err := bl.Close(); err != nil {
		t.Errorf("second Close after ctx cancel: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Log with empty and nil slices
// ----------------------------------------------------------------------------

func TestLog_EmptySlice(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	// Empty slice should not error.
	if err := bl.Log([][]byte{}); err != nil {
		t.Errorf("Log(empty): unexpected error: %v", err)
	}

	// Nil slice should not error.
	if err := bl.Log(nil); err != nil {
		t.Errorf("Log(nil): unexpected error: %v", err)
	}
}

// ----------------------------------------------------------------------------
// MustLog panics on (simulated) error — tested via the Must* wrappers with
// valid inputs (smoke test that they don't panic when all is well).
// ----------------------------------------------------------------------------

func TestMustLog_NopanicOnSuccess(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	// Should not panic.
	bl.MustLog([][]byte{[]byte("must-log-entry")})
}

func TestMustCreateSnapshot_NopanicOnSuccess(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	bl.MustLog([][]byte{[]byte("e1")})
	prev, curr, ents, release := bl.MustCreateSnapshot()
	defer release(true)

	if prev != 0 || curr != 1 {
		t.Errorf("indices: got (%d,%d) want (0,1)", prev, curr)
	}
	if len(ents) != 1 {
		t.Errorf("entries: got %d want 1", len(ents))
	}
}

func TestMustStart_NopanicOnSuccess(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := gobinlogger.NewBinLogger(walDir, snapDir, 0)
	// Should not panic.
	bl.MustStart(context.Background())
	defer bl.Close()
}

// ----------------------------------------------------------------------------
// Start on non-existent WAL directory (brand new) vs existing
// ----------------------------------------------------------------------------

func TestStart_NonExistentWalDir(t *testing.T) {
	// walDir does not exist yet; wal.Create should be called.
	snapDir, err := os.MkdirTemp("", "binlogger-snap-*")
	if err != nil {
		t.Fatalf("MkdirTemp snap: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(snapDir) })

	walDir, err := os.MkdirTemp("", "binlogger-wal-*")
	if err != nil {
		t.Fatalf("MkdirTemp wal: %v", err)
	}
	// Remove the dir so wal.Create will be triggered (WAL does not exist).
	os.RemoveAll(walDir)
	t.Cleanup(func() { os.RemoveAll(walDir) })

	bl := gobinlogger.NewBinLogger(walDir, snapDir, 0)
	if err := bl.Start(context.Background()); err != nil {
		t.Fatalf("Start with missing walDir: %v", err)
	}
	defer bl.Close()

	mustLog(t, bl, "hello")
}

// ----------------------------------------------------------------------------
// Large payload
// ----------------------------------------------------------------------------

func TestLog_LargePayload(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	defer bl.Close()

	// 1 MB payload
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	if err := bl.Log([][]byte{payload}); err != nil {
		t.Fatalf("Log large payload: %v", err)
	}

	_, _, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	defer release(true)

	if len(ents) != 1 || len(ents[0].Data) != len(payload) {
		t.Errorf("unexpected entries after large payload log")
	}
}

// ============================================================================
// DATA INTEGRITY TESTS — focused on breaking snapshotting correctness
// ============================================================================

// collectAll drains the logger: creates a snapshot, collects the data strings
// from all returned entries, calls release(true), and returns them. It does NOT
// fatal if there are no entries; callers check the returned slice length.
func collectAll(t *testing.T, bl *gobinlogger.BinLogger) (prev, curr uint64, data []string) {
	t.Helper()
	p, c, ents, rel, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	for _, e := range ents {
		data = append(data, string(e.Data))
	}
	if rel != nil {
		if err := rel(true); err != nil {
			t.Fatalf("release(true): %v", err)
		}
	}
	return p, c, data
}

// restartLogger closes bl, then opens a brand-new BinLogger against the same
// directories and returns it. It registers Close via t.Cleanup.
func restartLogger(t *testing.T, bl *gobinlogger.BinLogger, walDir, snapDir string) *gobinlogger.BinLogger {
	t.Helper()
	if err := bl.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	bl2 := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl2.Close() })
	return bl2
}

// assertEntries checks that the collected data slice exactly matches want,
// in order.
func assertEntries(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: entry count got %d want %d\n  got:  %v\n  want: %v", label, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: entry[%d] got %q want %q", label, i, got[i], want[i])
		}
	}
}

// ----------------------------------------------------------------------------
// 1. No entry must be skipped or duplicated across consecutive snapshot windows
// ----------------------------------------------------------------------------

// TestSnapshot_NoSkipNoDuplicate writes a long sequence of entries spread
// across many snapshot/log cycles and verifies that every entry appears in
// exactly one snapshot window, none are skipped, and none are doubled.
func TestSnapshot_NoSkipNoDuplicate(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	const rounds = 10
	const perRound = 7
	seen := make(map[string]int) // value → count

	for r := 0; r < rounds; r++ {
		// Write a batch.
		batch := make([]string, perRound)
		for i := range batch {
			batch[i] = fmt.Sprintf("round%d-entry%d", r, i)
		}
		mustLog(t, bl, batch...)

		// Snapshot the batch.
		_, _, entries := collectAll(t, bl)
		for _, e := range entries {
			seen[e]++
		}
	}

	// Every entry must appear exactly once.
	total := rounds * perRound
	if len(seen) != total {
		t.Errorf("unique entries seen: got %d want %d", len(seen), total)
	}
	for v, count := range seen {
		if count != 1 {
			t.Errorf("entry %q appeared %d times (want 1)", v, count)
		}
	}
}

// ----------------------------------------------------------------------------
// 2. Entries written concurrently while a snapshot is open must not be lost
//    after restart (the core data-loss race from the analysis)
// ----------------------------------------------------------------------------

// TestSnapshot_ConcurrentLog_NoDataLossAfterRestart starts a snapshot, then
// concurrently writes new entries while the snapshot window is still open.
// After release(true) and a restart, those concurrent entries must still be
// recoverable in the next snapshot.
func TestSnapshot_ConcurrentLog_NoDataLossAfterRestart(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	// Seed some entries so CreateSnapshot has something to work with.
	mustLog(t, bl, "seed-1", "seed-2", "seed-3")

	// Open the snapshot but do NOT call release yet.
	prevIdx, snapIdx, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if release == nil {
		t.Fatal("expected non-nil release")
	}
	if prevIdx != 0 || snapIdx != 3 {
		t.Errorf("seed snapshot indices: got (%d,%d) want (0,3)", prevIdx, snapIdx)
	}
	assertEntries(t, "seed snapshot", func() []string {
		out := make([]string, len(ents))
		for i, e := range ents {
			out[i] = string(e.Data)
		}
		return out
	}(), []string{"seed-1", "seed-2", "seed-3"})

	// Concurrently write entries while the snapshot window is still open.
	const concurrent = 5
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := bl.Log([][]byte{[]byte(fmt.Sprintf("concurrent-%d", i))}); err != nil {
				t.Errorf("concurrent Log: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Now release the snapshot (triggers WAL GC for segments up to index 3).
	if err := release(true); err != nil {
		t.Fatalf("release(true): %v", err)
	}

	// Close and restart to force recovery from disk.
	bl2 := restartLogger(t, bl, walDir, snapDir)

	// The next snapshot must contain exactly the `concurrent` entries written
	// after the seed snapshot. None must be missing.
	_, _, recovered := collectAll(t, bl2)
	if len(recovered) != concurrent {
		t.Errorf("after restart: got %d entries want %d\n  entries: %v", len(recovered), concurrent, recovered)
	}
}

// ----------------------------------------------------------------------------
// 3. Snapshot boundary is exact: entries on the boundary index are not
//    included in the next window (no off-by-one doubling on boundary)
// ----------------------------------------------------------------------------

func TestSnapshot_BoundaryIndex_NoDoubling(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	mustLog(t, bl, "a", "b", "c") // indices 1,2,3
	prev1, curr1, window1 := collectAll(t, bl)

	if prev1 != 0 || curr1 != 3 {
		t.Fatalf("window1 indices: got (%d,%d) want (0,3)", prev1, curr1)
	}
	assertEntries(t, "window1", window1, []string{"a", "b", "c"})

	mustLog(t, bl, "d", "e") // indices 4,5
	prev2, curr2, window2 := collectAll(t, bl)

	if prev2 != 3 || curr2 != 5 {
		t.Fatalf("window2 indices: got (%d,%d) want (3,5)", prev2, curr2)
	}
	assertEntries(t, "window2", window2, []string{"d", "e"})

	// "c" must NOT appear in window2; "d" must NOT appear in window1.
	for _, e := range window1 {
		if e == "d" || e == "e" {
			t.Errorf("window1 contains entry from window2: %q", e)
		}
	}
	for _, e := range window2 {
		if e == "a" || e == "b" || e == "c" {
			t.Errorf("window2 contains entry from window1: %q", e)
		}
	}
}

// ----------------------------------------------------------------------------
// 4. After release(false) the same entries must appear in the next snapshot
//    (WAL not GC'd, no entries silently dropped)
// ----------------------------------------------------------------------------

func TestSnapshot_ReleaseFalse_EntriesRetainedInNextSnapshot(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	mustLog(t, bl, "x", "y", "z")

	// First snapshot: do NOT release WAL (ok=false).
	prev1, curr1, ents1, rel1, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot1: %v", err)
	}
	if prev1 != 0 || curr1 != 3 {
		t.Fatalf("snapshot1 indices: got (%d,%d) want (0,3)", prev1, curr1)
	}
	assertEntries(t, "snapshot1", func() []string {
		out := make([]string, len(ents1))
		for i, e := range ents1 {
			out[i] = string(e.Data)
		}
		return out
	}(), []string{"x", "y", "z"})

	// release(false) — WAL not GC'd.
	if err := rel1(false); err != nil {
		t.Fatalf("release(false): %v", err)
	}

	// Write more entries.
	mustLog(t, bl, "p", "q")

	// Second snapshot: must only contain the new entries (x,y,z already
	// committed to lastSnapIndex=3; they must NOT reappear).
	prev2, curr2, window2 := collectAll(t, bl)

	if prev2 != 3 || curr2 != 5 {
		t.Fatalf("snapshot2 indices: got (%d,%d) want (3,5)", prev2, curr2)
	}
	assertEntries(t, "snapshot2", window2, []string{"p", "q"})

	for _, e := range window2 {
		if e == "x" || e == "y" || e == "z" {
			t.Errorf("snapshot2 contains already-snapshotted entry: %q", e)
		}
	}
}

// ----------------------------------------------------------------------------
// 5. Full restart integrity: every entry logged before Close must be
//    recoverable via CreateSnapshot after restart, with correct indices
// ----------------------------------------------------------------------------

func TestSnapshot_RestartRecovery_CompleteIntegrity(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	// Phase 1: log and snapshot some entries, then log more without snapshotting.
	mustLog(t, bl, "e1", "e2", "e3")
	_, _, _ = collectAll(t, bl) // snapshot indices 0→3, release(true)

	mustLog(t, bl, "e4", "e5") // not yet snapshotted
	mustLog(t, bl, "e6")       // also not snapshotted

	// Restart without snapshotting e4/e5/e6.
	bl2 := restartLogger(t, bl, walDir, snapDir)

	mustLog(t, bl2, "e7", "e8") // written post-restart

	// Now snapshot. Must recover e4..e8.
	prev, curr, recovered := collectAll(t, bl2)

	if prev != 3 {
		t.Errorf("prevSnapIndex after restart: got %d want 3", prev)
	}
	if curr != 8 {
		t.Errorf("currSnapIndex after restart: got %d want 8", curr)
	}
	assertEntries(t, "post-restart snapshot", recovered, []string{"e4", "e5", "e6", "e7", "e8"})
}

// ----------------------------------------------------------------------------
// 6. snapshotMutex prevents two concurrent CreateSnapshot calls from racing:
//    the second must block until release() is called on the first
// ----------------------------------------------------------------------------

func TestSnapshot_MutexBlocksConcurrentSnapshot(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	mustLog(t, bl, "alpha", "beta")

	// Start the first snapshot; hold the release.
	_, _, _, release1, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot1: %v", err)
	}

	// Attempt a second snapshot in a goroutine; it must block.
	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		_, _, _, rel2, err := bl.CreateSnapshot()
		if err != nil {
			t.Errorf("snapshot2: %v", err)
		}
		if rel2 != nil {
			_ = rel2(true)
		}
		close(finished)
	}()

	<-started
	// Give the goroutine time to block on snapshotMutex.
	time.Sleep(50 * time.Millisecond)

	select {
	case <-finished:
		t.Error("second CreateSnapshot returned before first release() was called")
	default:
		// expected: still blocked
	}

	// Release the first snapshot; the second should now unblock.
	if err := release1(true); err != nil {
		t.Fatalf("release1(true): %v", err)
	}

	select {
	case <-finished:
		// expected
	case <-time.After(3 * time.Second):
		t.Fatal("second CreateSnapshot never unblocked after release()")
	}
}

// ----------------------------------------------------------------------------
// 7. Snapshot index stored in lastSnapIndex must match what is recovered
//    on restart (snapshot file and WAL must be consistent)
// ----------------------------------------------------------------------------

func TestSnapshot_IndexConsistencyAcrossRestart(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	mustLog(t, bl, "r1", "r2", "r3", "r4", "r5")

	prev, curr, _ := collectAll(t, bl) // snapshot 0→5
	if prev != 0 || curr != 5 {
		t.Fatalf("snapshot indices: got (%d,%d) want (0,5)", prev, curr)
	}

	mustLog(t, bl, "r6") // un-snapshotted

	bl2 := restartLogger(t, bl, walDir, snapDir)

	// After restart, lastSnapIndex must be 5 (the last committed snapshot).
	// CreateSnapshot must pick up only r6.
	prev2, curr2, entries := collectAll(t, bl2)
	if prev2 != 5 {
		t.Errorf("prevSnapIndex after restart: got %d want 5", prev2)
	}
	if curr2 != 6 {
		t.Errorf("currSnapIndex after restart: got %d want 6", curr2)
	}
	assertEntries(t, "post-restart", entries, []string{"r6"})
}

// ----------------------------------------------------------------------------
// 8. Entries at the exact snapshot boundary index are not double-counted
//    across restart (fence-post check at prevSnapIndex boundary)
// ----------------------------------------------------------------------------

func TestSnapshot_FencePost_BoundaryEntryNotDoubled(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	mustLog(t, bl, "fence-1", "fence-2") // indices 1,2
	_, curr, _ := collectAll(t, bl)      // snapshot 0→2
	if curr != 2 {
		t.Fatalf("snapshot curr: got %d want 2", curr)
	}

	// No more writes; restart.
	bl2 := restartLogger(t, bl, walDir, snapDir)

	// CreateSnapshot must be a no-op (no new entries since last snapshot).
	prevN, currN, ents, rel, err := bl2.CreateSnapshot()
	if err != nil {
		t.Fatalf("no-op snapshot after restart: %v", err)
	}
	if ents != nil || rel != nil {
		t.Errorf("expected no-op snapshot; got prevN=%d currN=%d entries=%v", prevN, currN, ents)
	}
}

// ============================================================================
// DEEP EDGE-CASE & ERROR PATH TESTS — data integrity under unusual conditions
// ============================================================================

// ----------------------------------------------------------------------------
// 9. Nil-data entry: a Log call with one nil-payload element must be written,
//    recovered, and appear in a snapshot without corrupting adjacent entries.
// ----------------------------------------------------------------------------

func TestSnapshot_NilDataEntry_PreservedAndIsolated(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	// Write a real entry, a nil-data entry, then another real entry.
	if err := bl.Log([][]byte{[]byte("before-nil")}); err != nil {
		t.Fatalf("Log before nil: %v", err)
	}
	if err := bl.Log([][]byte{nil}); err != nil {
		t.Fatalf("Log nil data: %v", err)
	}
	if err := bl.Log([][]byte{[]byte("after-nil")}); err != nil {
		t.Fatalf("Log after nil: %v", err)
	}

	_, curr, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	defer release(true)

	if curr != 3 {
		t.Errorf("currSnapIndex: got %d want 3", curr)
	}
	if len(ents) != 3 {
		t.Fatalf("entry count: got %d want 3", len(ents))
	}
	if string(ents[0].Data) != "before-nil" {
		t.Errorf("ents[0]: got %q want %q", ents[0].Data, "before-nil")
	}
	if ents[1].Data != nil {
		t.Errorf("ents[1].Data: got %q want nil", ents[1].Data)
	}
	if string(ents[2].Data) != "after-nil" {
		t.Errorf("ents[2]: got %q want %q", ents[2].Data, "after-nil")
	}
}

// ----------------------------------------------------------------------------
// 10. Nil-data entry survives a full restart with correct index assignment.
// ----------------------------------------------------------------------------

func TestSnapshot_NilDataEntry_SurvivesRestart(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	if err := bl.Log([][]byte{[]byte("pre"), nil, []byte("post")}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	// Snapshot and close without releasing WAL (release true still).
	_, _, _, rel, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := rel(true); err != nil {
		t.Fatalf("release: %v", err)
	}

	mustLog(t, bl, "after-snap")

	bl2 := restartLogger(t, bl, walDir, snapDir)

	_, curr, ents, release2, err := bl2.CreateSnapshot()
	if err != nil {
		t.Fatalf("post-restart snapshot: %v", err)
	}
	defer release2(true)

	// After restart the un-snapshotted entry ("after-snap", index 4) must appear.
	if curr != 4 {
		t.Errorf("currSnapIndex: got %d want 4", curr)
	}
	if len(ents) != 1 || string(ents[0].Data) != "after-snap" {
		t.Errorf("post-restart entries: got %v want [after-snap]", ents)
	}
}

// ----------------------------------------------------------------------------
// 11. Multiple restarts with no new writes between them must be idempotent:
//     lastIndex and lastSnapIndex must remain stable, CreateSnapshot no-op.
// ----------------------------------------------------------------------------

func TestSnapshot_MultipleRestarts_Idempotent(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	mustLog(t, bl, "stable-1", "stable-2", "stable-3")
	_, snapCurr, _ := collectAll(t, bl)
	if snapCurr != 3 {
		t.Fatalf("initial snapshot curr: got %d want 3", snapCurr)
	}

	// Restart 3 times without any writes or snapshots.
	for i := 0; i < 3; i++ {
		bl = restartLogger(t, bl, walDir, snapDir)

		// CreateSnapshot must be a no-op every time.
		_, _, ents, rel, err := bl.CreateSnapshot()
		if err != nil {
			t.Fatalf("restart %d CreateSnapshot: %v", i+1, err)
		}
		if ents != nil || rel != nil {
			t.Errorf("restart %d: expected no-op snapshot, got entries=%v", i+1, ents)
		}

		// A new entry after each restart must get the next monotonic index.
		mustLog(t, bl, fmt.Sprintf("post-restart-%d", i+1))
		prev, curr, entries := collectAll(t, bl)
		wantPrev := uint64(3 + i)
		wantCurr := uint64(4 + i)
		if prev != wantPrev || curr != wantCurr {
			t.Errorf("restart %d snapshot: got (%d,%d) want (%d,%d)", i+1, prev, curr, wantPrev, wantCurr)
		}
		if len(entries) != 1 || entries[0] != fmt.Sprintf("post-restart-%d", i+1) {
			t.Errorf("restart %d entries: %v", i+1, entries)
		}
	}
}

// ----------------------------------------------------------------------------
// 12. Snapshot exactly at index 1 (single first entry) — boundary correctness.
// ----------------------------------------------------------------------------

func TestSnapshot_AtIndexOne(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	mustLog(t, bl, "only-entry")

	prev, curr, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	defer release(true)

	if prev != 0 || curr != 1 {
		t.Errorf("indices: got (%d,%d) want (0,1)", prev, curr)
	}
	if len(ents) != 1 || string(ents[0].Data) != "only-entry" {
		t.Errorf("entries: %v", ents)
	}
	if ents[0].Index != 1 {
		t.Errorf("entry.Index: got %d want 1", ents[0].Index)
	}
}

// ----------------------------------------------------------------------------
// 13. Long snapshot chain (many windows with release(true) each time):
//     no entry must cross a window boundary in either direction.
// ----------------------------------------------------------------------------

func TestSnapshot_LongChain_NoCrossWindowLeakage(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	const windows = 15
	const perWindow = 4

	allWindows := make([][]string, windows)

	for w := 0; w < windows; w++ {
		batch := make([]string, perWindow)
		for i := range batch {
			batch[i] = fmt.Sprintf("w%d-e%d", w, i)
		}
		mustLog(t, bl, batch...)

		_, _, entries := collectAll(t, bl)
		allWindows[w] = entries
	}

	// Verify each window has exactly perWindow entries.
	for w, entries := range allWindows {
		if len(entries) != perWindow {
			t.Errorf("window %d: got %d entries want %d: %v", w, len(entries), perWindow, entries)
		}
	}

	// Verify no entry from window W appears in window W+1 or later.
	for w := 0; w < windows-1; w++ {
		thisSet := make(map[string]bool, len(allWindows[w]))
		for _, e := range allWindows[w] {
			thisSet[e] = true
		}
		for laterW := w + 1; laterW < windows; laterW++ {
			for _, e := range allWindows[laterW] {
				if thisSet[e] {
					t.Errorf("entry %q from window %d reappeared in window %d", e, w, laterW)
				}
			}
		}
	}
}

// ----------------------------------------------------------------------------
// 14. release(true) after the final snapshot must not prevent a subsequent
//     Log+CreateSnapshot cycle from working (WAL GC does not remove live data).
// ----------------------------------------------------------------------------

func TestSnapshot_ReleaseTrue_DoesNotCorruptSubsequentWrites(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	// Snapshot a first batch, release WAL.
	mustLog(t, bl, "batch1-a", "batch1-b")
	_, snap1, _, rel1, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot1: %v", err)
	}
	if err := rel1(true); err != nil {
		t.Fatalf("release1: %v", err)
	}

	// Write a second batch and snapshot — must not be affected by the GC.
	mustLog(t, bl, "batch2-a", "batch2-b", "batch2-c")
	prev2, curr2, ents2, rel2, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot2 after release(true): %v", err)
	}
	defer rel2(true)

	if prev2 != snap1 {
		t.Errorf("snapshot2 prevIndex: got %d want %d", prev2, snap1)
	}
	if curr2 != snap1+3 {
		t.Errorf("snapshot2 currIndex: got %d want %d", curr2, snap1+3)
	}
	assertEntries(t, "snapshot2", func() []string {
		out := make([]string, len(ents2))
		for i, e := range ents2 {
			out[i] = string(e.Data)
		}
		return out
	}(), []string{"batch2-a", "batch2-b", "batch2-c"})
}

// ----------------------------------------------------------------------------
// 15. Snapshot then restart then release(true) on old snapshot: the old
//     release must be safe to call even after the logger has been restarted
//     (release closes over the old storage; after restart a new storage
//     instance exists — the caller must not use the old release after restart).
//     This test documents the expected behaviour: the old release operates on
//     the closed storage and must return an error or no-op, but must not panic.
// ----------------------------------------------------------------------------

func TestSnapshot_OldRelease_AfterRestart_DoesNotPanic(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	mustLog(t, bl, "before-restart")
	_, _, _, oldRelease, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Restart without calling oldRelease first.
	bl2 := restartLogger(t, bl, walDir, snapDir)
	defer bl2.Close()

	// oldRelease now operates on the closed/replaced storage.
	// It must not panic — an error is acceptable.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("oldRelease panicked after restart: %v", r)
		}
	}()
	_ = oldRelease(true) // error is tolerated; panic is not
}

// ----------------------------------------------------------------------------
// 16. Log entry order is preserved exactly across a snapshot+restart cycle,
//     even when logs were written in multiple separate batches.
// ----------------------------------------------------------------------------

func TestSnapshot_EntryOrderPreservedAcrossRestart(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	// Write in three separate batches of different sizes.
	mustLog(t, bl, "a1", "a2", "a3")
	mustLog(t, bl, "b1")
	mustLog(t, bl, "c1", "c2")

	// Snapshot the first 4 entries only.
	bl.Log([][]byte{}) // no-op, indices unchanged
	_, snap1, _, rel, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	_ = rel(true)
	if snap1 != 6 {
		t.Fatalf("snapIndex: got %d want 6", snap1)
	}

	// Write one more batch after the snapshot.
	mustLog(t, bl, "d1", "d2")

	bl2 := restartLogger(t, bl, walDir, snapDir)

	// After restart the un-snapshotted entries (d1, d2) must be recovered
	// in the correct order with the correct indices.
	_, _, ents, release2, err := bl2.CreateSnapshot()
	if err != nil {
		t.Fatalf("post-restart snapshot: %v", err)
	}
	defer release2(true)

	assertEntries(t, "post-restart order", func() []string {
		out := make([]string, len(ents))
		for i, e := range ents {
			out[i] = string(e.Data)
		}
		return out
	}(), []string{"d1", "d2"})

	if len(ents) == 2 {
		if ents[0].Index != 7 {
			t.Errorf("d1 index: got %d want 7", ents[0].Index)
		}
		if ents[1].Index != 8 {
			t.Errorf("d2 index: got %d want 8", ents[1].Index)
		}
	}
}

// ----------------------------------------------------------------------------
// 17. Concurrent Log calls interleaved with a snapshot window must not cause
//     the snapshot's filtered entry list to contain out-of-order indices.
// ----------------------------------------------------------------------------

func TestSnapshot_ConcurrentLog_EntriesInOrder(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	// Seed some entries.
	mustLog(t, bl, "seed-a", "seed-b")

	// Open snapshot window.
	_, _, _, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Concurrently write more entries while the window is open.
	var wg sync.WaitGroup
	const n = 10
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = bl.Log([][]byte{[]byte(fmt.Sprintf("concurrent-%02d", i))})
		}(i)
	}
	wg.Wait()
	_ = release(true)

	// Now snapshot the concurrent entries.
	_, _, ents, rel2, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot2: %v", err)
	}
	if rel2 != nil {
		defer rel2(true)
	}

	// Indices in the returned slice must be strictly monotonically increasing.
	for i := 1; i < len(ents); i++ {
		if ents[i].Index <= ents[i-1].Index {
			t.Errorf("entry indices not strictly increasing at position %d: %d <= %d",
				i, ents[i].Index, ents[i-1].Index)
		}
	}
}

// ----------------------------------------------------------------------------
// 18. Snapshot window captures exactly the entries up to lastIndex at the
//     moment CreateSnapshot was called, not entries added afterward.
// ----------------------------------------------------------------------------

func TestSnapshot_WindowBoundedAtCallTime(t *testing.T) {
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)
	t.Cleanup(func() { bl.Close() })

	mustLog(t, bl, "before-snap-1", "before-snap-2")

	// Open snapshot — captures lastIndex=2 at this moment.
	_, snapCurr, ents, release, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Write more entries AFTER CreateSnapshot captured lastIndex.
	mustLog(t, bl, "after-snap-1", "after-snap-2")

	// The snapshot's entry list must NOT contain the after-snap entries.
	for _, e := range ents {
		if string(e.Data) == "after-snap-1" || string(e.Data) == "after-snap-2" {
			t.Errorf("snapshot contains entry written after CreateSnapshot: %q", e.Data)
		}
	}
	if snapCurr != 2 {
		t.Errorf("snapCurr: got %d want 2", snapCurr)
	}
	_ = release(true)

	// The next snapshot must pick up exactly the after-snap entries.
	prev2, curr2, ents2, rel2, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot2: %v", err)
	}
	if rel2 != nil {
		defer rel2(true)
	}
	if prev2 != 2 || curr2 != 4 {
		t.Errorf("snapshot2 indices: got (%d,%d) want (2,4)", prev2, curr2)
	}
	assertEntries(t, "snapshot2", func() []string {
		out := make([]string, len(ents2))
		for i, e := range ents2 {
			out[i] = string(e.Data)
		}
		return out
	}(), []string{"after-snap-1", "after-snap-2"})
}

// ----------------------------------------------------------------------------
// 19. Stress: many concurrent goroutines log, interleaved with a snapshot
//     window, then restart. Total entry count must equal exactly what was
//     written with no duplicates or gaps across the restart boundary.
// ----------------------------------------------------------------------------

func TestSnapshot_StressConcurrentLogAndRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	walDir, snapDir := setupDirs(t)
	bl := newStarted(t, walDir, snapDir)

	const preWriters = 8
	const prePerWriter = 5
	const postWriters = 6
	const postPerWriter = 4

	// Phase 1: write concurrently, then snapshot.
	var wg1 sync.WaitGroup
	for i := 0; i < preWriters; i++ {
		wg1.Add(1)
		go func(i int) {
			defer wg1.Done()
			for j := 0; j < prePerWriter; j++ {
				if err := bl.Log([][]byte{[]byte(fmt.Sprintf("pre-%d-%d", i, j))}); err != nil {
					t.Errorf("pre Log: %v", err)
				}
			}
		}(i)
	}
	wg1.Wait()

	_, snap1Curr, _, rel1, err := bl.CreateSnapshot()
	if err != nil {
		t.Fatalf("snapshot1: %v", err)
	}
	expected1 := preWriters * prePerWriter
	if int(snap1Curr) != expected1 {
		t.Errorf("snap1Curr: got %d want %d", snap1Curr, expected1)
	}
	_ = rel1(true)

	// Phase 2: write more concurrently, then close without snapshotting.
	var wg2 sync.WaitGroup
	for i := 0; i < postWriters; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			for j := 0; j < postPerWriter; j++ {
				if err := bl.Log([][]byte{[]byte(fmt.Sprintf("post-%d-%d", i, j))}); err != nil {
					t.Errorf("post Log: %v", err)
				}
			}
		}(i)
	}
	wg2.Wait()

	// Restart.
	bl2 := restartLogger(t, bl, walDir, snapDir)

	// After restart the post-phase entries must all be present.
	_, snap2Curr, recovered := collectAll(t, bl2)

	expected2 := postWriters * postPerWriter
	if len(recovered) != expected2 {
		t.Errorf("post-restart entry count: got %d want %d", len(recovered), expected2)
	}
	if int(snap2Curr) != expected1+expected2 {
		t.Errorf("snap2Curr: got %d want %d", snap2Curr, expected1+expected2)
	}

	// No entry from phase 1 may appear in the phase 2 recovery.
	for _, e := range recovered {
		if len(e) >= 3 && e[:3] == "pre" {
			t.Errorf("phase-1 entry %q appeared in post-restart snapshot", e)
		}
	}

	// All post-phase entries must be present exactly once.
	seen := make(map[string]int, expected2)
	for _, e := range recovered {
		seen[e]++
	}
	for i := 0; i < postWriters; i++ {
		for j := 0; j < postPerWriter; j++ {
			key := fmt.Sprintf("post-%d-%d", i, j)
			if seen[key] != 1 {
				t.Errorf("entry %q: count %d want 1", key, seen[key])
			}
		}
	}
}
