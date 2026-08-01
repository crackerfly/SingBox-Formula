# Liquid Formula 1.8.6 Single-Subscription Implementation Plan

> **For Codex:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task, with requirements and code-quality review after each task.

**Goal:** Build Liquid Formula 1.8.6 from the archived 1.8.3 tree, forward-port every approved non-aggregation improvement, add current User-Agent presets and fixed Tuning contracts, and deliver verified full/incremental archives plus a safe cleanup guide.

**Architecture:** Keep the 1.8.3 scalar subscription model and single `sb-sub-c` procd instance on port 9716. Port independent template, LuCI, WAN, migration, URL-validation, CI and delivery improvements in bounded commits. Treat the archived converter Go tree and third-party source trees as immutable inputs verified by offline manifests.

**Tech Stack:** POSIX `sh`, OpenWrt UCI/procd/ubus/rpcd, LuCI JavaScript, TAP-style shell/Node fixtures, OpenWrt package Makefiles, GitHub Actions, Go source integrity manifests.

---

## Task 1: Lock the archived baseline and authoritative template contract

**Files:**
- Modify: `tests/shell/test_dpi_package.sh`
- Modify: `tests/shell/test_migration.sh`
- Modify: `tests/shell/test_generate_config.sh`
- Create: `openwrt-feed/liquid-formula/files/www/liquid-formula/templates/localdns-template.json`
- Modify: `openwrt-feed/liquid-formula/files/www/liquid-formula/templates/momo-template.json`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/liquid_formula`
- Modify: `openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula`
- Modify: `openwrt-feed/liquid-formula/Makefile`

- [ ] Add failing assertions for two packaged templates, exact root/package template hashes, new-install UCI sections, momo default, conffile preservation, and immutable converter/third-party source boundaries.
- [ ] Run the three targeted shell tests and record the expected RED failures.
- [ ] Port the reviewed 1.8.4 authoritative momo/localdns templates and minimal package/UCI wiring without gateway code.
- [ ] Run targeted tests to GREEN; inspect `git diff --check` and verify `openwrt-feed/liquid-formula/src` has no content diff.
- [ ] Commit as `feat: package authoritative 1.8.6 templates`.

## Task 2: Refresh LuCI template state without reloading the page

**Files:**
- Modify: `tests/shell/test_view_render.js`
- Modify: `tests/shell/test_rpc_contract.sh`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js`

- [ ] Add render/interaction tests for create, edit, delete, restore, default-option refresh, stale-response ordering, dirty-field preservation and invalid refreshed defaults.
- [ ] Run the Node render test and RPC contract test to demonstrate RED.
- [ ] Port the reviewed atomic template refresh controller from 1.8.4, adapting it to scalar `subscription_url` and excluding all subscription-state UI.
- [ ] Run both tests to GREEN and manually inspect that no full-page reload or gateway RPC appears.
- [ ] Commit as `fix: refresh scalar template choices atomically`.

## Task 3: Restore official automatic WAN resolution for FakeHTTP/FakeSIP

**Files:**
- Create: `tests/dpi/test_wan_resolver.sh`
- Modify: `tests/dpi/run.sh`
- Modify: `tests/dpi/test_service_commands.sh`
- Modify: `tests/dpi/test_hotplug.sh`
- Modify: `tests/shell/test_migration.sh`
- Modify: `tests/shell/test_view_render.js`
- Modify: `tests/shell/test_dpi_package.sh`
- Modify: `tests/shell/test_web_upload_permissions.sh`
- Create: `openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/wan-resolver.sh`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/fakehttp`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/fakesip`
- Modify: `openwrt-feed/liquid-formula/files/etc/init.d/fakehttp`
- Modify: `openwrt-feed/liquid-formula/files/etc/init.d/fakesip`
- Modify: `openwrt-feed/liquid-formula/files/etc/hotplug.d/iface/99-liquid-formula-dpi`
- Modify: `openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula-dpi`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/fakehttp.js`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/fakesip.js`
- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `.github/scripts/restore-executable-modes.sh`

