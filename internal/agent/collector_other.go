//go:build !linux

package agent

import (
	"context"
	"errors"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

type unsupportedCollector struct{}

func newPlatformCollector() Collector                 { return unsupportedCollector{} }
func newPlatformCollectorWithConfig(Config) Collector { return unsupportedCollector{} }
func newGatewayCollectorWithConfig(Config) Collector  { return unsupportedCollector{} }

func (unsupportedCollector) Snapshot(context.Context) ([]*networkobserverv1.FlowObservation, []*networkobserverv1.Capability, error) {
	return nil, nil, errors.New("endpoint collection is currently available only on Linux")
}
