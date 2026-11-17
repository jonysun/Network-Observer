package domain

import (
	"encoding/json"
	"testing"
)

func TestEnrichmentKeepsZeroCoordinatesWhenMarkedAvailable(t *testing.T) {
	item := Enrichment{IP: "203.0.113.4", Latitude: 0, Longitude: 0, CoordinatesAvailable: true,
		Automatic: EnrichmentFields{Latitude: 0, Longitude: 0, CoordinatesAvailable: true}}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["latitude"] != float64(0) || decoded["longitude"] != float64(0) || decoded["coordinates_available"] != true {
		t.Fatalf("zero-valued valid coordinates were not preserved: %s", data)
	}
	automatic := decoded["automatic"].(map[string]any)
	if automatic["latitude"] != float64(0) || automatic["longitude"] != float64(0) || automatic["coordinates_available"] != true {
		t.Fatalf("automatic coordinates were not preserved: %s", data)
	}
}

func TestNormalizeIP(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{" 192.0.2.8 ", "192.0.2.8"},
		{"::ffff:192.0.2.8", "192.0.2.8"},
		{"2001:0db8:0:0:0:0:0:1", "2001:db8::1"},
	}
	for _, test := range tests {
		got, err := NormalizeIP(test.input)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeIP(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	if _, err := NormalizeIP("example.com"); err == nil {
		t.Fatal("NormalizeIP accepted a hostname")
	}
}

func TestValidateAgentCoordinates(t *testing.T) {
	validCases := [][2]float64{{0, 0}, {-90, -180}, {90, 180}}
	for _, coordinates := range validCases {
		latitude, longitude := coordinates[0], coordinates[1]
		if err := ValidateAgentCoordinates(&latitude, &longitude); err != nil {
			t.Fatalf("valid coordinates %v rejected: %v", coordinates, err)
		}
	}
	latitude, longitude := 91.0, 0.0
	if err := ValidateAgentCoordinates(&latitude, &longitude); err == nil {
		t.Fatal("out-of-range latitude accepted")
	}
	if err := ValidateAgentCoordinates(&latitude, nil); err == nil {
		t.Fatal("partial coordinate pair accepted")
	}
	if err := ValidateAgentCoordinates(nil, nil); err != nil {
		t.Fatalf("cleared coordinates rejected: %v", err)
	}
}
