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
	return nil, []*networkobserverv1.Capability{{Name: "dns_metadata", Status: "unsupported", Detail: "DNS metadata collection is available only on Linux"}}, errors.New("endpoint collection is currently available only on Linux")
}

func (unsupportedCollector) NeighborSnapshot(context.Context) ([]*networkobserverv1.NeighborObservation, *networkobserverv1.Capability) {
	return nil, &networkobserverv1.Capability{Name: "lan_neighbors", Status: "unsupported", Detail: "Linux neighbor-table collection is available only on Linux"}
}
