# go-binlogger

A high-performance, crash-safe binary logger for Go using etcd's Write-Ahead Log (WAL).

## Features

- **Crash-safe persistence**: Built on etcd's battle-tested WAL implementation
- **Atomic operations**: Thread-safe logging with atomic index management
- **Snapshot support**: Efficient recovery and log compaction
- **Flexible panic modes**: Optional data dumping on failures
- **Zero-copy design**: Minimal allocations for high throughput

## Installation

```bash
go get github.com/HariKube/go-binlogger
```

## Quick Start

```go
package main

import (
    "context"
    "sync"
    "time"

    gobinlogger "github.com/HariKube/go-binlogger/pkg/binlogger"
)

func main() {
    // Create a new binary logger
    logger := gobinlogger.NewBinLogger(
        "/tmp/wal",           // WAL directory
        "/tmp/snap",          // Snapshot directory
        0,                    // Sync interval (0 = sync after each operation)
    )

    // Start the logger
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    var wg sync.WaitGroup
    logger.MustStart(ctx, &wg)

    // Log some data
    data := [][]byte{
        []byte("first entry"),
        []byte("second entry"),
        []byte("third entry"),
    }
    logger.MustLog(data)

    // Create a snapshot (returns entries since last snapshot)
    prevIndex, currentIndex, entries, release := logger.MustCreateSnapshot()
    
    // Process the entries...
    for _, entry := range entries {
        // Do something with entry.Data
    }
    
    // Release lock to allow WAL cleanup (panics on error)
    release()

    wg.Wait()
}
```

## API Reference

### Creating a Logger

```go
func NewBinLogger(walDir, snapDir string, syncInterval time.Duration) *BinLogger
```

- `walDir`: Directory for Write-Ahead Log files
- `snapDir`: Directory for snapshot files
- `syncInterval`: How often to sync WAL to disk (0 = sync after each operation, recommended for crash safety)

### Starting the Logger

```go
func (bl *BinLogger) Start(ctx context.Context, wg ...*sync.WaitGroup) error
```

Initializes the WAL and recovers from the last snapshot (if exists). Registers a cleanup goroutine that syncs and closes the log when the context is cancelled.

```go
func (bl *BinLogger) MustStart(ctx context.Context, wg ...*sync.WaitGroup)
```

Same as `Start()` but panics on error.

### Logging Data

```go
func (bl *BinLogger) Log(data [][]byte) error
```

Atomically logs a batch of entries. Returns `ErrIndexOverflow` if adding entries would exceed `math.MaxUint64`.

```go
func (bl *BinLogger) MustLog(data [][]byte)
```

Same as `Log()` but panics on error.

### Creating Snapshots

```go
func (bl *BinLogger) CreateSnapshot() (prevIndex uint64, currentIndex uint64, entries []raftpb.Entry, release func() error, err error)
```

Creates a snapshot at the current index and returns:
- `prevIndex`: Index of the previous snapshot (0 if this is the first)
- `currentIndex`: Index of the newly created snapshot
- `entries`: All entries since last snapshot
- `release`: Callback to release snapshot and allow cleanup of old WAL files
- `err`: Error if snapshot creation fails

**Important**: Call `release()` after successfully processing the entries to allow WAL to delete old segment files.

```go
func (bl *BinLogger) MustCreateSnapshot() (prevIndex uint64, currentIndex uint64, entries []raftpb.Entry, release func() error)
```

Same as `CreateSnapshot()` but panics on error. The returned `release` function also panics on error.

### Closing the Logger

```go
func (bl *BinLogger) Close() error
```

Syncs and closes the WAL. This releases the WAL lock file and ensures all data is persisted to disk. Call this before application shutdown to ensure clean restart.

### Example Usage

