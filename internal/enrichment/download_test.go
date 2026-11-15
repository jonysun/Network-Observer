package enrichment

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchLatestReleaseParsesRequiredAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2026.11.09","assets":[{"name":"GeoLite2-ASN.mmdb","browser_download_url":"https://github.com/blocktrace/maxmind-geoip/releases/download/v2026.11.09/GeoLite2-ASN.mmdb","digest":"sha256:` + strings.Repeat("a", 64) + `"},{"name":"GeoLite2-City.mmdb","browser_download_url":"https://github.com/blocktrace/maxmind-geoip/releases/download/v2026.11.09/GeoLite2-City.mmdb","digest":"sha256:` + strings.Repeat("b", 64) + `"}]}`))
	}))
	defer server.Close()

	release, err := fetchLatestRelease(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v2026.11.09" || len(release.Assets) != 2 {
		t.Fatalf("unexpected release metadata: %+v", release)
	}
}

func TestFetchLatestReleaseRejectsUnsafeAssetURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v2026.11.09","assets":[{"name":"GeoLite2-ASN.mmdb","browser_download_url":"http://example.com/asn.mmdb"},{"name":"GeoLite2-City.mmdb","browser_download_url":"https://github.com/blocktrace/maxmind-geoip/releases/download/v2026.11.09/GeoLite2-City.mmdb"}]}`))
	}))
	defer server.Close()
	if _, err := fetchLatestRelease(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("expected non-GitHub asset URL to be rejected")
	}
}

func TestDownloadAssetVerifiesDigestBeforeInstall(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not an MMDB")), Request: request}, nil
	})}
	path := filepath.Join(t.TempDir(), "asn.mmdb")
	err := downloadAsset(context.Background(), path, defaultDatabaseSources[0], releaseAsset{
		BrowserDownloadURL: "https://github.com/blocktrace/maxmind-geoip/releases/download/test/GeoLite2-ASN.mmdb",
		Digest:             "sha256:" + strings.Repeat("0", 64),
	}, client)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected digest mismatch to be rejected, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("asset was installed despite failing validation: %v", statErr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCopyAndValidateMMDBDoesNotInstallInvalidData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asn.mmdb")
	err := copyAndValidateMMDB(path, defaultDatabaseSources[0], strings.NewReader("not an MMDB"))
	if err == nil {
		t.Fatal("expected invalid MMDB to be rejected")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("invalid database was installed: %v", statErr)
	}
}

func TestValidReleaseTag(t *testing.T) {
	for _, tag := range []string{"v2026.11.09", "release_1-2"} {
		if !validReleaseTag(tag) {
			t.Errorf("expected %q to be valid", tag)
		}
	}
	for _, tag := range []string{"", "..", "../escape", "v1/tag", strings.Repeat("a", 101)} {
		if validReleaseTag(tag) {
			t.Errorf("expected %q to be rejected", tag)
		}
	}
}

func TestLookupReplaceFromPathsWithEmptyDatabases(t *testing.T) {
	lookup, err := LoadMMDB("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer lookup.Close()
	if err := lookup.ReplaceFromPaths("", ""); err != nil {
		t.Fatal(err)
	}
	if got := lookup.Version(); got != "" {
		t.Fatalf("expected empty version after empty replacement, got %q", got)
	}
}

func TestDefaultGeoLiteReleaseDownload(t *testing.T) {
	if os.Getenv("NETWORK_OBSERVER_TEST_GEOIP_DOWNLOAD") != "1" {
		t.Skip("set NETWORK_OBSERVER_TEST_GEOIP_DOWNLOAD=1 to exercise the live GeoLite release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	prepared, warnings := PrepareDefaultDatabases(ctx, "", "", t.TempDir())
	if len(warnings) != 0 {
		t.Fatalf("release preparation returned warnings: %v", warnings)
	}
	if prepared.Version == "" || !prepared.ManagedASN || !prepared.ManagedCity {
		t.Fatalf("unexpected prepared database state: %+v", prepared)
	}
	if err := validateMMDB(prepared.ASNPath, "asn"); err != nil {
		t.Fatalf("invalid live ASN database: %v", err)
	}
	if err := validateMMDB(prepared.CityPath, "city"); err != nil {
		t.Fatalf("invalid live City database: %v", err)
	}
}
