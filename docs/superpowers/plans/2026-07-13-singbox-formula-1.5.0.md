# SingBox Formula 1.5.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to execute this plan task-by-task, with a fresh implementer and a separate review pass for every task.

**Goal:** Ship a source-built `singbox-formula 1.5.0-r1` for OpenWrt 25.12.5 on Linksys E8450 that fixes converter reload/port conflicts and the audited lifecycle, cache, procd, RPC, LuCI, packaging, and upgrade defects while preserving the three user-approved security-sensitive behaviors.

**Architecture:** Vendor the exact upstream `8222509aff98229886d304ef72e1d0affb087a62` source into the OpenWrt package, replace the converter's self-referential reload logic with a single serialized supervisor, commit validated cache batches atomically behind one refresh coordinator, and treat the OpenWrt shell/procd/RPC layers as independently tested state machines. LuCI consumes narrow RPC methods and fail-closed schemas; template persistence is a transaction across UCI and the web-served file.

**Tech Stack:** Go 1.26.4, OpenWrt 25.12 package Makefiles and `golang-package.mk`, procd/ubus/rpcd/UCI shell, LuCI JavaScript, POSIX/BusyBox shell test harnesses, Node.js test runner, GitHub Actions, OpenWrt mediatek/mt7622 SDK.

## Global Constraints

- Preserve listener address `:<port>` on all interfaces. Tests must assert `:9716`, not loopback.
- Preserve default password `890716`; do not add first-install password generation.
- Preserve complete password, expected/got authentication values, subscription URL, and cache-busted fetch URL logging.
- Use upstream source commit `8222509aff98229886d304ef72e1d0affb087a62` exactly as the import baseline and keep its license material.
- Do not commit a prebuilt `sb-sub-c`, SDK output, credentials, `.git`, worktree metadata, or test temporary files.
- Every behavior change begins with a failing focused test, then the smallest implementation, then focused and regression verification.
- Run Go commands with `PATH=/tmp/go1.26.4/bin:$PATH GOMODCACHE=/tmp/sbf-go-mod-cache GOCACHE=/tmp/sbf-go-build-cache`.
- Keep the original uploaded 1.4.0 archive untouched.

---

## Task 1: Import exact converter source and switch the OpenWrt package to source builds

**Files:**

- Create: `tests/shell/harness.sh`
- Create: `tests/shell/test_source_package.sh`
- Create: `openwrt-feed/singbox-formula/src/**` from `/tmp/singbox-subscribe-convert-8222509`
- Create: `openwrt-feed/singbox-formula/src/UPSTREAM_COMMIT`
- Create: `openwrt-feed/singbox-formula/src/LICENSES/GPL-3.0-or-later.txt`
- Modify: `openwrt-feed/singbox-formula/Makefile`
- Modify: `.gitignore`

- [ ] Add `test_source_package.sh` assertions that `src/UPSTREAM_COMMIT` is the full pinned hash, `src/go.mod`, `src/go.sum`, and `src/LICENSE` exist, no `files/usr/bin/sb-sub-c` exists, package version is `1.5.0`, release is `1`, the package imports `golang-package.mk`, builds `github.com/haierkeys/singbox-subscribe-convert`, installs the target-built binary as `/usr/bin/sb-sub-c`, declares the combined license accurately, and gates on `TARGET_mediatek_mt7622`.
- [ ] Run `sh tests/shell/test_source_package.sh`; expect failure because `src/` and the source-build Makefile do not exist.
- [ ] Copy the exact clean upstream tree mechanically into `openwrt-feed/singbox-formula/src/`, excluding only upstream `.git`; add `UPSTREAM_COMMIT` with the full hash and verify `git -C /tmp/singbox-subscribe-convert-8222509 diff --quiet` before the copy.
- [ ] Rewrite the package Makefile to use `$(TOPDIR)/feeds/packages/lang/golang/golang-package.mk`, build the pinned in-tree module, install `$(PKG_BUILD_DIR)/sb-sub-c`, retain runtime files/conffiles, and restrict the package to `@TARGET_mediatek_mt7622`.
- [ ] Run `sh tests/shell/test_source_package.sh` and `go test ./...` in `src`; expect PASS.
- [ ] Commit with `build: compile converter from pinned source`.

## Task 2: Make converter bind and reload lifecycle deterministic

**Files:**

