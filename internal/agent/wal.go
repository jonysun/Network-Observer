package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
	"google.golang.org/protobuf/proto"
)

var ErrWALFull = errors.New("agent WAL capacity reached")
var ErrWALTooOld = errors.New("oldest unacknowledged observation exceeded WAL age limit")

const walCompactThreshold = 4 << 20

type walRecord struct {
	Kind        string                             `json:"kind"`
	Sequence    uint64                             `json:"sequence,omitempty"`
	Observation *networkobserverv1.FlowObservation `json:"observation,omitempty"`
}

type walEntry struct {
	sequence            uint64
	offset              int64
	size                int64
	observedAtUnixNanos int64
}

type WAL struct {
	mu           sync.Mutex
	path         string
	maxBytes     int64
	file         *os.File
	lock         *os.File
	bytes        int64
	lastAcked    uint64
	lastAssigned uint64
	pending      []walEntry

	reclaimable int64
	gaps        GapStatus
}

type GapStatus struct {
	Dropped uint64    `json:"dropped_observations"`
	First   time.Time `json:"first_gap_at"`
	Last    time.Time `json:"last_gap_at"`
	Reason  string    `json:"reason"`
}

func OpenWAL(path string, maxBytes int64) (*WAL, error) {
	if path == "" {
		return nil, errors.New("WAL path is required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("WAL max bytes must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create WAL directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(lock); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("another agent owns WAL %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("open WAL: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("secure WAL permissions: %w", err)
	}
	wal := &WAL{path: path, maxBytes: maxBytes, file: file, lock: lock}
	if err := wal.replay(); err != nil {
		_ = file.Close()
		_ = lock.Close()
		return nil, err
	}
	if data, err := os.ReadFile(path + ".gaps"); err == nil {
		if err := json.Unmarshal(data, &wal.gaps); err != nil {
			_ = wal.Close()
			return nil, fmt.Errorf("read WAL gap status: %w", err)
		}
	} else if !os.IsNotExist(err) {
		_ = wal.Close()
		return nil, err
	}
	return wal, nil
}

func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lock != nil {
		defer w.lock.Close()
		w.lock = nil
	}
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *WAL) AppendBatch(observations []*networkobserverv1.FlowObservation) (int, error) {
	if len(observations) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, errors.New("WAL is closed")
	}
	if len(w.pending) != 0 && time.Since(time.Unix(0, w.pending[0].observedAtUnixNanos)) > 24*time.Hour {
		return 0, ErrWALTooOld
	}
	var encoded []byte
	accepted := make([]walEntry, 0, len(observations))
	sequence := w.lastAssigned
	full := false
	compacted := false
	for _, observation := range observations {
		if observation == nil {
			continue
		}
		copyOfObservation := proto.Clone(observation).(*networkobserverv1.FlowObservation)
		sequence++
		copyOfObservation.Sequence = sequence
		line, err := json.Marshal(walRecord{Kind: "observation", Sequence: sequence, Observation: copyOfObservation})
		if err != nil {
			return 0, fmt.Errorf("encode WAL observation: %w", err)
		}
		line = append(line, '\n')
		if w.bytes+int64(len(encoded)+len(line)) > w.maxBytes && w.reclaimable > 0 && !compacted {
			// Reclaim on demand before rejecting new data. Ordinary ACKs wait
			// until a substantial part of the file is reclaimable.
			if err := w.compactLocked(); err != nil {
				return 0, err
			}
			compacted = true
		}
		if w.bytes+int64(len(encoded)+len(line)) > w.maxBytes {
			full = true
			break
		}
		encoded = append(encoded, line...)
		accepted = append(accepted, walEntry{
			sequence: sequence, size: int64(len(line)), observedAtUnixNanos: copyOfObservation.ObservedAtUnixNanos,
		})
	}
	if len(encoded) == 0 && full {
		return 0, ErrWALFull
	}
	if len(encoded) != 0 {
		if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
			return 0, err
		}
		if _, err := w.file.Write(encoded); err != nil {
			return 0, fmt.Errorf("append WAL observations: %w", err)
		}
		if err := w.file.Sync(); err != nil {
			return 0, fmt.Errorf("sync WAL observations: %w", err)
		}
		offset := w.bytes
		for index := range accepted {
			accepted[index].offset = offset
			offset += accepted[index].size
		}
		w.bytes += int64(len(encoded))
		w.lastAssigned = accepted[len(accepted)-1].sequence
		w.pending = append(w.pending, accepted...)
	}
	if full {
		return len(accepted), ErrWALFull
	}
	return len(accepted), nil
}

func (w *WAL) Pending(limit int) ([]*networkobserverv1.FlowObservation, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pendingLocked(limit)
}