- [ ] Port the 1.8.4 WAN tests first and run resolver/service/hotplug/migration/render/package tests to RED.
- [ ] Port the official network helper based implementation, including auto/manual precedence, IPv4/IPv6 family continuity, PPPoE L3 device resolution, reconnect and hotplug behavior.
- [ ] Ensure migration only fills missing WAN-mode values and never overwrites manual interface values.
- [ ] Run all targeted tests to GREEN; check POSIX shell syntax and executable allowlist.
- [ ] Commit as `feat: resolve DPI WAN bindings from OpenWrt state`.

## Task 4: Harden scalar URL handling and migrate 1.8.5 safely

**Files:**
- Modify: `tests/shell/test_generate_config.sh`
- Modify: `tests/shell/test_update.sh`
- Modify: `tests/shell/test_migration.sh`
- Modify: `tests/shell/test_view_render.js`
- Modify: `tests/shell/test_procd_service.sh`
- Modify: `openwrt-feed/liquid-formula/files/usr/share/liquid-formula/generate-config.sh`
- Modify: `openwrt-feed/liquid-formula/files/usr/share/liquid-formula/update.sh`
- Modify: `openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula`
- Modify: `openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js`

- [ ] Add failing cross-layer URL tables for whitespace, authority, percent escapes, userinfo, port and IPv6 boundaries, plus disabled manual Check/Update cleanup paths.
- [ ] Add failing 1.8.5 migration fixtures: ordered `list subscription_url` keeps only the first value, preserves enabled, backs up marked gateway YAML exactly once at 0600, regenerates scalar YAML, and preserves ordinary custom YAML.
- [ ] Run generator/updater/migration/render/procd tests to RED.
- [ ] Extract only scalar-compatible URL/lifecycle/migration fixes from the 1.8.4 final safeguard patch; do not port gateway or generation state.
- [ ] Run targeted tests to GREEN and search the entire tree for gateway port 9717, aggregation commands and list-valued subscription UI.
- [ ] Commit as `fix: harden scalar subscription migration and lifecycle`.

## Task 5: Update maintained User-Agent presets

**Files:**
- Modify: `tests/shell/test_migration.sh`
- Modify: `tests/shell/test_view_render.js`
- Modify: `openwrt-feed/liquid-formula/files/etc/config/liquid_formula`
- Modify: `openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js`

- [ ] Add failing tests for the exact seven presets, `v2rayN/7.24.4` fresh default, removal of misleading Clash/outdated presets, and continued custom-value acceptance.
- [ ] Add migration tests for exact old built-in mappings and preservation of arbitrary/empty UA values.
- [ ] Run migration and render tests to RED.
- [ ] Implement the UCI default, exact migration map and LuCI values without changing generator passthrough semantics.
- [ ] Run targeted tests to GREEN and confirm no provider token or URL appears in diagnostics.
- [ ] Commit as `feat: refresh subscription user-agent presets`.

## Task 6: Enforce fixed Tuning choices and OpenWrt version gating

**Files:**
- Modify: `tests/shell/test_tuning.sh`
- Modify: `tests/shell/test_view_render.js`
- Modify: `tests/shell/test_rpc_contract.sh`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/etc/config/tuning`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/etc/uci-defaults/99-luci-app-liquid-formula`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/usr/share/liquid-formula/apply-tuning.sh`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula`
- Modify: `openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/customlogo.js`

- [ ] Add failing UI tests for exact fixed choices/defaults and `cake_mq` visibility on 24.10, 25.12, 26.x and unknown releases.
- [ ] Add failing shell tests for exact allowlists, the 25.12 backend gate, invalid legacy values, missing-only UCI defaults and RPC release reporting.
- [ ] Run tuning/render/RPC tests to RED.
- [ ] Implement ListValue controls, safe release parsing, RPC status field, fixed defaults and backend allowlists while preserving existing tuning transactions.
- [ ] Run targeted tests to GREEN, including rollback/fault-injection cases.
- [ ] Commit as `feat: enforce supported tuning profiles`.

## Task 7: Make web-upload CI diagnostics history-independent

