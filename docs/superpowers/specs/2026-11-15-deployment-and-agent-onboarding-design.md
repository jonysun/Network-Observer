# Deployment and Agent Onboarding Design

**Status:** Design approved in chat on 2026-11-15; written specification ready for review
**Scope:** Minimal Center Compose deployment, optional host monitoring, GitHub image releases, one-time Agent enrollment, and Linux installation.

## Goals

- Start the Center without configuring or downloading Grafana, Prometheus, or Node Exporter.
- Keep host monitoring available as an explicit optional Compose overlay.
- Publish reproducible, versioned Center and Agent images and Linux Agent binaries through GitHub Actions.
- Let an administrator create a short-lived, single-use Agent token in the Center UI.
- Make Agent setup straightforward for Endpoint and Gateway devices using either Docker or a native systemd service.
- Retain the existing per-Agent choice between TLS plus token and plaintext token transport.

## Existing behavior and constraints

- The base `docker-compose.yml` currently includes Postgres, ClickHouse, the Center, HTTPS Web, Prometheus, Node Exporter, and Grafana. Core configuration therefore requires a Grafana password even when an operator does not use Grafana.
- The Center currently accepts the global `ENROLLMENT_TOKEN` for new registrations and reuses it as the long-lived Agent credential. The web console can list and revoke devices but cannot issue onboarding credentials.
- Linux Agent registration already writes a private JSON configuration and the repository includes Docker Compose and a systemd unit, but installation requires manual binary placement, CA setup, registration, and service setup.
- The release must support Linux amd64 and arm64. OpenWrt remains supported through Docker in this release; native procd integration is a later extension.
- Device identity and credential revocation, WAL persistence, optional TLS, and existing traffic collection behavior must remain intact.

## Decisions

### Compose deployment profiles by file boundary

Keep `docker-compose.yml` as the minimal Center deployment with only PostgreSQL, ClickHouse, the Observer service, and HTTPS Web. Move Prometheus, Node Exporter, Grafana, and their named volumes into `docker-compose.monitoring.yml`. The optional overlay is invoked alongside the core file. Only the monitoring overlay requires `GRAFANA_ADMIN_PASSWORD`; the core `.env.example` must not require monitoring-only values.

The Center's built-in web console and `/metrics` endpoint continue to work without the overlay. Do not make either database optional: ClickHouse is required for historical traffic analytics, and PostgreSQL is required for identities and configuration.

### CI and versioned release artifacts

GitHub Actions runs Go tests and vet, the Vue production build, and Compose configuration validation for pull requests and pushes to `main`. A version tag `vX.Y.Z` publishes:

- `ghcr.io/<owner>/network-observer:vX.Y.Z` for `linux/amd64` and `linux/arm64`.
- `ghcr.io/<owner>/network-observer-agent:vX.Y.Z` for `linux/amd64` and `linux/arm64`.
- Static Linux amd64 and arm64 Agent binaries and a SHA-256 checksum manifest as GitHub Release assets.

Release image tags are immutable version tags and do not depend on `latest`. The workflow builds the tagged commit and publishes exactly the Git tag as the image version. The installer requires a release version (or accepts one explicitly supplied in its environment), prints it, verifies binary checksums, and writes that exact image or binary version into the installed configuration. Operators can choose a specific version for reproducible installs and upgrades.

### One-time Agent enrollment credentials

The authenticated device page gains an “Add Agent” action. The administrator chooses a display label, Endpoint or Gateway type, and token lifetime. `POST /api/v1/devices/enrollment-tokens` generates a cryptographically random token, stores only its SHA-256 hash, and returns the plaintext token once. Default lifetime is 24 hours; allowed lifetime is 1 hour through 30 days. Token creation is audited. `DELETE /api/v1/devices/enrollment-tokens/{id}` lets the administrator invalidate an unused token; the UI offers this action while showing the newly created token.

Registration validates and consumes the token and creates the device, per-device credential hash, and ingestion checkpoint in one database transaction. A failed registration or type mismatch leaves the token unused; concurrent attempts cannot both succeed. The token is bound to the selected Agent type, expires automatically, and cannot be reused after success. The Center returns a new random per-device credential with the device ID. The Agent replaces the onboarding token in its configuration with this long-lived device credential; subsequent reconnects authenticate with it. Device revocation invalidates that credential as it does today.

The existing global `ENROLLMENT_TOKEN` remains accepted only for authenticating already registered legacy Agents during migration. It cannot create new devices. New generated device credentials continue to be stored hashed at the Center. Never log or return a generated token after its creation response.

### Installer behavior

