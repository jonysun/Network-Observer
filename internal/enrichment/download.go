package enrichment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/geoip2-golang"
)

const (
	DefaultASNURL       = "https://github.com/blocktrace/maxmind-geoip/releases/latest/download/GeoLite2-ASN.mmdb"
	DefaultCityURL      = "https://github.com/blocktrace/maxmind-geoip/releases/latest/download/GeoLite2-City.mmdb"
	defaultReleaseURL   = "https://api.github.com/repos/blocktrace/maxmind-geoip/releases/latest"
	maxMMDBSize         = 128 << 20
	maxReleaseJSONBytes = 2 << 20
	defaultUpdatePeriod = 24 * time.Hour
)

type databaseSource struct {
	name     string
	filename string
	kind     string
	fallback string
}

var defaultDatabaseSources = []databaseSource{
	{name: "ASN", filename: "GeoLite2-ASN.mmdb", kind: "asn", fallback: DefaultASNURL},
	{name: "City", filename: "GeoLite2-City.mmdb", kind: "city", fallback: DefaultCityURL},
}

var defaultHTTPClient = &http.Client{Timeout: 70 * time.Second}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type releaseManifest struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type PreparedDatabases struct {
	ASNPath     string
	CityPath    string
	ManagedASN  bool
	ManagedCity bool
	Version     string
	DataDir     string
}

// PrepareDefaultDatabases uses valid custom paths when provided and otherwise
// prepares the latest verified release in a persistent version directory.
// Missing data or remote failures degrade to unknown enrichment with warnings.
func PrepareDefaultDatabases(ctx context.Context, asnPath, cityPath, dataDir string) (PreparedDatabases, []error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "data/enrichment"
	}
	prepared := PreparedDatabases{
		ManagedASN:  strings.TrimSpace(asnPath) == "",
		ManagedCity: strings.TrimSpace(cityPath) == "",
		DataDir:     dataDir,
	}
	if strings.TrimSpace(asnPath) != "" {
		if err := validateMMDB(asnPath, "asn"); err == nil {
			prepared.ASNPath = strings.TrimSpace(asnPath)
		} else {
			prepared.ManagedASN = true
		}
	}
	if strings.TrimSpace(cityPath) != "" {
		if err := validateMMDB(cityPath, "city"); err == nil {
			prepared.CityPath = strings.TrimSpace(cityPath)
		} else {
			prepared.ManagedCity = true
		}
	}

	warnings := make([]error, 0, 4)
	if strings.TrimSpace(asnPath) != "" && prepared.ManagedASN {
		warnings = append(warnings, fmt.Errorf("configured ASN database at %s is unavailable; using the built-in release: %w", asnPath, validateMMDB(asnPath, "asn")))
	}
	if strings.TrimSpace(cityPath) != "" && prepared.ManagedCity {
		warnings = append(warnings, fmt.Errorf("configured City database at %s is unavailable; using the built-in release: %w", cityPath, validateMMDB(cityPath, "city")))
	}
	if !prepared.ManagedASN && !prepared.ManagedCity {
		return prepared, warnings
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "releases"), 0o755); err != nil {
		warnings = append(warnings, fmt.Errorf("create GeoLite cache directory: %w", err))
		if prepared.ManagedASN {
			prepared.ASNPath = existingDatabasePath(dataDir, prepared, defaultDatabaseSources[0])
		}
		if prepared.ManagedCity {
			prepared.CityPath = existingDatabasePath(dataDir, prepared, defaultDatabaseSources[1])
		}
		return prepared, warnings
	}

	prepared.Version, _ = readActiveRelease(dataDir)
	latest, releaseErr := fetchLatestRelease(ctx, defaultHTTPClient, defaultReleaseURL)
	if releaseErr != nil {
		warnings = append(warnings, fmt.Errorf("check for GeoLite updates: %w", releaseErr))
	} else if prepared.ManagedASN || prepared.ManagedCity {
		paths, err := installRelease(ctx, dataDir, latest, prepared.ManagedASN, prepared.ManagedCity, defaultHTTPClient)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("install GeoLite release %s: %w", latest.TagName, err))
		} else if err := writeActiveRelease(dataDir, latest.TagName); err != nil {
			warnings = append(warnings, fmt.Errorf("record active GeoLite release: %w", err))
		} else {
			prepared.Version = latest.TagName
			if prepared.ManagedASN {
				prepared.ASNPath = paths[0]
			}
			if prepared.ManagedCity {
				prepared.CityPath = paths[1]
			}
			cleanupOldReleases(dataDir, latest.TagName)
			cleanupLegacyDatabases(dataDir, &prepared)
		}
	}

	if prepared.ManagedASN && prepared.ASNPath == "" {
		prepared.ASNPath = existingDatabasePath(dataDir, prepared, defaultDatabaseSources[0])
		if prepared.ASNPath == "" {
			if err := ensureMMDB(ctx, filepath.Join(dataDir, defaultDatabaseSources[0].filename), defaultDatabaseSources[0], defaultHTTPClient); err != nil {
				warnings = append(warnings, fmt.Errorf("ASN database: %w", err))
			} else {
				prepared.ASNPath = filepath.Join(dataDir, defaultDatabaseSources[0].filename)
			}
		}
	}
	if prepared.ManagedCity && prepared.CityPath == "" {
		prepared.CityPath = existingDatabasePath(dataDir, prepared, defaultDatabaseSources[1])
		if prepared.CityPath == "" {
			if err := ensureMMDB(ctx, filepath.Join(dataDir, defaultDatabaseSources[1].filename), defaultDatabaseSources[1], defaultHTTPClient); err != nil {
				warnings = append(warnings, fmt.Errorf("City database: %w", err))
			} else {
				prepared.CityPath = filepath.Join(dataDir, defaultDatabaseSources[1].filename)
			}
		}
	}
	if prepared.Version == "" {
		prepared.Version, _ = readActiveRelease(dataDir)
	}
	return prepared, warnings
}