func (w *WAL) pendingLocked(limit int) ([]*networkobserverv1.FlowObservation, error) {
	if len(w.pending) != 0 && w.file == nil {
		return nil, errors.New("WAL is closed")
	}
	if limit <= 0 || limit > len(w.pending) {
		limit = len(w.pending)
	}
	result := make([]*networkobserverv1.FlowObservation, 0, limit)
	if limit == 0 {
		return result, nil
	}
	first := w.pending[0]
	last := w.pending[limit-1]
	length := last.offset + last.size - first.offset
	data := make([]byte, length)
	if _, err := w.file.ReadAt(data, first.offset); err != nil {
		return nil, fmt.Errorf("read WAL observations %d-%d: %w", first.sequence, last.sequence, err)
	}
	for i := 0; i < limit; i++ {
		entry := w.pending[i]
		start := entry.offset - first.offset
		observation, err := decodeWALObservation(entry, data[start:start+entry.size])
		if err != nil {
			return nil, err
		}
		result = append(result, observation)
	}
	return result, nil
}

func (w *WAL) AckThrough(sequence uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ackThroughLocked(sequence)
}

func (w *WAL) Reconcile(serverNextSequence uint64) error {
	_, err := w.ReconcileWithMode(serverNextSequence, false)
	return err
}

// ReconcileWithMode validates the Center checkpoint. A volatile WAL may be
// empty after a router reboot; only that explicit mode may advance past the
// missing local records, which the caller reports as a data gap.
func (w *WAL) ReconcileWithMode(serverNextSequence uint64, volatile bool) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if serverNextSequence == 0 {
		return false, errors.New("server returned an invalid next sequence")
	}
	serverAck := serverNextSequence - 1
	if serverAck < w.lastAcked {
		return false, fmt.Errorf("server sequence regressed from acknowledged %d to %d", w.lastAcked, serverAck)
	}
	if serverAck > w.lastAssigned {
		if volatile && len(w.pending) == 0 && w.lastAssigned == 0 {
			w.lastAcked = serverAck
			w.lastAssigned = serverAck
			return serverAck > 0, nil
		}
		return false, fmt.Errorf("server acknowledged %d beyond local WAL %d; refusing to silently skip lost state", serverAck, w.lastAssigned)
	}
	if serverAck > w.lastAcked {
		if err := w.ackThroughLocked(serverAck); err != nil {
			return false, err
		}
	}
	if len(w.pending) != 0 && w.pending[0].sequence != serverNextSequence {
		return false, fmt.Errorf("WAL sequence gap: server expects %d, WAL starts at %d", serverNextSequence, w.pending[0].sequence)
	}
	if len(w.pending) == 0 && w.lastAssigned+1 != serverNextSequence {
		return false, fmt.Errorf("WAL sequence gap: server expects %d, next local sequence is %d", serverNextSequence, w.lastAssigned+1)
	}
	return false, nil
}

// UnderPressure reports whether durable capacity is still exhausted after
// accounting for acknowledged bytes that can be reclaimed by compaction. It is
// used only to gate the bounded in-memory fallback; a healthy WAL never causes
// history eviction.
func (w *WAL) UnderPressure() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil || len(w.pending) == 0 {
		return false
	}
	if time.Since(time.Unix(0, w.pending[0].observedAtUnixNanos)) > 24*time.Hour {
		return true
	}
	return w.bytes-w.reclaimable >= w.maxBytes
}

func (w *WAL) Stats() (pending int, bytes int64, oldest *networkobserverv1.FlowObservation, lastAcked, lastAssigned uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != 0 {
		oldest = &networkobserverv1.FlowObservation{ObservedAtUnixNanos: w.pending[0].observedAtUnixNanos}
	}
	return len(w.pending), w.bytes, oldest, w.lastAcked, w.lastAssigned
}

func (w *WAL) ackThroughLocked(sequence uint64) error {
	if sequence <= w.lastAcked {
		return nil
	}
	if sequence > w.lastAssigned {
		return fmt.Errorf("cannot acknowledge sequence %d; last assigned is %d", sequence, w.lastAssigned)
	}
	if w.file == nil {
		return errors.New("WAL is closed")
	}
	line, err := json.Marshal(walRecord{Kind: "ack", Sequence: sequence})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := w.file.Write(line); err != nil {
		return fmt.Errorf("append WAL acknowledgement: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync WAL acknowledgement: %w", err)
	}
	w.bytes += int64(len(line))
	w.reclaimable += int64(len(line))
	w.lastAcked = sequence
	w.releaseThrough(sequence)
	if len(w.pending) == 0 || (w.reclaimable >= walCompactThreshold && w.reclaimable >= w.bytes/2) {
		return w.compactLocked()
	}
	return nil
}