- Modify: `openwrt-feed/singbox-formula/src/cmd/run.go`
- Modify: `openwrt-feed/singbox-formula/src/cmd/run_server.go`
- Create: `openwrt-feed/singbox-formula/src/cmd/server_supervisor.go`
- Create: `openwrt-feed/singbox-formula/src/cmd/run_server_test.go`
- Create: `openwrt-feed/singbox-formula/src/cmd/server_supervisor_test.go`
- Modify: `openwrt-feed/singbox-formula/src/global/config.go`
- Create: `openwrt-feed/singbox-formula/src/global/config_test.go`
- Modify: `openwrt-feed/singbox-formula/src/pkg/safe_close/safe_close.go`

- [ ] Write tests `TestLoadCandidateDoesNotMutateGlobalConfigOnFailure`, `TestNewServerBindsAllInterfacesBeforeStartingBackgroundServices`, `TestNewServerBindFailureStartsNoBackgroundServices`, `TestServerServeFailureCancelsBackgroundServices`, `TestSupervisorInvalidConfigKeepsCurrentServer`, `TestSupervisorReloadSamePortThreeTimes`, `TestSupervisorReloadBindFailurePropagates`, and `TestSupervisorShutdownLeavesNoWatcherOrUpdater`. Assert the captured listener address is exactly `:9716`.
- [ ] Run `go test ./cmd ./global -run 'Test(LoadCandidate|NewServer|Server|Supervisor)' -count=1`; expect compile failures for the new lifecycle interfaces or behavioral failure on repeated reload.
- [ ] Add `ListenFunc`, `LoadCandidate`, `NewServerFromConfig`, `Server.Done`, and `Server.Shutdown`. Bind synchronously before starting HTTP serving, cache watcher, or auto updater; return bind errors without success logs or leaked goroutines.
- [ ] Add `ServerSupervisor` as the only current-server owner. Make config watching call `Reload`, validate candidates without mutating current global state, close and await the current instance before same-port replacement, propagate fatal replacement failure, and remove the fixed two-second sleep.
- [ ] Ensure shutdown cancels context, closes HTTP/listener/watcher/ticker, and waits for all tracked goroutines once.
- [ ] Run `go test -race ./cmd ./global ./internal/watcher -count=1` and `go test -race ./...`; expect PASS.
- [ ] Commit with `fix(converter): make reload lifecycle deterministic`.

## Task 3: Serialize refresh and atomically preserve last-known-good caches

**Files:**

- Create: `openwrt-feed/singbox-formula/src/internal/cache/batch.go`
- Create: `openwrt-feed/singbox-formula/src/internal/cache/batch_test.go`
- Create: `openwrt-feed/singbox-formula/src/internal/refresh/manager.go`
- Create: `openwrt-feed/singbox-formula/src/internal/refresh/manager_test.go`
- Modify/Create tests: `openwrt-feed/singbox-formula/src/internal/fetcher/fetcher.go`, `fetcher_test.go`
- Modify/Create tests: `openwrt-feed/singbox-formula/src/internal/handler/handler.go`, `handler_refresh_test.go`
- Modify/Create tests: `openwrt-feed/singbox-formula/src/internal/watcher/watcher.go`, `watcher_test.go`
- Modify/Create tests: `openwrt-feed/singbox-formula/src/global/config.go`, `config_timeout_test.go`
- Modify: `openwrt-feed/singbox-formula/src/cmd/run_server.go`

- [ ] Write tests for one maximum writer across initial/manual/query/auto/on-demand refreshes, concurrent cancellation, 32 MiB node and 8 MiB template hard limits, failure preserving memory and disk, validation failure preserving destination, atomic batch commit, no pre-delete during manual refresh, watcher Write/Create/Rename debounce, watcher context shutdown, and `write_timeout > subscription_timeout` validation.
- [ ] Run the focused packages with `-run 'Test(Coordinator|ConcurrentRefresh|RefreshFailure|NodeResponse|TemplateResponse|Batch|ManualRefresh|Watcher|ValidateRejectsWriteTimeout)'`; expect missing packages/interfaces and current destructive-cache behavior failures.
- [ ] Implement a context-aware one-slot refresh manager shared by every refresh entry point.
- [ ] Implement bounded `FetchBytes`; stage each response in the destination directory, `Sync`, close, validate node data or compile the Pongo2 template, and rename only after the complete batch validates. Abort removes only staging files.
- [ ] Remove pre-refresh memory clearing and formal-cache deletion; apply in-memory reload only after committed cache success.
- [ ] Watch `Write|Create|Rename` on canonical cache paths with per-path debounce and clean cancellation.
- [ ] Run `go test -race ./internal/cache ./internal/refresh ./internal/fetcher ./internal/handler ./internal/watcher ./global -count=1` and `go test -race ./...`; expect PASS.
- [ ] Commit with `fix(converter): serialize refresh and commit cache atomically`.

