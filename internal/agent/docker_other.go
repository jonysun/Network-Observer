//go:build !linux

package agent

type dockerMetadataResult struct {
	status string
	detail string
}

func dockerMetadataCapability() dockerMetadataResult {
	return dockerMetadataResult{status: "unsupported", detail: "Docker Engine metadata enrichment is supported only on Linux"}
}
