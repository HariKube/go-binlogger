package gobinlogger_test

import (
	"context"
	"os"
	"testing"
	"time"

	gobinlogger "github.com/harikube/go-binlogger/pkg/binlogger"
)

func TestBinLogger(t *testing.T) {
	tmpWal, err := os.MkdirTemp("", "wal")
	if err != nil {
		t.Fatalf("failed to create temp wal dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpWal); err != nil {
			t.Fatalf("failed to remove temp snap dir: %v", err)
		}
	}()

	tmpSnap, err := os.MkdirTemp("", "snap")
	if err != nil {
		t.Fatalf("failed to create temp snap dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpSnap); err != nil {
			t.Fatalf("failed to remove temp snap dir: %v", err)
		}
	}()

	binLogger := gobinlogger.NewBinLogger(tmpWal, tmpSnap, 0)
	if err := binLogger.Start(context.Background()); err != nil {
		t.Fatalf("failed to start bin logger: %v", err)
	}

	data := [][]byte{
		[]byte("first entry"),
		[]byte("second entry"),
		[]byte("third entry"),
	}

	if err := binLogger.Log(data); err != nil {
		t.Fatalf("failed to log data: %v", err)
	}

	prevIndex, currIndex, entries, release, err := binLogger.CreateSnapshot()
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	if prevIndex != 0 || currIndex != 3 {
		t.Fatalf("unexpected snapshot indices: got (%d, %d), want (0, 3)", prevIndex, currIndex)
	}

	if len(entries) != 3 {
		t.Fatalf("unexpected number of entries in snapshot: got %d, want 3", len(entries))
	} else if string(entries[0].Data) != "first entry" || string(entries[1].Data) != "second entry" || string(entries[2].Data) != "third entry" {
		t.Fatalf("unexpected snapshot entries data")
	}

	if err := release(true); err != nil {
		t.Fatalf("failed to release snapshot lock: %v", err)
	}

	data2 := [][]byte{
		[]byte("fourth entry"),
		[]byte("fifth entry"),
	}

	if err := binLogger.Log(data2); err != nil {
		t.Fatalf("failed to log data: %v", err)
	}

	prevIndex, currIndex, entries, release, err = binLogger.CreateSnapshot()
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	if prevIndex != 3 || currIndex != 5 {
		t.Fatalf("unexpected snapshot indices: got (%d, %d), want (3, 5)", prevIndex, currIndex)
	}

	if len(entries) != 2 {
		t.Fatalf("unexpected number of entries in snapshot: got %d, want 2", len(entries))
	} else if string(entries[0].Data) != "fourth entry" || string(entries[1].Data) != "fifth entry" {
		t.Fatalf("unexpected snapshot entries data")
	}

	if err := release(true); err != nil {
		t.Fatalf("failed to release snapshot lock: %v", err)
	}

	data3 := [][]byte{
		[]byte("sixth entry"),
	}

	if err := binLogger.Log(data3); err != nil {
		t.Fatalf("failed to log data: %v", err)
	}

	if err := binLogger.Close(); err != nil {
		t.Fatalf("failed to close bin logger: %v", err)
	}

	defer func() {
		<-time.After(time.Second)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	binLogger = gobinlogger.NewBinLogger(tmpWal, tmpSnap, 0)
	if err := binLogger.Start(ctx); err != nil {
		t.Fatalf("failed to start bin logger: %v", err)
	}

	data4 := [][]byte{
		[]byte("seventh entry"),
	}

	if err := binLogger.Log(data4); err != nil {
		t.Fatalf("failed to log data: %v", err)
	}

	prevIndex, currIndex, entries, release, err = binLogger.CreateSnapshot()
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	if prevIndex != 5 || currIndex != 7 {
		t.Fatalf("unexpected snapshot indices: got (%d, %d), want (5, 7)", prevIndex, currIndex)
	}

	if len(entries) != 2 {
		t.Fatalf("unexpected number of entries in snapshot: got %d, want 2", len(entries))
	} else if string(entries[0].Data) != "sixth entry" || string(entries[1].Data) != "seventh entry" {
		t.Fatalf("unexpected snapshot entries data")
	}

	if err := release(true); err != nil {
		t.Fatalf("failed to release snapshot lock: %v", err)
	}
}
