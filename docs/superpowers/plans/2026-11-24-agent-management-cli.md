# Native Linux Agent Management CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Add a persistent interactive and scriptable `network-observer` manager for native Linux Agents.

**Architecture:** A Bash CLI menu delegates upgrades/uninstalls to the existing scripts, adds explicit confirmation and reinstall modes, and safely toggles the DNS config using Python JSON parsing. The native installer places the manager and lifecycle scripts in stable system paths; Release assets publish them.

**Tech Stack:** Bash, Python 3 standard-library JSON, existing systemd/curl/sha256sum tooling, shell tests.

**Spec:** `docs/superpowers/specs/2026-11-24-agent-management-cli-design.md`

## Global Constraints
- Native Linux/systemd only; do not alter Docker/OpenWrt management.
- Reuse checksum verification and rollback from `scripts/upgrade-agent.sh`.
- Destructive actions default to cancel and require a TTY confirmation.
- Full cleanup explicitly removes unacknowledged WAL and requires re-registration.
- No new external runtime dependencies beyond Python 3 for JSON config edits.

---

### Task 1: Add a testable manager script

**Files:** Create `scripts/network-observer`; create `scripts/test-network-observer.sh`.

- [x] Add tests for no-TTY refusal, default-no confirmation, version validation, DNS-only JSON mutation, and reinstall mode mapping before implementing behavior.
- [x] Run the test script and confirm expected failures.
- [x] Implement menu and `upgrade-agent`, `status`, `reinstall`, `uninstall`, `dns enable|disable|status`, and `logs` commands; inject paths via environment for isolated tests.
- [x] Run tests to green; verify preserve reinstall reuses the upgrade path without touching config/WAL, while full cleanup uninstalls and registers with a new Token.

### Task 2: Install and publish the manager

**Files:** Modify `scripts/install-agent.sh`, `scripts/upgrade-agent.sh`, `.github/workflows/release.yml`.

- [x] Extend native installation to fetch/checksum/install manager and lifecycle script assets.
- [x] Ensure upgrade updates the CLI without breaking the existing symlink/entry point and retains old binary rollback behavior.
- [x] Publish scripts and include checksum entries as needed in the release workflow.
- [x] Run shell syntax checks and existing uninstall tests.

### Task 3: Documentation and verification

**Files:** Modify `README.zh-CN.md`, `docs/installation.zh-CN.md`.

- [x] Document interactive and noninteractive examples, security prompts, DNS prerequisites, and full-cleanup WAL/identity loss.
- [x] Run `bash -n` on touched scripts, `scripts/test-network-observer.sh`, `sh scripts/test-uninstall-agent.sh`, and relevant Go tests if touched.
- [x] Review diff to confirm unrelated untracked files are untouched.
