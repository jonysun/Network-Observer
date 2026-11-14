//go:build linux

package agent

import (
	"fmt"
	"os"

	"github.com/cilium/ebpf"
)

// probeEBPF verifies the kernel prerequisites needed by the CO-RE collector.
// The repository currently ships the Socket Diag fallback; until a generated
// CO-RE object is installed this probe deliberately remains unavailable rather
// than claiming exact eBPF bytes from a zero counter.
func probeEBPF() (string, string) {
	objectPath := "/usr/local/lib/network-observer/flow_counter.o"
	if _, err := os.Stat(objectPath); err != nil {
		return "unavailable", "generated eBPF object is not installed"
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
		return "unsupported", "kernel BTF is unavailable"
	}
	if _, err := os.Stat("/sys/fs/bpf"); err != nil {
		return "degraded", "bpffs is not mounted"
	}
	m, err := ebpf.NewMap(&ebpf.MapSpec{Name: "network_observer_probe", Type: ebpf.Hash, KeySize: 4, ValueSize: 8, MaxEntries: 1})
	if err != nil {
		return "degraded", fmt.Sprintf("eBPF map creation denied: %v", err)
	}
	_ = m.Close()
	return "unavailable", "kernel prerequisites are present but no generated CO-RE program is attached; Socket Diag remains the fallback"
}
