//go:build linux

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

func TestVerifyUpgradeAsset(t *testing.T) {
	data := []byte("agent binary")
	digest := sha256.Sum256(data)
	line := fmt.Sprintf("%s  network-observer-agent-linux-amd64\n", hex.EncodeToString(digest[:]))
	if err := verifyUpgradeAsset([]byte(line), "network-observer-agent-linux-amd64", data); err != nil {
		t.Fatalf("verify valid asset: %v", err)
	}
	cases := []struct {
		name, manifest string
		data           []byte
	}{
		{name: "missing", manifest: "", data: data},
		{name: "digest mismatch", manifest: fmt.Sprintf("%064x  network-observer-agent-linux-amd64\n", 0), data: data},
		{name: "malformed entry", manifest: "not-a-digest  network-observer-agent-linux-amd64\n", data: data},
		{name: "duplicate asset", manifest: line + line, data: data},
		{name: "path traversal name", manifest: fmt.Sprintf("%s  ../network-observer-agent-linux-amd64\n", hex.EncodeToString(digest[:])), data: data},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyUpgradeAsset([]byte(test.manifest), "network-observer-agent-linux-amd64", test.data); err == nil {
				t.Fatal("expected manifest validation to fail")
			}
		})
	}
}
