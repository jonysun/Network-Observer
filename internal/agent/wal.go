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

type WAL struct {
	mu           sync.Mutex
	path         string
	maxBytes     int64
	file         *os.File
	lock         *os.File
	bytes        int64
	lastAcked    uint64
	lastAssigned uint64
	pending      []*networkobserverv1.FlowObservation
	gaps         GapStatus
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
	if len(w.pending) != 0 && time.Since(time.Unix(0, w.pending[0].ObservedAtUnixNanos)) > 24*time.Hour {
		return 0, ErrWALTooOld
	}
	var encoded []byte
	accepted := make([]*networkobserverv1.FlowObservation, 0, len(observations))
	sequence := w.lastAssigned
	full := false
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
		if w.bytes+int64(len(encoded)+len(line)) > w.maxBytes {
			full = true
			break
		}
		encoded = append(encoded, line...)
		accepted = append(accepted, copyOfObservation)
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
		w.bytes += int64(len(encoded))
		w.lastAssigned = accepted[len(accepted)-1].Sequence
		w.pending = append(w.pending, accepted...)
	}
	if full {
		return len(accepted), ErrWALFull
	}
	return len(accepted), nil
}

func (w *WAL) Pending(limit int) []*networkobserverv1.FlowObservation {
	w.mu.Lock()
	defer w.mu.Unlock()
	if limit <= 0 || limit > len(w.pending) {
		limit = len(w.pending)
	}
	result := make([]*networkobserverv1.FlowObservation, limit)
	for i := 0; i < limit; i++ {
		result[i] = proto.Clone(w.pending[i]).(*networkobserverv1.FlowObservation)
	}
	return result
}

func (w *WAL) AckThrough(sequence uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ackThroughLocked(sequence)
}

func (w *WAL) Reconcile(serverNextSequence uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if serverNextSequence == 0 {
		return errors.New("server returned an invalid next sequence")
	}
	serverAck := serverNextSequence - 1
	if serverAck < w.lastAcked {
		return fmt.Errorf("server sequence regressed from acknowledged %d to %d", w.lastAcked, serverAck)
	}
	if serverAck > w.lastAssigned {
		return fmt.Errorf("server acknowledged %d beyond local WAL %d; refusing to silently skip lost state", serverAck, w.lastAssigned)
	}
	if serverAck > w.lastAcked {
		if err := w.ackThroughLocked(serverAck); err != nil {
			return err
		}
	}
	if len(w.pending) != 0 && w.pending[0].Sequence != serverNextSequence {
		return fmt.Errorf("WAL sequence gap: server expects %d, WAL starts at %d", serverNextSequence, w.pending[0].Sequence)
	}
	if len(w.pending) == 0 && w.lastAssigned+1 != serverNextSequence {
		return fmt.Errorf("WAL sequence gap: server expects %d, next local sequence is %d", serverNextSequence, w.lastAssigned+1)
	}
	return nil
}

func (w *WAL) Stats() (pending int, bytes int64, oldest *networkobserverv1.FlowObservation, lastAcked, lastAssigned uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != 0 {
		oldest = proto.Clone(w.pending[0]).(*networkobserverv1.FlowObservation)
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
	w.lastAcked = sequence
	firstPending := 0
	for firstPending < len(w.pending) && w.pending[firstPending].Sequence <= sequence {
		firstPending++
	}
	w.pending = append([]*networkobserverv1.FlowObservation(nil), w.pending[firstPending:]...)
	if w.bytes >= walCompactThreshold || len(w.pending) == 0 {
		return w.compactLocked()
	}
	return nil
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
				w.pending = append(w.pending, record.Observation)
			}
		case "ack":
			if record.Sequence < w.lastAcked || (w.lastAssigned != 0 && record.Sequence > w.lastAssigned) {
				return fmt.Errorf("invalid WAL acknowledgement sequence %d", record.Sequence)
			}
			w.lastAcked = record.Sequence
			if record.Sequence > w.lastAssigned {
				w.lastAssigned = record.Sequence
			}
			firstPending := 0
			for firstPending < len(w.pending) && w.pending[firstPending].Sequence <= record.Sequence {
				firstPending++
			}
			w.pending = append([]*networkobserverv1.FlowObservation(nil), w.pending[firstPending:]...)
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
	writeRecord := func(record walRecord) error {
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		_, err = file.Write(line)
		return err
	}
	if w.lastAcked != 0 {
		if err := writeRecord(walRecord{Kind: "ack", Sequence: w.lastAcked}); err != nil {
			_ = file.Close()
			return err
		}
	}
	for _, observation := range w.pending {
		if err := writeRecord(walRecord{Kind: "observation", Sequence: observation.Sequence, Observation: observation}); err != nil {
			_ = file.Close()
			return err
		}
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
	return nil
}
