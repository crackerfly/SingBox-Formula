# Liquid Formula 1.8.4 Design

**Date:** 2026-07-31
**Status:** Approved in conversation
**Baseline:** Liquid Formula 1.8.3 (`a60f593`)

## Scope

This release combines four bounded changes:

1. integrate the supplied `momo-template.json` and
   `localdns-template.json`;
2. refresh the LuCI default-template choices immediately after a template
   mutation;
3. add ordered multi-subscription aggregation with failure policy B;
4. add official OpenWrt WAN-device auto-detection to FakeHTTP and FakeSIP.

The existing Tuning Utility transaction model is frozen. The converter Go
source, plaintext credential behavior, DPI NFQUEUE numbers, FakeSIP excluded
port, theme-template customization, read-only LuCI controls, and existing
single-primary-WAN boundary are outside this change.

The package version remains 1.8.3 during implementation and becomes 1.8.4 only
after all regression and fault-injection tests pass.

## Templates and migration

The two files supplied in `template(2).zip` are authoritative bytes:

- `momo-template.json`:
  `ac1648ec562b7d8e23407baeea758f3788889054f6c1bca56220534077c715c3`
- `localdns-template.json`:
  `9dc80b9caf2eba67ca4b542c480a10a3f88ef8082903a3c10f31a141b1b0fbdf`

Both templates are installed below
`/www/liquid-formula/templates/`, declared as conffiles, represented by
enabled UCI template sections on a new installation, and validated through
the existing JSON/placeholder/reference/generated-config checks. The default
template remains `momo_template`.

Migration is additive and idempotent. It fills missing settings and template
sections but does not rewrite an existing default, an existing user template
section, or an edited conffile. A nonempty legacy scalar subscription URL is
moved byte-for-byte to the sole item of the new ordered URL list only when that
list does not already exist. The old default empty scalar is removed and is
represented by an absent option (a zero-item list); new installations likewise
omit the option until a URL is configured. This avoids turning “no URL” into an
invalid supplied empty-string item.

## LuCI template refresh

`list_templates` remains the single authoritative RPC read. A successful
template mutation requests one fresh result, derives both the table rows and
the `form.ListValue` key/value arrays, and commits both UI changes together.
There is no page reload and no full `Map` re-render, so unrelated unsaved form
fields remain untouched.

The current dropdown value is preserved. Uploading an enabled template adds it
to the list but does not select it. Disabled templates are absent. Renames,
deletes, and disable operations are reflected immediately. Updating choices
must not call the option's change handler or create a UCI delta.

If the refresh RPC fails, neither the table nor dropdown is changed. The user
is told that the template write succeeded but the interface refresh failed.
The backend's existing protection against deleting or disabling the saved
default remains the final authority.

## Multi-subscription aggregation

### Configuration

LuCI exposes one official `form.TextValue` as an ordered newline-delimited
editor for one to eight HTTP(S) URLs while subscriptions are enabled. A stock
`form.DynamicList` was deliberately not retained because LuCI 24.10 collapses
duplicate values even when `allowduplicates` is requested; the TextValue path
preserves exact occurrence order on both LuCI 24.10 and 25.12. The existing
user agent, timeout, and refresh interval remain global.

The ordered list is read without shell evaluation. ASCII whitespace/control
bytes, malformed percent escapes in parsed URL components, missing hostnames,
and non-HTTP(S) schemes are rejected consistently at the browser, generator,
updater, and gateway boundaries. The full URL is not copied into status
summaries. Existing plaintext configuration and logging behavior otherwise
remains unchanged.

### External loopback gateway

The vendored Go converter remains byte-identical. The helper is a second Go
binary whose source lives outside `openwrt-feed/liquid-formula/src`. During
`Build/Prepare` that source is copied below the existing module as a separate
`cmd` package, allowing it to reuse `internal/subscription` and the existing
module-locked `yaml.v3` dependency without adding a Python runtime dependency.
Building the helper must not modify any repository file in the frozen
converter tree.

The generated converter YAML remains one atomically replaced file. Its
`subscription.url` points to a private HTTP endpoint on `127.0.0.1`; an
additional top-level `liquid_formula_gateway` section, ignored by the
unchanged converter, carries the ordered real URLs and helper settings. A
supervised gateway instance binds that endpoint before the converter starts.
A readiness wrapper verifies the gateway health identity and current config
digest before executing the converter, including after boot delay and service
restart.

For each aggregate request, the gateway:

1. downloads each source into a private transaction directory with the
   existing timeout, user-agent, and 32 MiB limit;
2. detects sing-box outbound JSON, Base64 URI lists, plain URI lists, or
   Clash/Mihomo YAML;
3. converts each accepted source to a normalized sing-box outbound array;
4. validates the per-source result;
5. applies per-URL cache fallback;
6. merges nodes deterministically;
7. returns one aggregate sing-box JSON source for the unchanged converter.

Startup, automatic, query, manual, check, and update refreshes all reach this
same request-driven gateway. A cross-process subscription transaction lock
serializes them. The existing lifecycle and updater locks remain outer locks;
the gateway never acquires them in reverse order.

Clash handling accepts only root-level inline `proxies`. It never fetches or
recurses through `proxy-providers`; a provider-only document fails. Supported
protocols mirror the current converter: Shadowsocks, VMess, VLESS, Trojan,
Hysteria2/Hy2, TUIC, AnyTLS, and SOCKS/SOCKS5. Unsupported or unreliably
mapped nodes are skipped with a warning; a source with zero valid nodes fails.

