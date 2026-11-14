//go:build linux

package agent

import (
	"encoding/binary"
	"testing"
)

func TestParseTCPInfoCounters(t *testing.T) {
	info := make([]byte, 200)
	binary.LittleEndian.PutUint64(info[120:128], 1234)
	binary.LittleEndian.PutUint64(info[192:200], 5678)
	sent, received, ok := parseTCPInfo(info)
	if !ok || sent != 5678 || received != 1234 {
		t.Fatalf("unexpected TCP counters sent=%d received=%d ok=%t", sent, received, ok)
	}
}

func TestParseDiagMessageWithTCPInfo(t *testing.T) {
	payload := make([]byte, 72+4+200)
	payload[1] = 1
	binary.LittleEndian.PutUint32(payload[40:44], 2)
	binary.LittleEndian.PutUint32(payload[68:72], 99)
	binary.LittleEndian.PutUint16(payload[72:74], 204)
	binary.LittleEndian.PutUint16(payload[74:76], inetDiagInfo)
	binary.LittleEndian.PutUint64(payload[76+120:76+128], 222)
	binary.LittleEndian.PutUint64(payload[76+192:76+200], 111)
	result := make(map[uint64]diagStats)
	if err := parseDiagMessage(payload, result); err != nil {
		t.Fatal(err)
	}
	stats, ok := result[99]
	if !ok || stats.InterfaceIndex != 2 || !stats.HasCounters || stats.BytesSent != 111 || stats.BytesReceived != 222 {
		t.Fatalf("unexpected diag stats: %+v", stats)
	}
}

func TestCounterDeltaAndRate(t *testing.T) {
	if got := tcpCounterDelta(10, 12); got != 0 {
		t.Fatalf("counter reset should not create negative delta: %d", got)
	}
	if got := ratePerSecond(100, 2e9); got != 50 {
		t.Fatalf("rate=%d, want 50", got)
	}
}
