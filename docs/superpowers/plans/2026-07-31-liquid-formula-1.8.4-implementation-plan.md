# Liquid Formula 1.8.4 Implementation Plan

> **For Codex:** Use `superpowers:subagent-driven-development` to execute this
> plan task by task. Apply test-driven development and run the named focused
> tests after every implementation slice.

**Goal:** Integrate the supplied templates, repair live LuCI template-choice
refresh, add transactional ordered multi-subscription aggregation with policy
B, and add official OpenWrt WAN auto-detection without changing the vendored Go
converter or frozen Tuning behavior.

**Architecture:** Keep the existing converter and service lifecycle intact.
Add a bounded request-driven Go subscription gateway on loopback and a shared
DPI WAN resolver. Build the gateway as a second binary by copying external
source into the existing Go module only in `Build/Prepare`. Feed results into
the current generator/update and procd paths through explicit contracts. UI
changes consume existing RPC methods and mutate only the affected DOM/options.

**Tech stack:** POSIX/BusyBox `ash`, OpenWrt UCI/procd/rpcd/network.sh, LuCI
JavaScript, a cross-compiled Go loopback gateway using the existing internal
subscription parser and vendored `yaml.v3`, Node test fixtures, shell fault
injection, GitHub Actions. No Python runtime package is added.

---

## Task 1: Lock the baseline and authoritative inputs

**Files:**

- Test: `tests/shell/test_source_package.sh`
- Test: `tests/shell/test_migration.sh`
- Test: `tests/shell/test_generate_config.sh`
- Create: `tests/shell/fixtures/template-1.8.4.sha256`

1. Record the current Go-source tree digest and 1.8.3 baseline commit.
2. Add failing assertions for both supplied template hashes and required
   packaged paths.
3. Add failing assertions that both templates are installed as conffiles and
   both new-install UCI sections are enabled while momo remains default.
4. Run the three focused suites and confirm only the new assertions fail.
5. Do not change version metadata.

## Task 2: Integrate templates and additive migration

**Files:**

- Modify:
  `openwrt-feed/liquid-formula/files/www/liquid-formula/templates/momo-template.json`
