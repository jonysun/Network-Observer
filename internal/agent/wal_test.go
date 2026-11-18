package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

func TestLargeWALSmallAcknowledgementsDoNotRewritePendingQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.wal")
	wal, err := OpenWAL(path, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	items := make([]*pb.FlowObservation, 6000)
	for i := range items {
		items[i] = testObservation("flow")
		items[i].AttributionReason = strings.Repeat("x", 1000)
	}
	if _, err := wal.AppendBatch(items); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if before.Size() <= walCompactThreshold {
		t.Fatal("fixture is too small to reproduce the large-WAL compaction bug")
	}
	if err := wal.AckThrough(500); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("acknowledging 500 records rewrote the entire large WAL")
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	wal, err = OpenWAL(path, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if pending := wal.Pending(1); len(pending) != 1 || pending[0].Sequence != 501 {
		t.Fatal("restart lost the acknowledged checkpoint")
	}
	if err := wal.AckThrough(5000); err != nil {
		t.Fatal(err)
	}
	compacted, _ := os.Stat(path)
	if compacted.Size() >= before.Size()/2 {
		t.Fatal("substantial acknowledged space was not reclaimed")
	}
	if pending := wal.Pending(0); len(pending) != 1000 || pending[0].Sequence != 5001 || pending[999].Sequence != 6000 {
		t.Fatal("compaction changed pending records or their order")
	}
}

func TestWALReclaimsConfirmedSpaceBeforeRejectingAppend(t *testing.T) {
	wal, err := OpenWAL(filepath.Join(t.TempDir(), "agent.wal"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	items := []*pb.FlowObservation{testObservation("one"), testObservation("two"), testObservation("three")}
	for _, item := range items {
		item.AttributionReason = strings.Repeat("x", 1000)
	}
	if _, err := wal.AppendBatch(items); err != nil {
		t.Fatal(err)
	}
	_, used, _, _, _ := wal.Stats()
	wal.maxBytes = used // Start with no spare space, as on the affected NAS.
	if err := wal.AckThrough(2); err != nil {
		t.Fatal(err)
	}
	if count, err := wal.AppendBatch([]*pb.FlowObservation{testObservation("four")}); err != nil || count != 1 {
		t.Fatalf("append after ACK count=%d err=%v", count, err)
	}
	if pending := wal.Pending(0); len(pending) != 2 || pending[0].Sequence != 3 || pending[1].Sequence != 4 {
		t.Fatal("space reclamation lost records or sequence continuity")
	}
}

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

func TestWALVolatileReconcileReportsUnknownGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.wal")
	wal, err := OpenWAL(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	gap, err := wal.ReconcileWithMode(8, true)
	if err != nil || !gap {
		t.Fatalf("volatile reconcile gap=%v err=%v, want true/nil", gap, err)
	}
	if err := wal.RecordGap(0, "test volatile restart"); err != nil {
		t.Fatal(err)
	}
	_, _, _, acked, assigned := wal.Stats()
	if acked != 7 || assigned != 7 {
		t.Fatalf("checkpoint = %d/%d, want 7/7", acked, assigned)
	}
}

func TestWALDefaultReconcileStillRejectsMissingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.wal")
	wal, err := OpenWAL(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if _, err := wal.ReconcileWithMode(8, false); err == nil {
		t.Fatal("expected persistent WAL to reject missing records")
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