// RunUpdateLoop checks release metadata once per interval, downloads only when
// the release tag changes, safely swaps the active readers, then removes old
// managed copies. Custom database paths are left under operator control.
func RunUpdateLoop(ctx context.Context, lookup *Lookup, prepared PreparedDatabases, interval time.Duration, onWarning func(error), onUpdated func(string)) {
	if lookup == nil || (!prepared.ManagedASN && !prepared.ManagedCity) {
		return
	}
	if interval <= 0 {
		interval = defaultUpdatePeriod
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated, err := updateRelease(ctx, lookup, &prepared, defaultHTTPClient)
			if err != nil {
				if onWarning != nil {
					onWarning(err)
				}
				continue
			}
			if updated && onUpdated != nil {
				onUpdated(prepared.Version)
			}
		}
	}
}

func updateRelease(ctx context.Context, lookup *Lookup, prepared *PreparedDatabases, client *http.Client) (bool, error) {
	latest, err := fetchLatestRelease(ctx, client, defaultReleaseURL)
	if err != nil {
		return false, fmt.Errorf("check for GeoLite updates: %w", err)
	}
	if latest.TagName == prepared.Version {
		return false, nil
	}
	paths, err := installRelease(ctx, prepared.DataDir, latest, prepared.ManagedASN, prepared.ManagedCity, client)
	if err != nil {
		return false, fmt.Errorf("install GeoLite release %s: %w", latest.TagName, err)
	}
	asnPath, cityPath := prepared.ASNPath, prepared.CityPath
	if prepared.ManagedASN {
		asnPath = paths[0]
	}
	if prepared.ManagedCity {
		cityPath = paths[1]
	}
	if err := lookup.ReplaceFromPaths(asnPath, cityPath); err != nil {
		return false, fmt.Errorf("load GeoLite release %s: %w", latest.TagName, err)
	}
	if err := writeActiveRelease(prepared.DataDir, latest.TagName); err != nil {
		return false, fmt.Errorf("record active GeoLite release %s: %w", latest.TagName, err)
	}
	prepared.ASNPath, prepared.CityPath, prepared.Version = asnPath, cityPath, latest.TagName
	cleanupOldReleases(prepared.DataDir, latest.TagName)
	cleanupLegacyDatabases(prepared.DataDir, prepared)
	return true, nil
}

