package gobinlogger

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/storage"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"
)

var (
	// ErrIndexOverflow is returned when the WAL entry index overflows uint64.
	ErrIndexOverflow = fmt.Errorf("BinLogger index overflow")

	raftpbHardState = raftpb.HardState{}
)

type BinLogger struct {
	walDir       string
	snapDir      string
	syncInterval time.Duration

	logMutex      sync.Mutex
	lastIndex     atomic.Uint64
	snapshotMutex sync.Mutex
	lastSnapIndex atomic.Uint64
	snapshotter   *snap.Snapshotter
	storage       storage.Storage

	// closeOnce ensures Close is idempotent even when called concurrently
	// (e.g. direct call and context-cancellation goroutine racing).
	closeOnce sync.Once
}

func NewBinLogger(walDir, snapDir string, syncInterval time.Duration) *BinLogger {
	return &BinLogger{
		walDir:       walDir,
		snapDir:      snapDir,
		syncInterval: syncInterval,
	}
}

func (bl *BinLogger) Start(ctx context.Context, wg ...*sync.WaitGroup) error {
	bl.snapshotter = snap.New(zap.NewNop(), bl.snapDir)
	// Reset closeOnce so a restarted BinLogger can be closed again.
	bl.closeOnce = sync.Once{}

	var w *wal.WAL
	var err error
	if wal.Exist(bl.walDir) {
		snaps, err := os.ReadDir(bl.snapDir)
		if err != nil {
			return fmt.Errorf("failed to read snapshots dir at %s: %v", bl.snapDir, err)
		}

		index := uint64(0)
		if len(snaps) > 0 {
			// Filter to only .snap files and sort them numerically by index so
			// that we always pick the highest-index snapshot regardless of
			// term number (lexicographic order is wrong for multi-digit indices).
			type snapEntry struct {
				name  string
				index uint64
			}
			var snapFiles []snapEntry
			for _, s := range snaps {
				if !strings.HasSuffix(s.Name(), ".snap") {
					continue
				}
				parts := strings.Split(strings.TrimSuffix(s.Name(), ".snap"), "-")
				if len(parts) != 2 {
					return fmt.Errorf("invalid snapshot file name found at %s: %s", bl.snapDir, s.Name())
				}
				idx, parseErr := strconv.ParseUint(parts[1], 16, 64)
				if parseErr != nil {
					return fmt.Errorf("failed to parse snapshot file name %s: %v", s.Name(), parseErr)
				}
				snapFiles = append(snapFiles, snapEntry{name: s.Name(), index: idx})
			}

			if len(snapFiles) > 0 {
				// Sort ascending by index; take the last (highest) one.
				sort.Slice(snapFiles, func(i, j int) bool {
					return snapFiles[i].index < snapFiles[j].index
				})
				index = snapFiles[len(snapFiles)-1].index
			}
		}

		walSnap := walpb.Snapshot{
			Index: index,
		}

		bl.lastIndex.Store(index)
		bl.lastSnapIndex.Store(index)

		w, err = wal.Open(zap.NewNop(), bl.walDir, walSnap)
		if err != nil {
			return fmt.Errorf("failed to open missing revisions log at %s: %v", bl.walDir, err)
		}

		_, _, ents, err := w.ReadAll()
		if err != nil {
			return fmt.Errorf("failed to read missing revisions log at %s: %v", bl.walDir, err)
		}

		if len(ents) > 0 {
			bl.lastIndex.Store(ents[len(ents)-1].Index)
		}
	} else {
		w, err = wal.Create(zap.NewNop(), bl.walDir, nil)
		if err != nil {
			return fmt.Errorf("failed to create missing revisions log at %s: %v", bl.walDir, err)
		}
	}

	bl.storage = storage.NewStorage(zap.NewNop(), w, bl.snapshotter)

	for _, wg := range wg {
		wg.Add(1)
		go func(wg *sync.WaitGroup) {
			defer wg.Done()
			<-ctx.Done()

			bl.closeOnce.Do(func() {
				if err := bl.close(); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to close binlog at %s: %v\n", bl.walDir, err)
				}
			})
		}(wg)
	}

	if bl.syncInterval > 0 {
		go func() {
			ticker := time.NewTicker(bl.syncInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := bl.storage.Sync(); err != nil {
						fmt.Fprintf(os.Stderr, "Failed to sync binlog at %s: %v\n", bl.walDir, err)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	return nil
}

func (bl *BinLogger) MustStart(ctx context.Context, wg ...*sync.WaitGroup) {
	if err := bl.Start(ctx, wg...); err != nil {
		panic(err)
	}
}

func (bl *BinLogger) Log(data [][]byte) error {
	bl.logMutex.Lock()
	defer bl.logMutex.Unlock()

	entries := make([]raftpb.Entry, len(data))
	for i := range data {
		entries[i] = raftpb.Entry{
			Index: bl.lastIndex.Add(1),
			Type:  raftpb.EntryNormal,
			Data:  data[i],
		}
	}

	if err := bl.storage.Save(raftpbHardState, entries); err != nil {
		return fmt.Errorf("failed to log entries to %s: %v", bl.walDir, err)
	}

	if bl.syncInterval == 0 {
		if err := bl.storage.Sync(); err != nil {
			return fmt.Errorf("failed to sync WAL after log: %v", err)
		}
	}

	return nil
}

func (bl *BinLogger) MustLog(data [][]byte) {
	if err := bl.Log(data); err != nil {
		panic(err)
	}
}

// close is the internal implementation; it is called at most once via closeOnce.
func (bl *BinLogger) close() error {
	if err := bl.storage.Sync(); err != nil {
		return fmt.Errorf("failed to sync storage: %v", err)
	}

	if err := bl.storage.Close(); err != nil {
		return fmt.Errorf("failed to close storage: %v", err)
	}

	return nil
}

// Close syncs and closes the underlying WAL storage. It is safe to call
// multiple times; subsequent calls are no-ops.
func (bl *BinLogger) Close() error {
	var closeErr error
	bl.closeOnce.Do(func() {
		closeErr = bl.close()
	})
	return closeErr
}

func (bl *BinLogger) CreateSnapshot() (uint64, uint64, []raftpb.Entry, func(bool) error, error) {
	bl.snapshotMutex.Lock()

	lastIndex := bl.lastIndex.Load()
	prevSnapIndex := bl.lastSnapIndex.Load()

	if prevSnapIndex >= lastIndex {
		bl.snapshotMutex.Unlock()
		return 0, 0, nil, nil, nil
	}

	prevWalSnapshot := walpb.Snapshot{Index: prevSnapIndex}
	w, err := wal.OpenForRead(zap.NewNop(), bl.walDir, prevWalSnapshot)
	if err != nil {
		bl.snapshotMutex.Unlock()
		return 0, 0, nil, nil, err
	}

	_, _, ents, err := w.ReadAll()
	if err != nil {
		bl.snapshotMutex.Unlock()
		return 0, 0, nil, nil, err
	}

	walSnapshot := walpb.Snapshot{
		Index: lastIndex,
	}

	var filtered []raftpb.Entry
	for _, ent := range ents {
		if ent.Index <= walSnapshot.Index {
			filtered = append(filtered, ent)
		}
	}

	snashot := raftpb.Snapshot{
		Metadata: raftpb.SnapshotMetadata{
			Index: walSnapshot.Index,
		},
		Data: nil,
	}

	if err := bl.storage.SaveSnap(snashot); err != nil {
		bl.snapshotMutex.Unlock()
		return 0, 0, nil, nil, fmt.Errorf("failed to save raft snapshot to %s (%d - %d): %v", bl.snapDir, prevWalSnapshot.Index, walSnapshot.Index, err)
	}
	bl.lastSnapIndex.Store(walSnapshot.Index)

	if bl.syncInterval == 0 {
		if err := bl.storage.Sync(); err != nil {
			bl.snapshotMutex.Unlock()
			return 0, 0, nil, nil, fmt.Errorf("failed to sync WAL after snapshot: %v", err)
		}
	}

	releaseFn := func(ok bool) error {
		defer bl.snapshotMutex.Unlock()

		if ok {
			return bl.storage.Release(snashot)
		}

		return nil
	}

	return prevSnapIndex, walSnapshot.Index, filtered, releaseFn, nil
}

func (bl *BinLogger) MustCreateSnapshot() (uint64, uint64, []raftpb.Entry, func(bool) error) {
	prevSnapshot, snapshot, entries, release, err := bl.CreateSnapshot()
	if err != nil {
		panic(err)
	}

	return prevSnapshot, snapshot, entries, func(ok bool) error {
		if release != nil {
			if err := release(ok); err != nil {
				panic(fmt.Errorf("failed to release lock to %s (%d - %d): %v", bl.snapDir, prevSnapshot, snapshot, err))
			}
		}

		return nil
	}
}