The YAML parser is bounded for document size, nesting, scalar length, aliases,
and node count. It handles UTF-8 BOM, CRLF, comments, quoted scalars, anchors,
and aliases within those bounds.

### Merge and cache identity

Merge order is subscription order, then source node order. Canonical JSON of
the complete outbound excluding the mutable tag provides exact semantic
duplicate identity; the first occurrence wins. Same-name, different-content
nodes remain and receive deterministic, collision-free numbering. All final
tags are nonempty and unique.

Committed cache state uses immutable generation directories selected by one
atomically replaced `current` pointer. Each generation contains the aggregate,
manifest, bounded status, and URL-bound source references. Cache identity is a
cryptographic digest of the exact URL, and metadata binds content to that URL
and its content digest. Reordering therefore cannot mismatch caches, while
replacing or removing a URL cannot borrow another source's cache. A valid
legacy `node.json` may be adopted once for the migrated first URL. Obsolete
generation and object cleanup happens only after a successful pointer commit.

### Failure policy B and transaction

Every source independently yields either a fresh validated result or its own
validated old cache. A failed source with a valid bound cache produces a
degraded successful generation. A failed source without a valid cache fails
the complete operation.

On complete failure no new per-source cache, aggregate cache, snapshot, status
generation, or final output is published. The last complete configuration
continues to run. On degraded success all fresh source caches, the aggregate,
cache manifest, and status are installed as one generation after JSON
validation and, when a compatible local `sing-box` executable exists,
`sing-box check`.

Refresh, check, update, automatic refresh, and startup refresh share the
gateway subscription transaction lock. The existing lifecycle/update locks
nest outside it, preventing mixed cache generations without introducing a
lock-order cycle.

Status and logs report total sources, fresh count, fallback source indices,
detected format and accepted/skipped counts per source, overall
`fresh`/`degraded`/`failed`, failure stage, and whether the prior complete
configuration was preserved.

### Layered validation boundary

The frozen converter has no external prepare/commit acknowledgement, so the
gateway, converter snapshot, and installed output validate and publish at
layered transaction boundaries. The gateway publishes only a fully validated
immutable source generation; the unchanged converter publishes its node and
template snapshot only after its existing validation; `update.sh` publishes a
final output only after JSON validation and additionally runs `sing-box check`
when a compatible local executable is available. This preserves the approved
optional-runtime contract from 1.5 while strengthening validation on devices
that have the CLI installed. This boundary does not relax failure policy B.
Fault-injection tests must exercise every
handoff and replacement point and prove the strongest available rollback:
each layer preserves its last complete artifact, no partial generation is
selected, and no invalid final output is installed.

## WAN auto-detection

Both DPI services gain `interface_mode=auto`, which is the new-install
default. Existing explicit `selected` configurations and manual interface
lists are preserved. `selected` always wins and does not call, supplement, or
fall back to auto detection. FakeHTTP retains `all`; FakeSIP continues to
reject it.

The runtime resolver sources `/lib/functions/network.sh`, flushes its cache,
and resolves:

- IPv4: `network_find_wan()` then `network_get_device()`;
- IPv6: `network_find_wan6()` then `network_get_device()`.

It uses the actual L3 device, including `pppoe-wan`, and never falls back to
`/proc/net/route`, a logical interface, a physical parent, an arbitrary
hotplug `DEVICE`, or an old automatic UCI value. IPv4 and IPv6 are resolved
only when their configured family needs them. Duplicate devices are collapsed;
different v4/v6 default devices yield at most two devices.

The last successfully resolved runtime mapping is written atomically for
ifdown handling. Relevant ifup/reconnect events re-resolve current official
defaults. An ifdown event uses the last mapping to determine relevance and
then re-resolves. LAN, VPN, and non-current-default events are ignored.
Dual-stack mode continues with one available family; no available family
stops the instance and cleans its own firewall state.

The current lifecycle lock, delayed start, firewall cleanup, NFQUEUE 8970/8971,
FakeSIP `-P 53`, direction, mark/mask, payload, and IPv6 packet behavior remain
unchanged.

## Tuning Utility freeze

The existing shared `flock`, atomic file replacement, mode preservation,
symlink rejection, per-key backup/restore, stale-backup retirement,
fail-closed reads, explicit rollback reporting, partial runtime status,
disable semantics, prerm ordering, LuCI `uci-applied` sequencing, and additive
migration behavior must remain unchanged.

Every regression suite must pass in full. The frozen Tuning suite's historical
and current gate is 100/100; the previously written inflated figure had no
repository basis. LuCI and migration totals grow as the 1.8.4 fixtures are
expanded, so their final reported totals are taken from the release run.

## Verification and release

Tests are added before implementation for:

- template hashes, conffiles, defaults, migration, and generated outputs;
- atomic dropdown/table refresh and failed-refresh behavior;
- one/eight/nine URLs, formats, protocols, de-duplication, tag collisions,
  URL-bound cache fallback, rollback, fault injection, and concurrency;
- PPPoE, DHCP/static, IPv6-only, same/different-device dual stack, hotplug,
  manual/all modes, and resolver failure;
- zero Go-source byte changes;
- existing template, updater, RPC, LuCI, tuning, DPI, release, permissions,
  and third-party-source suites.

Only after the frozen and new suites pass is version metadata changed to
1.8.4. The final artifact is a complete GitHub-web-upload-safe source ZIP,
plus a 1.8.3-to-1.8.4 changed-file ZIP/list and SHA-256 checksums.