```go
// With error handling
prevIdx, currIdx, entries, release, err := logger.CreateSnapshot()
if err != nil {
    log.Fatalf("Failed to create snapshot: %v", err)
}

// Process entries
var err error
for _, entry := range entries {
    if err = processEntry(entry); err != nil {
        break
    }
}

// Release lock after successful processing
if err := release(err != nil); err != nil {
    log.Fatalf("Failed to release lock: %v", err)
}

// Or with Must variant (panics on error)
prevIdx, currIdx, entries, release := logger.MustCreateSnapshot()

// Process entries
var err error
for _, entry := range entries {
    if err = processEntry(entry); err != nil {
        break
    }
}

release(err != nil)  // panic on error
```

## How Snapshots Work

Snapshots provide efficient recovery and log compaction:

1. **First snapshot** (index 0 → 100):
   - Returns all entries from index 1 to 100
   
2. **Second snapshot** (index 100 → 250):
   - Returns entries from 101 to 250
   - On restart, WAL only reads entries after 250

3. **Recovery**:
   - Logger reads the last snapshot metadata
   - Only replays entries after that snapshot
   - Much faster than replaying the entire log

## Thread Safety

All operations are thread-safe:
- `Log()` uses atomic compare-and-swap for index allocation
- `CreateSnapshot()` uses a mutex to prevent concurrent snapshot creation
- Multiple goroutines can call `Log()` concurrently

## Error Handling

The logger provides both error-returning and panic-on-error variants:

```go
// Error handling
if err := logger.Log(data); err != nil {
    if errors.Is(err, gobinlogger.ErrIndexOverflow) {
        // Handle overflow
    }
}

// Panic on error (useful for critical paths)
logger.MustLog(data)  // Panics if logging fails
```

## Performance Considerations

- **Batch logging**: Always log multiple entries at once for better throughput
- **Snapshot frequency**: Balance between recovery time and snapshot overhead
- **WAL rotation**: etcd WAL automatically rotates at ~64MB per segment
- **Sync interval**: Set to 0 for maximum crash safety (sync after each operation), or use a duration like `100*time.Millisecond` for higher throughput at the cost of potentially losing recent entries on crash
- **Cleanup**: Call `release()` after processing snapshot entries to allow WAL to delete old segments and free disk space

## 🤝 Contribution Guide

We welcome and encourage contributions from the community! Whether it's a bug fix, a new feature, or an improvement to the documentation, your help is greatly appreciated.

Before you get started, please take a moment to review our guidelines:

- Read the Documentation: Familiarize yourself with the framework's architecture and existing features.
- Open an Issue: For any significant changes or new features, please open an issue first to discuss the idea. This helps prevent duplicated work and ensures alignment with the project's goals.
- Fork the Repository: Fork the repository to your own GitHub account.
- Create a Branch: Create a new branch for your feature or bug fix: git checkout -b feature-my-awesome-feature.
- Commit Your Changes: Make your changes and commit them with a clear and descriptive message.
- Submit a Pull Request: Push your branch to your forked repository and open a pull request against the main branch of this repository. Please provide a clear description of your changes in the PR.

We are committed to providing a friendly, safe, and welcoming environment for all, regardless of background or experience. We are following Kubernetes Please see them [Code of Conduct](https://kubernetes.io/community/code-of-conduct/) for more details.

## 🙏 Share Feedback and Report Issues

Your feedback is invaluable in helping us improve this framework. If you encounter any issues, have a suggestion for a new feature, or simply want to share your experience, we want to hear from you!

- Report Bugs: If you find a bug, please open a [GitHub Issue](https://github.com/mhmxs/go-binlogger/issues). Include as much detail as possible, such as steps to reproduce the bug, expected behavior, and your environment (e.g., Kubernetes version, Go version).
- Request a Feature: If you have an idea for a new feature, open a [GitHub Issue](https://github.com/mhmxs/go-binlogger/issues) and use the `enhancement` label. Describe the use case and how the new feature would benefit the community.
- Ask a Question: For general questions or discussions, please use the [GitHub Discussions](https://github.com/mhmxs/go-binlogger/discussions).

## 📝 License

This project is licensed under the BSD 3-Clause "New" or "Revised" License. See the LICENSE file for details.

