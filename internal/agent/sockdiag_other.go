//go:build !linux

package agent

import (
	"context"
	"errors"
)

type diagStats struct{}

func collectTCPSocketDiag(context.Context) (map[uint64]diagStats, error) {
	return nil, errors.New("socket diag is supported only on Linux")
}
