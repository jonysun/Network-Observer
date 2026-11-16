package agent

import (
	"strings"
	"testing"
)

func TestParseProcSocketIPv4(t *testing.T) {
	input := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 020000C0:0050 01 00000000:00000000 00:00000000 00000000 1000 0 12345\n"
	sockets, err := parseProcSocketTable(strings.NewReader(input), "ipv4", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(sockets) != 1 {
		t.Fatalf("got %d sockets, want 1", len(sockets))
	}
	got := sockets[0]
	if got.LocalIP.String() != "127.0.0.1" || got.LocalPort != 8080 || got.RemoteIP.String() != "192.0.0.2" || got.RemotePort != 80 {
		t.Fatalf("unexpected IPv4 tuple: %+v", got)
	}
	if got.State != "ESTABLISHED" || got.Inode != 12345 {
		t.Fatalf("unexpected socket metadata: %+v", got)
	}
}

func TestParseProcSocketIPv6FiltersListenerAndClosedSocket(t *testing.T) {
	input := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000000000000000000000000000:0035 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 76\n" +
		"   1: 00000000000000000000000001000000:1F90 00000000000000000000000002000000:0050 01 00000000:00000000 00:00000000 00000000 1000 0 77\n" +
		"   2: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000 1000 0 78\n"
	sockets, err := parseProcSocketTable(strings.NewReader(input), "ipv6", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(sockets) != 1 {
		t.Fatalf("got %d sockets, want only the connected socket", len(sockets))
	}
	got := sockets[0]
	if got.LocalIP.String() != "::1" || got.RemoteIP.String() != "::2" || got.LocalPort != 8080 || got.RemotePort != 80 || got.State != "ESTABLISHED" {
		t.Fatalf("unexpected IPv6 tuple: %+v", got)
	}
}

func TestParseProcSocketIPv4UDPFiltersUnconnectedAndKeepsPeer(t *testing.T) {
	input := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:14E9 00000000:0000 07 00000000:00000000 00:00000000 00000000 1000 0 91\n" +
		"   1: 0A1FA8C0:14E9 351FA8C0:0035 01 00000000:00000000 00:00000000 00000000 1000 0 92\n"
	sockets, err := parseProcSocketTable(strings.NewReader(input), "ipv4", "udp")
	if err != nil {
		t.Fatal(err)
	}
	if len(sockets) != 1 {
		t.Fatalf("got %d sockets, want only the UDP socket with a peer", len(sockets))
	}
	got := sockets[0]
	if got.LocalIP.String() != "192.168.31.10" || got.LocalPort != 5353 || got.RemoteIP.String() != "192.168.31.53" || got.RemotePort != 53 {
		t.Fatalf("unexpected UDP tuple: %+v", got)
	}
}

func TestParseProcSocketRejectsMalformedLine(t *testing.T) {
	if _, err := parseProcSocketTable(strings.NewReader("0: bad\n"), "ipv4", "udp"); err == nil {
		t.Fatal("expected malformed proc row error")
	}
}
