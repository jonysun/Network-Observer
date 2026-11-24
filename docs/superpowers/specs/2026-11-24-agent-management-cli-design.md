# Native Linux Agent Management CLI

## Intent and scope
Provide a persistent `network-observer` command for native Linux/systemd Agent operators. It opens a terminal menu with upgrade, status, reinstall, uninstall, DNS metadata toggle/status, and logs; explicit subcommands remain available for automation.

## Design
Use a small Bash management entry point installed by the native installer. It reuses the existing checksum-verifying upgrade and safe uninstall scripts. Upgrade offers latest (resolved from GitHub latest release) or a validated explicit `vX.Y.Z`, displays the target and requires explicit `y` confirmation. Reinstall offers preserve (configuration, identity, CA, WAL) or full cleanup (all removed, re-registration required); both are confirmed, and full cleanup warns that unacknowledged WAL is lost. Uninstall lists effects and requires confirmation. DNS toggling edits only `dns_metadata_enabled` in the existing JSON config, validates the resulting JSON, restarts the systemd service, then reports configured state separately from service state/capability; requires Python 3 and fails closed if unavailable. Non-TTY invocation never opens a menu and must use subcommands.

## Safety
Default for every destructive confirmation is no. Piped/noninteractive invocations never silently confirm. The manager is native Linux/systemd only and does not manage Docker/OpenWrt. Full cleanup removes the service, Agent binary, config/CA/identity, WAL/state, and eBPF object using existing uninstall behavior. It does not revoke the Center-side device record.

## Approaches considered
1. Bash menu reusing existing scripts (selected: minimal dependency and preserves tested rollback/checksum/uninstall logic).
2. New Go management application (rejected for duplicating existing shell lifecycle behavior and expanding release artifacts).
3. `dialog`/`whiptail` UI (rejected because it adds optional package dependencies; plain terminal menu works over SSH).

## Acceptance
Native install makes `network-observer` available; latest and explicit-version upgrades are confirmed; reinstall choices have correct preservation semantics; uninstall is confirmed; DNS enable/disable updates only its config key and restarts the service; status and logs are read-only; non-TTY no-argument invocation exits with usage instead of hanging.
