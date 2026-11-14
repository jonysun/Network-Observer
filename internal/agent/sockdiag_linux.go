//go:build linux

package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

const (
	sockDiagByFamily = 20
	inetDiagInfo     = 2
	nlmsgNoop        = 1
	nlmsgError       = 2
	nlmsgDone        = 3
	nlmFRequest      = 1
	nlmFDump         = 0x300
)

type diagStats struct {
	InterfaceIndex uint32
	State          string
	BytesSent      uint64
	BytesReceived  uint64
	HasCounters    bool
}

func collectTCPSocketDiag(ctx context.Context) (map[uint64]diagStats, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return nil, fmt.Errorf("open socket diag netlink: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("bind socket diag netlink: %w", err)
	}
	result := make(map[uint64]diagStats)
	for _, family := range []uint8{unix.AF_INET, unix.AF_INET6} {
		if err := requestDiagDump(ctx, fd, family, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func requestDiagDump(ctx context.Context, fd int, family uint8, result map[uint64]diagStats) error {
	request := make([]byte, 16+56)
	binary.LittleEndian.PutUint32(request[0:4], uint32(len(request)))
	binary.LittleEndian.PutUint16(request[4:6], sockDiagByFamily)
	binary.LittleEndian.PutUint16(request[6:8], nlmFRequest|nlmFDump)
	binary.LittleEndian.PutUint32(request[8:12], 1)
	request[16] = family
	request[17] = unix.IPPROTO_TCP
	request[18] = 1 << (inetDiagInfo - 1)
	binary.LittleEndian.PutUint32(request[20:24], ^uint32(0))
	if err := unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("send socket diag request: %w", err)
	}
	buffer := make([]byte, 256*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		read, _, err := unix.Recvfrom(fd, buffer, 0)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("receive socket diag response: %w", err)
		}
		if done, err := parseDiagNetlinkMessages(buffer[:read], result); err != nil {
			return err
		} else if done {
			return nil
		}
	}
}

func parseDiagNetlinkMessages(data []byte, result map[uint64]diagStats) (bool, error) {
	for offset := 0; offset+16 <= len(data); {
		length := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if length < 16 || offset+length > len(data) {
			return false, errors.New("malformed socket diag netlink message")
		}
		messageType := binary.LittleEndian.Uint16(data[offset+4 : offset+6])
		payload := data[offset+16 : offset+length]
		switch messageType {
		case nlmsgDone:
			return true, nil
		case nlmsgError:
			if len(payload) < 4 {
				return false, errors.New("socket diag netlink error without errno")
			}
			errno := int32(binary.LittleEndian.Uint32(payload[:4]))
			if errno != 0 {
				return false, unix.Errno(-errno)
			}
			return true, nil
		case nlmsgNoop:
			// Ignore padding/no-op messages.
		default:
			if err := parseDiagMessage(payload, result); err != nil {
				return false, err
			}
		}
		aligned := (length + 3) &^ 3
		offset += aligned
	}
	return false, nil
}

func parseDiagMessage(payload []byte, result map[uint64]diagStats) error {
	// inet_diag_msg is 72 bytes before its netlink attributes.
	if len(payload) < 72 {
		return errors.New("short socket diag message")
	}
	state := payload[1]
	interfaceIndex := binary.LittleEndian.Uint32(payload[40:44])
	inode := binary.LittleEndian.Uint32(payload[68:72])
	if inode == 0 {
		return nil
	}
	stats := diagStats{InterfaceIndex: interfaceIndex, State: procTCPState(uint64(state))}
	for offset := 72; offset+4 <= len(payload); {
		length := int(binary.LittleEndian.Uint16(payload[offset : offset+2]))
		attributeType := binary.LittleEndian.Uint16(payload[offset+2 : offset+4])
		if length < 4 || offset+length > len(payload) {
			return errors.New("malformed socket diag attribute")
		}
		if attributeType == inetDiagInfo {
			stats.BytesSent, stats.BytesReceived, stats.HasCounters = parseTCPInfo(payload[offset+4 : offset+length])
		}
		offset += (length + 3) &^ 3
	}
	result[uint64(inode)] = stats
	return nil
}

func parseTCPInfo(info []byte) (bytesSent, bytesReceived uint64, ok bool) {
	// These fields were added to Linux tcp_info after the original fixed header.
	// Refuse short structures rather than interpreting unrelated fields as counters.
	if len(info) < 200 {
		return 0, 0, false
	}
	bytesReceived = binary.LittleEndian.Uint64(info[120:128])
	bytesSent = binary.LittleEndian.Uint64(info[192:200])
	return bytesSent, bytesReceived, true
}

func tcpCounterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func ratePerSecond(delta uint64, elapsed time.Duration) uint64 {
	if delta == 0 || elapsed <= 0 {
		return 0
	}
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		return 0
	}
	return uint64(float64(delta) / seconds)
}

func interfaceName(index uint32) string {
	if index == 0 {
		return ""
	}
	iface, err := net.InterfaceByIndex(int(index))
	if err != nil {
		return ""
	}
	return iface.Name
}