func fetchLatestRelease(ctx context.Context, client *http.Client, endpoint string) (releaseManifest, error) {
	if client == nil {
		client = defaultHTTPClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("create release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "network-observer")
	response, err := client.Do(request)
	if err != nil {
		return releaseManifest{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return releaseManifest{}, fmt.Errorf("release API returned HTTP %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseJSONBytes+1))
	if err != nil {
		return releaseManifest{}, fmt.Errorf("read release metadata: %w", err)
	}
	if len(body) > maxReleaseJSONBytes {
		return releaseManifest{}, errors.New("release metadata exceeds size limit")
	}
	var manifest releaseManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return releaseManifest{}, fmt.Errorf("decode release metadata: %w", err)
	}
	if !validReleaseTag(manifest.TagName) {
		return releaseManifest{}, fmt.Errorf("release API returned an invalid tag %q", manifest.TagName)
	}
	for _, source := range defaultDatabaseSources {
		asset, ok := manifest.asset(source.filename)
		if !ok {
			return releaseManifest{}, fmt.Errorf("release %s is missing %s", manifest.TagName, source.filename)
		}
		if err := validateAssetURL(asset.BrowserDownloadURL); err != nil {
			return releaseManifest{}, fmt.Errorf("release asset %s: %w", source.filename, err)
		}
	}
	return manifest, nil
}

func (r releaseManifest) asset(name string) (releaseAsset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func validReleaseTag(tag string) bool {
	if tag == "" || tag == "." || tag == ".." || len(tag) > 100 {
		return false
	}
	for _, char := range tag {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func validateAssetURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" {
		return errors.New("unexpected download URL")
	}
	return nil
}

func installRelease(ctx context.Context, dataDir string, release releaseManifest, installASN, installCity bool, client *http.Client) ([2]string, error) {
	var paths [2]string
	if !installASN && !installCity {
		return paths, nil
	}
	if !validReleaseTag(release.TagName) {
		return paths, fmt.Errorf("invalid release tag %q", release.TagName)
	}
	releasesDir := filepath.Join(dataDir, "releases")
	if err := os.MkdirAll(releasesDir, 0o755); err != nil {
		return paths, err
	}
	targetDir := filepath.Join(releasesDir, release.TagName)
	if releaseDirectoryValid(targetDir, installASN, installCity) {
		return releasePaths(targetDir), nil
	}
	if _, err := os.Stat(targetDir); err == nil {
		if _, markerErr := os.Stat(filepath.Join(targetDir, ".network-observer-managed")); markerErr != nil {
			return paths, fmt.Errorf("refusing to replace unmanaged directory %s", targetDir)
		}
		if err := os.RemoveAll(targetDir); err != nil {
			return paths, fmt.Errorf("remove invalid cached release: %w", err)
		}
	}
	stagingDir, err := os.MkdirTemp(releasesDir, ".download-")
	if err != nil {
		return paths, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	if err := os.WriteFile(filepath.Join(stagingDir, ".network-observer-managed"), []byte("managed\n"), 0o644); err != nil {
		return paths, fmt.Errorf("mark staging directory: %w", err)
	}
	for index, source := range defaultDatabaseSources {
		if index == 0 && !installASN || index == 1 && !installCity {
			continue
		}
		asset, ok := release.asset(source.filename)
		if !ok {
			return paths, fmt.Errorf("release %s is missing %s", release.TagName, source.filename)
		}
		if err := downloadAsset(ctx, filepath.Join(stagingDir, source.filename), source, asset, client); err != nil {
			return paths, err
		}
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		if releaseDirectoryValid(targetDir, installASN, installCity) {
			return releasePaths(targetDir), nil
		}
		return paths, fmt.Errorf("activate downloaded release directory: %w", err)
	}
	return releasePaths(targetDir), nil
}

func downloadAsset(ctx context.Context, path string, source databaseSource, asset releaseAsset, client *http.Client) error {
	if err := validateAssetURL(asset.BrowserDownloadURL); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return fmt.Errorf("create %s download request: %w", source.name, err)
	}
	if client == nil {
		client = defaultHTTPClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s database: %w", source.name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s database returned HTTP %s", source.name, response.Status)
	}
	if response.ContentLength > maxMMDBSize {
		return fmt.Errorf("%s database exceeds %d bytes", source.name, maxMMDBSize)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mmdb-download-")
	if err != nil {
		return fmt.Errorf("create %s temporary file: %w", source.name, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, maxMMDBSize+1))
	if copyErr != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s database: %w", source.name, copyErr)
	}
	if written > maxMMDBSize {
		_ = temporary.Close()
		return fmt.Errorf("%s database exceeds %d bytes", source.name, maxMMDBSize)
	}
	if digest := strings.TrimPrefix(asset.Digest, "sha256:"); digest != "" {
		if len(digest) != sha256.Size*2 || !strings.EqualFold(digest, hex.EncodeToString(hash.Sum(nil))) {
			_ = temporary.Close()
			return fmt.Errorf("%s database SHA-256 does not match release metadata", source.name)
		}
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set %s database permissions: %w", source.name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s database: %w", source.name, err)
	}
	if err := validateMMDB(temporaryPath, source.kind); err != nil {
		return fmt.Errorf("downloaded %s file is not a valid MMDB: %w", source.name, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install %s database: %w", source.name, err)
	}
	return nil
}

func ensureMMDB(ctx context.Context, path string, source databaseSource, client *http.Client) error {
	if err := validateMMDB(path, source.kind); err == nil {
		return nil
	}
	return downloadFallback(ctx, path, source, client)
}

func downloadFallback(ctx context.Context, path string, source databaseSource, client *http.Client) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.fallback, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	if client == nil {
		client = defaultHTTPClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download from %s: %w", source.fallback, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download from %s returned HTTP %s", source.fallback, response.Status)
	}
	return copyAndValidateMMDB(path, source, response.Body)
}

func copyAndValidateMMDB(path string, source databaseSource, body io.Reader) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mmdb-download-")
	if err != nil {
		return fmt.Errorf("create temporary database file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, copyErr := io.Copy(temporary, io.LimitReader(body, maxMMDBSize+1))
	if copyErr != nil {
		_ = temporary.Close()
		return fmt.Errorf("write database download: %w", copyErr)
	}
	if written > maxMMDBSize {
		_ = temporary.Close()
		return fmt.Errorf("downloaded file exceeds %d bytes", maxMMDBSize)
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set database permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close downloaded database: %w", err)
	}
	if err := validateMMDB(temporaryPath, source.kind); err != nil {
		return fmt.Errorf("downloaded file is not a valid %s MMDB: %w", source.name, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install downloaded database: %w", err)
	}
	return nil
}

func validateMMDB(path, expectedKind string) error {
	reader, err := geoip2.Open(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	databaseType := strings.ToLower(reader.Metadata().DatabaseType)
	if !strings.Contains(databaseType, strings.ToLower(expectedKind)) {
		return fmt.Errorf("unexpected database type %q", reader.Metadata().DatabaseType)
	}
	return nil
}

func releaseDirectoryValid(path string, asn, city bool) bool {
	if _, err := os.Stat(filepath.Join(path, ".network-observer-managed")); err != nil {
		return false
	}
	if asn && validateMMDB(filepath.Join(path, defaultDatabaseSources[0].filename), "asn") != nil {
		return false
	}
	if city && validateMMDB(filepath.Join(path, defaultDatabaseSources[1].filename), "city") != nil {
		return false
	}
	return true
}

func releasePaths(directory string) [2]string {
	return [2]string{
		filepath.Join(directory, defaultDatabaseSources[0].filename),
		filepath.Join(directory, defaultDatabaseSources[1].filename),
	}
}

func existingDatabasePath(dataDir string, prepared PreparedDatabases, source databaseSource) string {
	if prepared.Version != "" {
		directory := filepath.Join(dataDir, "releases", prepared.Version)
		path := filepath.Join(directory, source.filename)
		if validateMMDB(path, source.kind) == nil {
			return path
		}
	}
	legacyPath := filepath.Join(dataDir, source.filename)
	if validateMMDB(legacyPath, source.kind) == nil {
		return legacyPath
	}
	return ""
}

func readActiveRelease(dataDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "active-release"))
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(data))
	if !validReleaseTag(tag) {
		return "", errors.New("active release marker is invalid")
	}
	return tag, nil
}

func writeActiveRelease(dataDir, tag string) error {
	if !validReleaseTag(tag) {
		return fmt.Errorf("invalid release tag %q", tag)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dataDir, ".active-release-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := io.WriteString(temporary, tag+"\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dataDir, "active-release"))
}

func cleanupOldReleases(dataDir, activeTag string) {
	releasesDir := filepath.Join(dataDir, "releases")
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == activeTag {
			continue
		}
		path := filepath.Join(releasesDir, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".network-observer-managed")); err == nil {
			_ = os.RemoveAll(path)
		}
	}
}

func cleanupLegacyDatabases(dataDir string, prepared *PreparedDatabases) {
	if prepared.ManagedASN {
		_ = os.Remove(filepath.Join(dataDir, defaultDatabaseSources[0].filename))
	}
	if prepared.ManagedCity {
		_ = os.Remove(filepath.Join(dataDir, defaultDatabaseSources[1].filename))
	}
}
