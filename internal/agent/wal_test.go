package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func testObservation(id string) *pb.FlowObservation {
	return &pb.FlowObservation{Id: id, FlowId: id, ObservedAtUnixNanos: time.Now().UnixNano(), Protocol: "tcp", SourceIp: "127.0.0.1", DestinationIp: "127.0.0.1", Active: true}
}

func TestWALPersistsPendingRecordsAndAcknowledgements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.wal")
	wal, err := OpenWAL(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("one"), testObservation("two")}); err != nil || count != 2 {
		t.Fatalf("append count=%d err=%v", count, err)
	}
	if err := wal.AckThrough(1); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenWAL(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending := reopened.Pending(0)
	if len(pending) != 1 || pending[0].Sequence != 2 || pending[0].Id != "two" {
		t.Fatalf("unexpected pending records: %+v", pending)
	}
	_, _, _, acked, assigned := reopened.Stats()
	if acked != 1 || assigned != 2 {
		t.Fatalf("unexpected sequence state acked=%d assigned=%d", acked, assigned)
	}
}

func TestWALReconcileRejectsServerStateThatWouldSkipLocalRecords(t *testing.T) {
	wal, err := OpenWAL(filepath.Join(t.TempDir(), "agent.wal"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if _, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("one")}); err != nil {
		t.Fatal(err)
	}
	if err := wal.Reconcile(4); err == nil {
		t.Fatal("expected server sequence gap error")
	}
}

func TestWALCapacityRecordsDataGapAndKeepsAcceptedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.wal")
	wal, err := OpenWAL(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	count, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("one"), testObservation("two"), testObservation("three")})
	if !errors.Is(err, ErrWALFull) || count == 0 || count >= 3 {
		t.Fatalf("expected partial WAL append, count=%d err=%v", count, err)
	}
	if err := wal.RecordGap(uint64(3-count), "WAL full"); err != nil {
		t.Fatal(err)
	}
	gap := wal.GapStatus()
	if gap.Dropped != uint64(3-count) || gap.Reason != "WAL full" {
		t.Fatalf("unexpected gap status: %+v", gap)
	}
}

func TestWALDropsUncommittedTailAfterInterruptedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.wal")
	wal, err := OpenWAL(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("one")}); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"kind\":\"observation\"}"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reopened, err := OpenWAL(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if pending := reopened.Pending(0); len(pending) != 1 || pending[0].Id != "one" {
		t.Fatalf("unexpected replay after partial tail: %+v", pending)
	}
}
