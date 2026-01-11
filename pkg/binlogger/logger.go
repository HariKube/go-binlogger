package gobinlogger

import (
	"context"
	"fmt"
	"math"
	"os"
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
	ErrIndexOverflow = fmt.Errorf("BinLogger index overflow")

	raftpbHardState = raftpb.HardState{}
)

type BinLogger struct {
	walDir        string
	snapDir       string
	index         atomic.Uint64
	lastSnapIndex atomic.Uint64
	logMutex      sync.Mutex
	snapshotter   *snap.Snapshotter
	syncInterval  time.Duration
	storage       storage.Storage
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

	var w *wal.WAL
	var err error
	if wal.Exist(bl.walDir) {
		snaps, err := os.ReadDir(bl.snapDir)
		if err != nil {
			return fmt.Errorf("failed to read snapshots dir at %s: %v", bl.snapDir, err)
		}

		index := uint64(0)
		if len(snaps) > 0 {
			if !strings.HasSuffix(snaps[len(snaps)-1].Name(), ".snap") {
				return fmt.Errorf("invalid latest snapshot file found at %s: %s", bl.snapDir, snaps[len(snaps)-1].Name())
			}

			parts := strings.Split(strings.TrimSuffix(snaps[len(snaps)-1].Name(), ".snap"), "-")
			if len(parts) != 2 {
				return fmt.Errorf("invalid latest snapshot file name found at %s: %s", bl.snapDir, snaps[len(snaps)-1].Name())
			}

			if index, err = strconv.ParseUint(parts[1], 10, 64); err != nil {
				return fmt.Errorf("failed to parse snapshot file name %s: %v", snaps[len(snaps)-1].Name(), err)
			}
		}

		walSnap := walpb.Snapshot{
			Index: index,
		}

		bl.index.Store(index)
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
			bl.index.Store(ents[len(ents)-1].Index)
		}
	} else {
		w, err = wal.Create(zap.NewNop(), bl.walDir, nil)
		if err != nil {
			return fmt.Errorf("failed to create missing revisions log at %s: %v", bl.walDir, err)
		}
	}

	bl.storage = storage.NewStorage(zap.NewNop(), w, bl.snapshotter)

	syncOnce := sync.Once{}
	for _, w := range wg {
		w.Add(1)
		go func(w *sync.WaitGroup) {
			defer w.Done()
			<-ctx.Done()

			syncOnce.Do(func() {
				if err := bl.Close(); err != nil {
					println("Failed to close binlog at %s: %v", bl.walDir, err)
				}
			})
		}(w)
	}

	if bl.syncInterval > 0 {
		go func() {
			ticker := time.NewTicker(bl.syncInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := bl.storage.Sync(); err != nil {
						println("Failed to sync binlog at %s: %v", bl.walDir, err)
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
	var currentIndex uint64
	for {
		currentIndex = bl.index.Load()
		if math.MaxUint64-currentIndex < uint64(len(data)) {
			return ErrIndexOverflow
		}

		newIndex := currentIndex + uint64(len(data))
		if bl.index.CompareAndSwap(currentIndex, newIndex) {
			break
		}
	}

	entries := make([]raftpb.Entry, len(data))
	for i := range data {
		currentIndex++
		entries[i] = raftpb.Entry{
			Index: currentIndex,
			Type:  raftpb.EntryNormal,
			Data:  data[i],
		}
	}

	if err := bl.storage.Save(raftpbHardState, entries); err != nil {
		return fmt.Errorf("failed to log entries to %s: %v", bl.walDir, err)
	}

	if bl.syncInterval == 0 {
		if err := bl.storage.Sync(); err != nil {
			return fmt.Errorf("failed to sync WAL after snapshot: %v", err)
		}
	}

	return nil
}

func (bl *BinLogger) MustLog(data [][]byte) {
	if err := bl.Log(data); err != nil {
		panic(err)
	}
}

func (bl *BinLogger) Close() error {
	if err := bl.storage.Sync(); err != nil {
		return fmt.Errorf("failed to sync storage: %v", err)
	}

	if err := bl.storage.Close(); err != nil {
		return fmt.Errorf("failed to close storage: %v", err)
	}

	return nil
}

func (bl *BinLogger) CreateSnapshot() (uint64, uint64, []raftpb.Entry, func(bool) error, error) {
	bl.logMutex.Lock()

	prevSnapIndex := bl.lastSnapIndex.Load()
	currentIndex := bl.index.Load()

	if prevSnapIndex >= currentIndex {
		bl.logMutex.Unlock()
		return 0, 0, nil, nil, nil
	}

	prevWalSnapshot := walpb.Snapshot{Index: prevSnapIndex}
	w, err := wal.OpenForRead(zap.NewNop(), bl.walDir, prevWalSnapshot)
	if err != nil {
		bl.logMutex.Unlock()
		return 0, 0, nil, nil, err
	}
	defer func() {
		if err := w.Close(); err != nil {
			fmt.Printf("failed to close wal reader: %v", err)
		}
	}()

	_, _, ents, err := w.ReadAll()
	if err != nil {
		bl.logMutex.Unlock()
		return 0, 0, nil, nil, err
	}

	walSnapshot := walpb.Snapshot{
		Index: currentIndex,
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
		bl.logMutex.Unlock()
		return 0, 0, nil, nil, fmt.Errorf("failed to save raft snapshot to %s (%d - %d): %v", bl.snapDir, prevWalSnapshot.Index, walSnapshot.Index, err)
	}
	bl.lastSnapIndex.Store(walSnapshot.Index)

	if bl.syncInterval == 0 {
		if err := bl.storage.Sync(); err != nil {
			bl.logMutex.Unlock()
			return 0, 0, nil, nil, fmt.Errorf("failed to sync WAL after snapshot: %v", err)
		}
	}

	releaseFn := func(ok bool) error {
		defer bl.logMutex.Unlock()

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