**Files:**
- Create: `tests/shell/fixtures/converter-source-1.8.3.manifest`
- Create: `tests/shell/source_manifest.sh`
- Create: `tests/shell/test_source_manifest.sh`
- Modify: `tests/shell/test_source_package.sh`
- Modify: `tests/shell/test_web_upload_permissions.sh`
- Modify: `.github/scripts/restore-executable-modes.sh`
- Modify: `.github/workflows/build.yml`

- [ ] Add/reconcile tests for shallow single-commit clones, all-0755 worktrees, incorrect 100755 Git index modes, byte deletion/change/mode tampering, ambiguous filenames, symlink allowlist attacks and atomic manifest publication.
- [ ] Run source-package/manifest/web-upload tests to RED.
- [ ] Port the reviewed 1.8.5 offline manifest and bidirectional mode normalizer, excluding every subscription-gateway source/test entry.
- [ ] Add the explicit `every tracked file is committed as 100644` diagnostic and amd64/arm/arm64 converter compile coverage without restoring historical commit dependencies.
- [ ] Run targeted tests to GREEN; verify all index entries are 100644 and the converter manifest has exactly the archived source paths.
- [ ] Commit as `ci: make 1.8.6 source checks web-upload safe`.

## Task 8: Document cleanup, version and release behavior

**Files:**
- Create: `docs/CLEANUP_1.5.0_TO_1.8.5.md`
- Create: `docs/RELEASE_NOTES_1.8.6.md`
- Modify: `README.md`
- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `openwrt-feed/luci-app-liquid-formula/Makefile`
- Modify: `tests/shell/test_source_package.sh`
- Modify: `tests/shell/test_dpi_package.sh`
- Modify: `tests/shell/test_migration.sh`

- [ ] Add failing version assertions for both packages and documentation tests for the cleanup safety headings, exact gateway residues, conditional legacy paths and explicit preserve list.
- [ ] Write bilingual release guidance that clearly distinguishes 1.8.6 features from 1.8.4/1.8.5 history and states that multi-subscription was removed.
- [ ] Write the manual cleanup guide with prerequisites, backup commands, exact paths, sensitivity notes and safe order; never suggest broad recursive deletion of persistent roots.
- [ ] Set both packages to 1.8.6 and update README version references.
- [ ] Run targeted tests to GREEN and commit as `release: prepare Liquid Formula 1.8.6`.

## Task 9: Independent review and full verification

**Files:**
- Review: all files changed since baseline commit `4956b34`

- [ ] Request independent requirements review for: single-subscription/gateway absence, migration/data preservation, UA exactness, Tuning gate, WAN behavior and cleanup safety.
- [ ] Request independent code-quality/security review for POSIX portability, atomic transactions, symlink/path boundaries, Git mode behavior and LuCI 24.10/25.12 compatibility.
- [ ] Resolve every confirmed high/medium issue with a new RED test and minimal fix; rerun affected groups after each production change.
- [ ] Run all shell tests serially, all LuCI Node tests, DPI suite, workflow lint, third-party hash checks, shell/JavaScript syntax and available Go/C tests.
- [ ] Verify converter tree byte-for-byte against baseline and ensure no gateway source/binary/runtime reference remains except release/cleanup documentation and negative tests.
- [ ] Verify clean Git status and all tracked index modes are 100644; commit any final reviewed fixes.

## Task 10: Build and reverse-verify delivery archives

**Files:**
- Create outside repository: `Liquid-Formula-1.8.6-single-subscription-<timestamp>.zip`
- Create outside repository: `Liquid-Formula-1.8.3-to-1.8.6-single-subscription-<timestamp>-update-files.zip`
- Create outside repository: `Liquid-Formula-1.8.3-to-1.8.6-files.txt`

- [ ] Generate archives only from committed Git trees, never from the mutable worktree.
- [ ] Full archive must contain every tracked 1.8.6 file; incremental archive must contain exactly added/modified paths relative to archived 1.8.3 and separately list any required deletions.
- [ ] Extract each archive into a fresh temporary directory and compare path sets, file bytes, hidden files, package versions, template hashes and forbidden gateway paths.
- [ ] Run ZIP integrity and calculate SHA-256 for both archives and the file list.
- [ ] Save the final artifacts durably and provide cache-safe download links, hashes, installation instructions and the cleanup guide link.
