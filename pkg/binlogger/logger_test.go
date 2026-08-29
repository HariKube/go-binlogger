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