## Task 4: Correct OpenWrt logging, rotation, health identity, and sensitive-log compatibility

**Files:**

- Modify/Create tests: `openwrt-feed/singbox-formula/src/pkg/logger/logger.go`, `logger_test.go`
- Modify/Create tests: `openwrt-feed/singbox-formula/src/internal/handler/handler.go`, `handler_health_test.go`, `handler_logging_test.go`
- Modify/Create tests: `openwrt-feed/singbox-formula/src/internal/fetcher/fetcher.go`, `fetcher_logging_test.go`
- Modify/Create test: `openwrt-feed/singbox-formula/src/cmd/run_server.go`, `run_server_logging_test.go`
- Modify: `openwrt-feed/singbox-formula/src/go.mod`
- Modify: `openwrt-feed/singbox-formula/src/go.sum`
- Modify: `openwrt-feed/singbox-formula/Makefile`

- [ ] Write tests that debug/info reach stdout only, error reaches stderr, file logging receives every enabled level, `max_size/max_backups/max_age` configure built-in rotation, `/health` returns `service`, `version`, and `status`, and full sensitive query/auth/subscription/cache-buster values remain present.
- [ ] Run focused `go test` across `pkg/logger`, `internal/handler`, `internal/fetcher`, and `cmd`; expect stderr routing, unused rotation settings, and incomplete health schema failures.
- [ ] Build separate Zap stdout and stderr cores and a rotating file core using lumberjack; avoid duplicate error entries on stdout.
- [ ] Return stable health identity `service: singbox-subscribe-convert`, runtime version, and `status`; retain all user-approved full-value logs unchanged.
- [ ] Update package license metadata and notices for the added dependency.
- [ ] Run focused `go test -race` and `go test -race ./...`; expect PASS.
- [ ] Commit with `fix(converter): route logs and expose stable health identity`.

## Task 5: Make config generation and update operations atomic and bounded

**Files:**

- Create: `tests/shell/test_generate_config.sh`
- Create: `tests/shell/test_update.sh`
- Modify: `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh`
- Modify: `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/update.sh`
- Modify: `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/validate-template.sh`
- Modify: `openwrt-feed/singbox-formula/files/etc/config/singbox_formula`
- Modify: `openwrt-feed/singbox-formula/files/etc/singbox-formula/config.yaml.example`

- [ ] Add mock-UCI/curl/jsonfilter tests for port `1..65535`, subscription timeout `5..600`, refresh interval `1..10080`, boot delay `0..600`, permitted URL schemes/base URLs/output allowlist, enabled default template, empty YAML strings, 0600 atomic config writes, and unchanged-config inode/mtime preservation. An empty subscription URL is accepted only while the service is disabled; enabled generation requires HTTP(S).
- [ ] Add updater tests proving `refresh` never invokes the generator, all entry points share one atomic owner-token lock, dead-owner recovery is safe, temp names are unique, `install` is never used, JSON and optional sing-box validation happen before replacement, failed downloads preserve output/cache, logs rotate at 256 KiB to two backups, and only five output backups remain.
- [ ] Run `sh tests/shell/test_generate_config.sh` and `sh tests/shell/test_update.sh`; expect current range, idempotency, locking, destructive refresh, fixed-temp, and BusyBox command failures.
- [ ] Implement strict parsing and validation, `umask 077`, destination-directory `mktemp`, traps, `cmp -s`, and atomic `chmod 0600` plus `mv` in the generator.
- [ ] Implement global atomic directory locking with PID/token ownership, unique temp directories, last-known-good refresh, BusyBox `cp/chmod/mv`, validation gates, bounded logging, and backup retention in the updater.
- [ ] Set UCI `subscription_timeout` default and generate HTTP write timeout as timeout plus 60 seconds.
- [ ] Run both shell suites plus `sh -n` on all three production scripts; expect PASS.
- [ ] Commit with `fix(shell): make generation and updates atomic`.

## Task 6: Correct procd lifecycle, package migrations, and conffiles

**Files:**