Provide a Bash installer distributed as a GitHub Release asset (and available in the repository for inspection) with a menu for native systemd service or Docker Compose deployment. It supports Endpoint and Gateway types and prompts for Center `host:port`, one-time token, device name, and security mode. TLS is the default; the installer accepts a local CA file or downloads a CA file from an operator-provided HTTPS URL. Plaintext mode omits CA configuration and clearly identifies the selected listener and reduced transport protection.

For native installation, require root, download the versioned architecture-matched binary and checksum, install the executable under `/usr/local/bin`, register the Agent once, store its config under `/etc/network-observer` with mode `0600`, install the systemd unit, and enable/start the service. For Docker installation, download the same pinned release binary for one-time registration, store config and any CA under a private host directory, write a Compose file pinned to the matching multi-architecture image version, and persist WAL data in a named volume. Run the container as the host installation user's UID/GID so its private config remains readable without making credentials world-readable. Endpoint Docker deployment may mount the Docker socket read-only for metadata; Gateway deployment does not mount it. Existing collection mode and capabilities remain independently configurable by the generated Agent config.

The installer must be repeatable without overwriting an existing Agent identity. It must stop with a clear message when configuration already exists and offer the explicit existing CLI recovery workflow rather than silently registering a duplicate device. The v1 native service target is systemd Linux; OpenWrt uses Docker until native procd support is designed.

## Security and compatibility

- Generate enrollment and per-device credentials with a cryptographically secure random source.
- Persist only token hashes on the Center and persist Agent configuration with owner-only file permissions.
- Enforce one-time use, expiry, observer-type match, and admin authorization server-side; do not rely on UI validation.
- Keep TLS enabled by default in installation prompts and preserve per-Agent `security_mode` behavior.
- Do not print tokens in command logs, installer output after initial display, service logs, or GitHub Actions logs.
- Keep old Agent configs usable through the legacy authentication path while requiring a new one-time token for new registrations.
- The installer uses versioned release artifacts, verifies their published checksum, and never installs an unpinned `latest` image.

## Failure behavior

- If the monitoring overlay is absent, the core Center starts normally and does not resolve Grafana-only required variables.
- If the optional overlay is selected without a Grafana password, Compose reports a clear configuration error.
- Expired, reused, revoked, or type-mismatched enrollment tokens fail registration without creating a device.
- Enrollment tokens used for registration are consumed in the same transaction as device creation, so failed/mismatched registration does not burn a valid token.
- If registration succeeds but local config writing fails, the installer reports the generated device ID and requires an administrator to revoke it before retrying; it must not hide the orphaned registration.
- Download, checksum, CA validation, registration, and service startup failures stop installation and preserve enough detail for recovery without revealing credentials.
- Unsupported Linux architectures or init systems receive an explicit unsupported-platform message.

## Acceptance criteria

- `docker compose config` succeeds with only core variables; starting the core stack starts exactly Postgres, ClickHouse, Observer, and Web.
- The monitoring overlay validates only when a Grafana password is supplied and brings up Prometheus, Node Exporter, and Grafana without altering the core deployment.
- CI validates Go, Vue, and Compose on pull requests and main pushes.
- A `vX.Y.Z` release publishes both multi-architecture images, both Linux Agent binary architectures, and checksums; release assets and composed Agent config use the same version.
- Enrollment tokens are shown once, stored only as hashes, time-bounded, type-bound, audited, and consumed atomically. Concurrent attempts cannot both register successfully.
- Successful Agent registration replaces the one-time token with its per-device credential before saving config; the file is mode `0600` and reconnects work with the new credential.
- Revocation stops subsequent Agent authentication. Legacy registered devices can reconnect with the configured global credential during migration, but that credential cannot register new devices.
- Installer shell checks cover TLS and plaintext, Endpoint and Gateway, amd64 and arm64, systemd and Docker paths, download/checksum failures, and existing-config protection.
- A clean Linux amd64 and arm64 test host can complete registration and start its selected service using the installer prompts; OpenWrt Docker setup is documented and native procd is explicitly out of scope.

## Rollout

1. Split Compose and update core/optional deployment documentation.
2. Add and test one-time token persistence, authenticated issuance, atomic consumption, and per-device credential handoff.
3. Add CI and tagged multi-architecture releases with versioned binary assets.
4. Add and exercise the interactive installer for systemd and Docker on Linux amd64/arm64.
5. Publish a tagged release and test a minimal Center plus one Linux Agent before expanding to Gateway and OpenWrt Docker deployments.

## Out of scope

- Native OpenWrt procd installation, Windows/macOS Agents, automatic DNS discovery of the Center, certificate issuance/rotation, arbitrary remote shell installation, and unattended enrollment without an administrator-issued one-time token.
