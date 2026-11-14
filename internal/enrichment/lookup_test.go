package enrichment

import (
	"testing"
)

func TestUnknownSafeLookup(t *testing.T) {
	lookup, err := LoadMMDB("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer lookup.Close()
	for _, test := range []struct{ ip, reason string }{
		{"not-an-ip", "invalid_ip"},
		{"192.168.1.20", "private_or_reserved"},
		{"2001:db8::1", "private_or_reserved"},
		{"8.8.8.8", "database_unconfigured"},
	} {
		_, _, reason := lookup.Lookup(test.ip)
		if reason != test.reason {
			t.Errorf("Lookup(%q) reason = %q, want %q", test.ip, reason, test.reason)
		}
	}
}

func TestLoadMMDBRejectsMissingConfiguredFile(t *testing.T) {
	if _, err := LoadMMDB(t.TempDir()+"/missing.mmdb", ""); err == nil {
		t.Fatal("expected missing MMDB file to fail startup")
	}
}