- Create: `tests/shell/test_procd_service.sh`
- Create: `tests/shell/test_migration.sh`
- Modify: `openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula`
- Create: `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/run-delayed.sh`
- Modify: `openwrt-feed/singbox-formula/files/etc/uci-defaults/99-singbox-formula`
- Modify: `openwrt-feed/singbox-formula/Makefile`
- Modify: `openwrt-feed/luci-app-singbox-formula/Makefile`

- [ ] Add procd recorder tests for named `main`, boot/default/manual modes, cancelable managed boot delay, enabled recheck after delay, no delay on manual start, one delay per boot cycle, finite respawn `30 5 5`, `term_timeout 5`, digest environment updates, disabled reconcile removal, and no `pgrep` dependency.
- [ ] Add migration/package tests that only missing UCI options are filled, explicit port/output values remain, the template is a conffile, postinst does not restart uhttpd, and package scripts fail honestly when rpcd registration fails.
- [ ] Run the two suites; expect failures against the current unmanaged sleep, infinite/default respawn semantics, mtime trigger, overwrite migration, missing conffile, and postinst behavior.
- [ ] Implement a procd-managed `run-delayed.sh` helper that is stoppable, validates the `0..600` delay, rechecks enabled state after sleeping, and records a boot marker so respawn does not repeat the delay; manual starts bypass the helper.
- [ ] Generate before registration, pass a SHA-256 configuration digest as `env`, set respawn and term timeout exactly, and omit the instance when disabled during reconcile.
- [ ] Make migration helpers fill only missing values and correct package conffiles/postinst/dependencies.
- [ ] Run shell suites and `sh -n` on init/uci-default scripts; expect PASS.
- [ ] Commit with `fix(procd): reconcile one bounded service instance`.

## Task 7: Split RPC capabilities and serialize background actions

**Files:**

- Create/Modify: `tests/shell/harness.sh`
- Create: `tests/shell/test_rpc_control.sh`
- Create: `tests/node/luci_harness.mjs`
- Create: `tests/node/overview_rpc_contract.test.mjs`
- Modify: `openwrt-feed/luci-app-singbox-formula/Makefile`
- Modify: `openwrt-feed/luci-app-singbox-formula/root/usr/share/rpcd/acl.d/luci-app-singbox-formula.json`
- Modify: `openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula`
- Modify: `openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/overview.js`

- [ ] Test removal of generic file ACL and legacy `action`; require `service_action`, `generate`, `refresh`, `check`, `update`; test `{code,output}` sync schema and exact `{code:0,output:"queued"}` async success.
- [ ] Test 20 parallel jobs yield one winner, dead owner recovery, token-safe unlock, corrupt state fail-closed, 900-second worker timeout, service failure nonzero, target `main` procd waiting, health identity, valid JSON numeric port, `unknown` version failure, digest/config-error status, and frontend split-method use.
- [ ] Run `sh tests/shell/test_rpc_control.sh` and `node --test tests/node/overview_rpc_contract.test.mjs`; expect failures.
- [ ] Remove `rpcd-mod-file` and file ACLs; expose only narrow methods. Secure `/var/run/singbox-formula` as root 0700, reject symlinks/wrong ownership, use atomic lock/token/state writes, and strictly parse state/code enums.
- [ ] Make all shell/UCI/init/wait/health failures nonzero and status fail closed while retaining `config_mtime` only as an intermediate compatibility field.
- [ ] Migrate overview calls from legacy `action` in the same commit.
- [ ] Run shell/Node focused suites, shell syntax, ACL JSON parsing, and JS syntax; expect PASS.
- [ ] Commit with `fix(rpc): split capabilities and serialize background actions`.

## Task 8: Make LuCI state handling fail closed and digest-driven

**Files:**

- Create: `tests/node/overview_state.test.mjs`
- Modify: `openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/overview.js`
- Modify: `openwrt-feed/luci-app-singbox-formula/po/templates/singbox-formula.pot`

- [ ] Test missing/non-number `code`, nonexact async queued replies, RPC rejection, stale status and recovery, first digest creation, equal-size same-second digest changes, exactly one restart, unchanged digest, applied disk `status.enabled`, enabled-only default template choices, and “Converter URL” text.
- [ ] Run `node --test tests/node/overview_state.test.mjs`; expect fail-open and mtime-based behavior failures.
- [ ] Add strict response guards and stale/unavailable state, reconcile Save & Apply solely by `config_digest` and returned disk enabled state, remove frontend coordination by mtime, filter disabled templates, and update the POT text.
- [ ] Run both overview Node suites and `node --check`; expect PASS.
- [ ] Commit with `fix(luci): fail closed and reconcile by config digest`.