- Create:
  `openwrt-feed/liquid-formula/files/www/liquid-formula/templates/localdns-template.json`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/liquid_formula`
- Modify:
  `openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula`
- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `README.md`
- Test: `tests/shell/test_migration.sh`
- Test: `tests/shell/test_generate_config.sh`
- Test: `tests/shell/test_source_package.sh`

1. Copy the exact attachment bytes into package paths.
2. Install and declare both files as conffiles.
3. Add both enabled sections to the new-install config; retain momo default.
4. Extend migration to add only missing sections/options and preserve existing
   user settings and files.
5. Add first-run and second-run migration fixtures for old scalar subscription
   data and custom templates.
6. Validate both template placeholders/references and generated configurations.
7. Run focused tests until green.

## Task 3: Specify LuCI atomic refresh in tests

**Files:**

- Test: `tests/shell/test_view_render.js`
- Test: `tests/shell/test_rpc_contract.sh`

1. Add a rendered `form.ListValue` fixture that tracks selected value, option
   arrays, change events, and UCI deltas.
2. Add failing cases for immediate add/rename/delete/disable updates.
3. Assert upload does not select the new template and unsaved unrelated form
   values survive.
4. Assert one RPC response drives both table and dropdown.
5. Assert a refresh failure leaves both UI structures unchanged and produces a
   distinct post-save refresh warning.
6. Run the two suites and verify failures precede product edits.

## Task 4: Implement LuCI atomic refresh

**Files:**

- Modify:
  `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js`
- Test: `tests/shell/test_view_render.js`
- Test: `tests/shell/test_rpc_contract.sh`

1. Retain references to the default-template option and rendered select.
2. Build a pure snapshot from `list_templates`.
3. Stage table rows and dropdown key/value arrays before changing either.
4. Commit both without `map.render()`, `uci.load()`, or a synthetic change.
5. Preserve the current value and unrelated dirty form inputs.
6. Distinguish write failure from successful-write/failed-refresh.
7. Run focused tests until green.

## Task 5: Specify subscription configuration and migration

**Files:**

- Test: `tests/shell/test_generate_config.sh`
- Test: `tests/shell/test_migration.sh`
- Test: `tests/shell/test_view_render.js`
- Test: `tests/shell/test_rpc_contract.sh`

1. Add failing tests for ordered 1/8/9 URL boundaries and exact ordering.
2. Add invalid scheme, empty entry, control-character, and disabled-mode cases.
3. Assert lossless scalar-to-list migration and idempotent second run.
4. Assert the LuCI TextValue list preserves full values, duplicate occurrences,
   and order on both supported LuCI branches.
5. Assert existing timeout/user-agent semantics remain global.

## Task 6: Build the bounded Go normalizer and second binary

**Files:**

- Create: `openwrt-feed/liquid-formula/src-subscription-gateway/*.go`
- Create: `tests/subscription/fixtures/*`
- Create: `tests/shell/test_subscription_normalize.sh`
- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `.github/workflows/build.yml`
- Modify: `.github/actions/build-package/action.yml`
- Modify: `.github/tests/test-release-workflow.sh`
- Test: `tests/shell/test_release_matrix.sh`
- Test: `tests/shell/test_source_package.sh`
- Test: `tests/shell/test_web_upload_permissions.sh`

1. Keep helper source outside the frozen `src` tree. In `Build/Prepare`, copy
   it into a separate `cmd/liquid-formula-subscription-gateway` package below
   the existing module, build a second binary, and install it without changing
   the converter binary's source inputs.
2. Import the existing `internal/subscription` parser and module-locked
   `yaml.v3`; add no Python interpreter or YAML runtime package.
3. Add fixtures for sing-box JSON, plain/Base64 URI lists, inline Clash,
   inline-plus-provider, provider-only, BOM/CRLF, anchors, aliases, unknown and
   partially invalid nodes.
4. Add failing document-size, nesting, scalar, alias, YAML-node, normalized-node,
   and zero-valid-node bound tests.
5. Implement strict format detection and normalized outbound emission.
6. Map only the confirmed protocol/transport fields and never open provider
   URLs.
7. Make both Go toolchain checks compile and test the staged helper, and make
   both OpenWrt release builds require an architecture-matching second binary,
   while every repository file below `openwrt-feed/liquid-formula/src` stays
   byte-identical.
8. Run normalizer, source-package, and permission tests until green.

## Task 7: Build loopback gateway and URL-bound generation transaction

**Files:**

- Create:
  `openwrt-feed/liquid-formula/files/usr/share/liquid-formula/wait-subscription-gateway.sh`
- Create: `tests/shell/test_subscription_aggregate.sh`
- Modify:
  `openwrt-feed/liquid-formula/files/usr/share/liquid-formula/generate-config.sh`
- Modify:
  `openwrt-feed/liquid-formula/files/usr/share/liquid-formula/update.sh`
- Modify: `openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula`
- Modify:
  `openwrt-feed/liquid-formula/files/etc/liquid-formula/config.yaml.example`
- Modify: `openwrt-feed/liquid-formula/Makefile`

1. Generate one atomic YAML file whose converter `subscription.url` names the
   loopback aggregate endpoint and whose additional top-level
   `liquid_formula_gateway` section carries the ordered real URLs and gateway
   settings.
2. Supervise the gateway as a separate procd instance. Require the readiness
   wrapper to verify health identity and matching config digest before it
   execs the unchanged converter.
3. Route startup, automatic, query, manual, check, and update refreshes through
   `GET /v1/aggregate`. Serialize every request with one cross-process
   subscription lock nested inside the existing lifecycle/update locks.
4. Test URL identity across reorder, replacement, deletion, and duplicate URLs.
5. Test fresh/fallback/no-cache/corrupt-cache cases under policy B.
6. Test cross-source exact duplicates, same-name differences, existing
   numbered names, empty tags, and deterministic reruns.
7. Implement private staging and immutable generation directories containing
   the aggregate, URL-bound manifest, source references, and bounded status.
   Fsync staged data and atomically replace one validated `current` pointer;
   clean obsolete generations/objects only after that commit.
8. Bind fallback metadata to the exact URL digest and content digest. Adopt a
   valid legacy first-source cache only for the migrated first URL.
9. Fault-inject before every generation-pointer, converter-snapshot, and final
   output replacement. Assert the strongest layered rollback possible with
   the frozen converter: each layer preserves its prior complete artifact, no
   partial generation is selected, and no invalid output is installed.
10. Run aggregate, generator, updater, procd, and concurrency tests until green.

## Task 8: Expose subscription state and warnings

**Files:**

- Modify:
  `openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula`
- Modify:
  `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js`
- Modify: `tests/shell/test_rpc_contract.sh`
- Modify: `tests/shell/test_view_render.js`
- Modify: `tests/shell/test_update.sh`

1. Add failing response-contract tests for totals, fresh count, fallback
   indices, per-source format/counts, active generation, state, failure stage,
   and preservation.
2. Keep successful status inside the immutable selected generation. On
   complete failure, atomically publish only a bounded last-attempt record and
   leave the selected status/data generation unchanged.
3. Never duplicate full source URLs into status; report ordered source indices
   and bounded diagnostics.
4. Render degraded state as a warning rather than fully fresh success.
5. Return nonzero and identify the source index when no valid URL-bound cache
   exists, while reporting that the prior complete generation was preserved.
6. Run focused RPC, view, updater, failure-injection, and status-race tests
   until green.

## Task 9: Specify WAN auto resolution

**Files:**

- Create: `tests/dpi/test_wan_resolver.sh`
- Modify: `tests/dpi/run.sh`
- Modify: `tests/dpi/test_service_commands.sh`
- Modify: `tests/dpi/test_hotplug.sh`
- Modify: `tests/shell/test_migration.sh`
- Modify: `tests/shell/test_view_render.js`

1. Add fixtures for PPPoE, DHCP/static, IPv6-only, same-device dual stack,
   distinct-device dual stack, one-family loss, and no-WAN.
2. Assert use of `network_flush_cache`, `network_find_wan*`, and
   `network_get_device`.
3. Assert no `/proc/net/route` or arbitrary `DEVICE` fallback.
4. Assert selected mode never calls auto, FakeHTTP all still works, and
   FakeSIP all still fails.
5. Assert new-install auto and existing selected migration preservation.
6. Add ifup/ifdown, irrelevant LAN/VPN, reconnect, and runtime mapping cases.

## Task 10: Implement shared WAN resolver and service integration

**Files:**

- Create:
  `openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/wan-resolver.sh`
- Modify:
  `openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/service-common.sh`
- Modify: `openwrt-feed/liquid-formula/files/etc/init.d/fakehttp`
- Modify: `openwrt-feed/liquid-formula/files/etc/init.d/fakesip`
- Modify:
  `openwrt-feed/liquid-formula/files/etc/hotplug.d/iface/99-liquid-formula-dpi`
- Modify:
  `openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula-dpi`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/fakehttp`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/fakesip`
- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `.github/scripts/restore-executable-modes.sh`
- Modify: `tests/shell/test_web_upload_permissions.sh`
- Modify: `tests/shell/test_dpi_package.sh`

1. Resolve requested families through official network.sh APIs.
2. Validate and de-duplicate L3 device names; cap dual mode at two.
3. Atomically store the last runtime logical/device/family mapping.
4. Re-resolve relevant ifup/reconnect events; use stored mapping to classify
   ifdown before re-resolution.
5. Stop and clean only when no requested family resolves.
6. Preserve current command flags, firewall cleanup, and lifecycle locking.
7. Run WAN, hotplug, service, boot-delay, and firewall tests until green.

## Task 11: Implement LuCI auto/manual interface controls

**Files:**

- Modify:
  `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/fakehttp.js`
- Modify:
  `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/fakesip.js`
- Modify:
  `openwrt-feed/luci-app-liquid-formula/root/usr/share/rpcd/acl.d/luci-app-liquid-formula.json`
- Modify: `tests/shell/test_view_render.js`
- Modify: `tests/dpi/test_luci_boot_delay.js`

1. Remove all `/proc/net/route` reads and the now-unused ACL grant.
2. Add auto mode and dependencies without mutating saved manual lists.
3. Display the current resolved result or a clear unavailable state.
4. Preserve FakeHTTP all and FakeSIP rejection.
5. Run LuCI tests until green.

## Task 12: Frozen behavior and full fault-injection regression

**Files:**

- Test: `tests/shell/test_tuning.sh`
- Test: `tests/shell/test_template_transactions.sh`
- Test: `tests/shell/test_update.sh`
- Test: `tests/shell/test_rpc_contract.sh`
- Test: `tests/shell/test_procd_service.sh`
- Test: `tests/dpi/run.sh`

1. Preserve the real frozen Tuning 100/100 gate and require every expanded
   migration and LuCI check to pass; report their final totals from the release
   run rather than embedding a stale target.
2. Run all shell suites sequentially so lifecycle fixtures do not contend.
3. Run all Node/LuCI and DPI suites.
4. Run release workflow, web-upload permission, source integrity, and
   third-party source checks.
5. Compare every file below `openwrt-feed/liquid-formula/src` to baseline and
   require zero differences.
6. Fix only reproducible product failures; distinguish fixture/process
   namespace failures from source regressions.

## Task 13: Documentation, version, and release artifacts

**Files:**

- Modify: `README.md`
- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `openwrt-feed/luci-app-liquid-formula/Makefile`
- Modify: `tests/shell/test_source_package.sh`
- Create: `docs/RELEASE_NOTES_1.8.4.md`

1. Update bilingual README configuration, formats, policy B, cache status,
   templates, WAN auto behavior, migration, and unchanged security behavior.
2. After all prior tests pass, change both package versions to 1.8.4 and update
   version assertions.
3. Re-run the complete final suite from a clean source tree.
4. Build a complete `Liquid-Formula-1.8.4.zip` preserving `.github` and all
   source files.
5. Diff against GitHub baseline `a60f593`; build a changed-files ZIP with the
   exact repository-relative layout and a human-readable file list.
6. Extract both ZIPs into fresh temporary directories and compare bytes,
   modes where applicable, versions, hidden files, template hashes, and zero
   Go-source changes.
7. Compute SHA-256 for both deliverables and report test totals and any
   environment-limited compile checks.
