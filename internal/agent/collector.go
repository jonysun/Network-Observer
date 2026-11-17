package agent

import (
	"context"

	networkobserverv1 "github.com/network-observer/network-observer/gen/networkobserver/v1"
)

type Collector interface {
	Snapshot(context.Context) ([]*networkobserverv1.FlowObservation, []*networkobserverv1.Capability, error)
}

type NeighborCollector interface {
	NeighborSnapshot(context.Context) ([]*networkobserverv1.NeighborObservation, *networkobserverv1.Capability)
}

func NewEndpointCollector() Collector {
	return newPlatformCollector()
}

func NewCollector(config Config) Collector {
	return newConfiguredCollector(config)
}

func newConfiguredCollector(config Config) Collector {
	if openwrtLiteBuild {
		config.ObserverType = "gateway"
		config.EbpfMode = "off"
		config.DNSMetadataEnabled = false
		config.DisableProcessAttribution = true
		config.DisableSocketDiag = true
	}
	if config.ObserverType == "gateway" {
		return newGatewayCollectorWithConfig(config)
	}
	return newPlatformCollectorWithConfig(config)
}
