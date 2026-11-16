package agent

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

type procSocket struct {
	Protocol   string
	LocalIP    net.IP
	LocalPort  uint16
	RemoteIP   net.IP
	RemotePort uint16
	State      string
	Inode      uint64
}

func parseProcSocketTable(input io.Reader, family, protocol string) ([]procSocket, error) {
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 4096), 1<<20)
	result := make([]procSocket, 0)
	lineNumber := 0
	for reader.Scan() {
		lineNumber++
		line := strings.TrimSpace(reader.Text())
		if line == "" || strings.HasPrefix(line, "sl ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return nil, fmt.Errorf("malformed proc socket line %d", lineNumber)
		}
		localIP, localPort, err := parseProcAddressPort(fields[1], family)
		if err != nil {
			return nil, fmt.Errorf("parse local address on line %d: %w", lineNumber, err)
		}
		remoteIP, remotePort, err := parseProcAddressPort(fields[2], family)
		if err != nil {
			return nil, fmt.Errorf("parse remote address on line %d: %w", lineNumber, err)
		}
		state, err := strconv.ParseUint(fields[3], 16, 8)
		if err != nil {
			return nil, fmt.Errorf("parse state on line %d: %w", lineNumber, err)
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse inode on line %d: %w", lineNumber, err)
		}
		// /proc also lists listeners and unconnected UDP sockets. They have no
		// peer tuple, so presenting their wildcard local bind as a remote endpoint
		// would fabricate a connection in the Center.
		if protocol == "tcp" && (state == 0x07 || state == 0x0a) {
			continue
		}
		if !hasSocketPeer(remoteIP, remotePort) {
			continue
		}
		result = append(result, procSocket{
			Protocol: protocol, LocalIP: localIP, LocalPort: localPort,
			RemoteIP: remoteIP, RemotePort: remotePort, State: procSocketState(protocol, state), Inode: inode,
		})
	}
	if err := reader.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func hasSocketPeer(ip net.IP, port uint16) bool {
	return ip != nil && !ip.IsUnspecified() && port != 0
}

func parseProcAddressPort(value, family string) (net.IP, uint16, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return nil, 0, fmt.Errorf("invalid address %q", value)
	}
	port, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid port %q", parts[1])
	}
	encoded, err := hex.DecodeString(parts[0])
	if err != nil {
		return nil, 0, fmt.Errorf("invalid address bytes %q", parts[0])
	}
	switch family {
	case "ipv4":
		if len(encoded) != net.IPv4len {
			return nil, 0, fmt.Errorf("IPv4 address must contain 8 hex digits")
		}
		for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
			encoded[left], encoded[right] = encoded[right], encoded[left]
		}
	case "ipv6":
		if len(encoded) != net.IPv6len {
			return nil, 0, fmt.Errorf("IPv6 address must contain 32 hex digits")
		}
		for offset := 0; offset < len(encoded); offset += 4 {
			word := binary.LittleEndian.Uint32(encoded[offset : offset+4])
			binary.BigEndian.PutUint32(encoded[offset:offset+4], word)
		}
	default:
		return nil, 0, fmt.Errorf("unsupported address family %q", family)
	}
	return net.IP(encoded), uint16(port), nil
}

func procTCPState(state uint64) string {
	switch state {
	case 0x01:
		return "ESTABLISHED"
	case 0x02:
		return "SYN_SENT"
	case 0x03:
		return "SYN_RECV"
	case 0x04:
		return "FIN_WAIT1"
	case 0x05:
		return "FIN_WAIT2"
	case 0x06:
		return "TIME_WAIT"
	case 0x07:
		return "CLOSE"
	case 0x08:
		return "CLOSE_WAIT"
	case 0x09:
		return "LAST_ACK"
	case 0x0a:
		return "LISTEN"
	case 0x0b:
		return "CLOSING"
	case 0x0c:
		return "NEW_SYN_RECV"
	default:
		return fmt.Sprintf("UNKNOWN_%02X", state)
	}
}

func procSocketState(protocol string, state uint64) string {
	if protocol == "udp" {
		switch state {
		case 0x07:
			return "UNCONNECTED"
		case 0x01:
			return "ESTABLISHED"
		default:
			return fmt.Sprintf("UNKNOWN_%02X", state)
		}
	}
	return procTCPState(state)
}