## Task 9: Make template persistence transactional

**Files:**

- Create: `tests/shell/test_template_transactions.sh`
- Create: `tests/node/templates.test.mjs`
- Modify: `openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula`
- Modify: `openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/templates.js`

- [ ] Test canonical `[A-Za-z0-9_]+` IDs, `[A-Za-z0-9._-]+\.json` filenames, exact 1 MiB boundary, immutable identity, unique filenames, default disable/delete rejection, unsafe UCI entry skipping, parallel serialization, rollback at each write/delete phase, persisted generate failure without restart, restart-phase failure, locked editor identity, and malformed-reply failure.
- [ ] Run shell and Node template suites; expect failures.
- [ ] Add a separate atomic template lock and validate before any canonicalization or path access. Keep temp/backup files outside webroot; snapshot UCI and file, commit both, and rollback both on persistence failure.
- [ ] Return exact phase schemas: success `{ok:true,persisted:true,phase:"done"}`, pre-persistence failure with `persisted:false`, and generate/restart failure with `persisted:true`; run restart only after successful generation.
- [ ] Lock ID and filename in edit mode and enforce the same content bound/client schema in LuCI.
- [ ] Run template suites, all RPC/overview Node tests, shell syntax, and JS syntax; expect PASS.
- [ ] Commit with `fix(templates): make persistence transactional`.

## Task 10: Pin CI, update documentation, and produce a clean release archive

**Files:**

- Create: `tests/shell/test_release_tree.sh`
- Modify: `.github/workflows/build.yml`
- Modify: `README.md`
- Create: `CHANGELOG.md`
- Create: `ROUTER-TEST-CHECKLIST.md`
- Create at release time: `dist/singbox-formula-1.5.0-source.zip`
- Create at release time: `dist/SHA256SUMS`

- [ ] Add release-tree tests that every third-party action reference is a 40-character SHA, OpenWrt is `25.12.5`, target/subtarget are `mediatek/mt7622`, CI runs shell/Node/JSON/Go/race suites, builds both APKs, verifies AArch64 ELF, and the archive excludes binaries, secrets, `.git`, worktrees, locks, caches, and temporary files.
- [ ] Run `sh tests/shell/test_release_tree.sh`; expect current floating action refs, old metadata, and incomplete verification failures.
- [ ] Pin all actions, add deterministic test/build/architecture/checksum stages, and ensure the workflow builds from the in-tree source package.
- [ ] Update README for source builds, supported target, upgrade/rollback, tests, intentional listener/password/log behaviors, and remove all `--allow-untrusted` guidance. Add concise release changes and a router checklist covering install, upgrade, reboot, boot-delay cancellation, start/stop/restart, repeated refresh/check/apply, occupied port, and template preservation.
- [ ] Run the complete local verification matrix:

  ```sh
  sh tests/shell/test_source_package.sh
  sh tests/shell/test_generate_config.sh
  sh tests/shell/test_update.sh
  sh tests/shell/test_procd_service.sh
  sh tests/shell/test_migration.sh
  sh tests/shell/test_rpc_control.sh
  sh tests/shell/test_template_transactions.sh
  sh tests/shell/test_release_tree.sh
  node --test tests/node/*.test.mjs
  find openwrt-feed -type f \( -name '*.sh' -o -path '*/etc/init.d/*' -o -path '*/etc/uci-defaults/*' -o -path '*/usr/libexec/rpcd/*' \) -exec sh -n {} \;
  find openwrt-feed -type f -name '*.json' -exec jq -e . {} \;
  find openwrt-feed -type f -name '*.js' -exec node --check {} \;
  cd openwrt-feed/singbox-formula/src && go test -race ./...
  ```

- [ ] Create the source archive from a clean tracked export, inspect its file list, scan it for credential-like samples and ELF files, compute SHA-256, extract it into a fresh directory, and rerun the non-SDK test matrix there.
- [ ] Commit with `docs(ci): prepare verified 1.5.0 source release`.

## Final Review Gate

- [ ] Review the full diff against the design specification and confirm every exclusion is still explicitly tested.
- [ ] Run `git diff --check`, `git status --short`, the complete local matrix, ZIP extraction verification, and SHA-256 verification using fresh command output.
- [ ] Record anything that cannot run locally—specifically the OpenWrt SDK APK build and router hardware checklist—as clearly pending external verification; do not report those as passed.
