//go:build linux

package agent

import (
	"fmt"
	"os"

	"github.com/cilium/ebpf"
)

// probeEBPF verifies the kernel prerequisites needed by the cgroup-skb
// collector. The object is packaged in the Agent image; native installations
// may provide it at the same path or configure an alternate path. Returning
// unavailable here lets newPlatformCollectorWithConfig attempt the real
// object load and cgroup attach before advertising support.
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
	return "unavailable", "kernel prerequisites are present; attempting to load and attach the packaged eBPF object"
}