// releaseThrough runs under the WAL mutex (or during initial replay). Slice
// off the acknowledged prefix instead of copying the entire remaining queue.
func (w *WAL) releaseThrough(sequence uint64) {
	n := 0
	for n < len(w.pending) && w.pending[n].sequence <= sequence {
		w.reclaimable += w.pending[n].size
		n++
	}
	w.pending = w.pending[n:]
}

func (w *WAL) replay() error {
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(w.file)
	var validBytes int64
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				// Only a newline-terminated record was committed. Preserve every complete record.
				if err := w.file.Truncate(validBytes); err != nil {
					return err
				}
				if err := w.file.Sync(); err != nil {
					return err
				}
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read WAL: %w", err)
		}
		recordOffset := validBytes
		validBytes += int64(len(line))
		var record walRecord
		if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
			return fmt.Errorf("decode WAL record: %w", err)
		}
		switch record.Kind {
		case "observation":
			if record.Observation == nil || record.Sequence == 0 || record.Observation.Sequence != record.Sequence || record.Sequence != w.lastAssigned+1 {
				return fmt.Errorf("invalid WAL observation sequence %d", record.Sequence)
			}
			w.lastAssigned = record.Sequence
			if record.Sequence > w.lastAcked {
				w.pending = append(w.pending, walEntry{
					sequence: record.Sequence, offset: recordOffset, size: int64(len(line)),
					observedAtUnixNanos: record.Observation.ObservedAtUnixNanos,
				})
			}
		case "ack":
			if record.Sequence < w.lastAcked || (w.lastAssigned != 0 && record.Sequence > w.lastAssigned) {
				return fmt.Errorf("invalid WAL acknowledgement sequence %d", record.Sequence)
			}
			w.lastAcked = record.Sequence
			if record.Sequence > w.lastAssigned {
				w.lastAssigned = record.Sequence
			}
			w.reclaimable += int64(len(line))
			w.releaseThrough(record.Sequence)
		default:
			return fmt.Errorf("unknown WAL record kind %q", record.Kind)
		}
	}
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	w.bytes = info.Size()
	if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return nil
}

func (w *WAL) RecordGap(count uint64, reason string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	next := w.gaps
	next.Dropped += count
	next.Last = time.Now().UTC()
	if next.First.IsZero() {
		next.First = next.Last
	}
	next.Reason = reason
	if err := writePrivateJSON(w.path+".gaps", next); err != nil {
		return err
	}
	w.gaps = next
	return nil
}

func (w *WAL) GapStatus() GapStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gaps
}

func (w *WAL) compactLocked() error {
	temporary := w.path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create compacted WAL: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	writer := bufio.NewWriterSize(file, 256<<10)
	entries := make([]walEntry, 0, len(w.pending))
	position := int64(0)
	writeRecord := func(record walRecord) error {
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		_, err = writer.Write(line)
		if record.Kind == "observation" {
			entries = append(entries, walEntry{
				sequence: record.Sequence, offset: position, size: int64(len(line)),
				observedAtUnixNanos: record.Observation.ObservedAtUnixNanos,
			})
		}
		position += int64(len(line))
		return err
	}
	if w.lastAcked != 0 {
		if err := writeRecord(walRecord{Kind: "ack", Sequence: w.lastAcked}); err != nil {
			_ = file.Close()
			return err
		}
	}
	for _, entry := range w.pending {
		observation, err := w.pendingEntryLocked(entry)
		if err != nil {
			_ = file.Close()
			return err
		}
		if err := writeRecord(walRecord{Kind: "observation", Sequence: entry.sequence, Observation: observation}); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, w.path); err != nil {
		w.file, _ = os.OpenFile(w.path, os.O_CREATE|os.O_RDWR, 0o600)
		return fmt.Errorf("replace compacted WAL: %w", err)
	}
	w.file, err = os.OpenFile(w.path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("reopen compacted WAL: %w", err)
	}
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	w.bytes = info.Size()
	w.pending = entries
	w.reclaimable = 0
	return nil
}

func (w *WAL) pendingEntryLocked(entry walEntry) (*networkobserverv1.FlowObservation, error) {
	line := make([]byte, entry.size)
	if _, err := w.file.ReadAt(line, entry.offset); err != nil {
		return nil, fmt.Errorf("read WAL observation %d: %w", entry.sequence, err)
	}
	return decodeWALObservation(entry, line)
}

func decodeWALObservation(entry walEntry, line []byte) (*networkobserverv1.FlowObservation, error) {
	var record walRecord
	if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
		return nil, fmt.Errorf("decode WAL observation %d: %w", entry.sequence, err)
	}
	if record.Kind != "observation" || record.Sequence != entry.sequence || record.Observation == nil || record.Observation.Sequence != entry.sequence {
		return nil, fmt.Errorf("invalid WAL observation at sequence %d", entry.sequence)
	}
	return record.Observation, nil
}
