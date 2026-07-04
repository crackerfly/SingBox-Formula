# SingBox Formula for OpenWrt 25.12

**Version:** 1.1.0 (both `singbox-formula` and `luci-app-singbox-formula` are versioned together).

## What's new in 1.1.0

- Renamed the project to **SingBox Formula** (package slugs `singbox-formula` / `singbox_formula`, UCI config `singbox_formula`, paths under `/etc/singbox-formula`, `/usr/share/singbox-formula`, `/www/singbox-formula`).
- Fixed RPC JSON encoding: tab characters and other control characters are now escaped, so tab-indented templates and log lines no longer break the `read_template` / `status` responses (previously produced invalid JSON that ubus rejected).
- Fixed a case where an empty/invalid `port` could make the `status` response malformed JSON.
- The **Enable converter service** checkbox is now the single master switch for boot autostart. The service is registered with procd at install, and the checkbox alone governs whether it starts on boot; the `Disable` action no longer removes the rc.d symlink, so re-enabling from the form works as expected.
- Percent-encoding of the password/template in generated URLs is now byte-accurate (UTF-8 safe).
- Output-file backups are pruned to the 5 most recent to avoid filling router flash.
- The template list now refreshes in place after Save/Delete (no manual page reload needed).

This bundle contains two OpenWrt package source directories:

- `singbox-formula`: wraps the provided `sb-sub-c` 0.7.2 Linux arm64/aarch64 binary, init service, update scripts and the default OpenWrt template.
- `luci-app-singbox-formula`: LuCI JavaScript app for converter settings, file generation, output file updates and template upload/edit/delete management.

The default template is generated from the provided `singbox-config(1).json`. Static DNS, inbounds, route rules, rule-set download settings and Clash API settings are preserved. Node outbounds are replaced by `singbox-formula` placeholders:

- `{{ Nodes }}` inserts all subscription node outbounds.
- `{{ "..." | NotesName }}` dynamically fills selector/urltest groups such as Hong Kong, Singapore, US, Japan and Korea.
- `no_node` defaults to `➜ Direct`, so empty groups remain valid.

No real subscription URL or node credentials are included in this bundle. Fill your subscription URL and converter password in LuCI after installing.

## Defaults in this build

- Converter service port: `9716`
- Boot delay: `300` seconds
- Output config path: `/etc/momo/profiles/config.json`
- Default template ID: `openwrt`
- Template directory: `/www/singbox-formula/templates`
- Local converted URL format for services running on the router:

```text
http://127.0.0.1:9716/?password=<your-password>&template=openwrt
```

This app only converts subscriptions and updates the configured output JSON file. It does not start, stop, reload or restart sing-box. Use OpenWrt-momo to run sing-box, manage firewall rules, access control, profiles and scheduled restart.

In LuCI, open:

```text
Services -> SingBox Formula
Services -> Momo -> Profile
```

The converter page shows a one-click copy field named `Local converted URL`. Paste that URL into the OpenWrt-momo profile/subscription field when you want Momo to fetch the generated sing-box JSON from this router.

## i18n

All LuCI UI text defaults to English and is wrapped with LuCI `_()` translation calls where applicable. A POT template is included at:

```text
openwrt-feed/luci-app-singbox-formula/po/templates/singbox-formula.pot
```

No extra language package is included in this bundle.

## Package build with OpenWrt SDK

Copy `openwrt-feed/singbox-formula` and `openwrt-feed/luci-app-singbox-formula` into your OpenWrt SDK `package/` directory, then build:

```sh
make package/singbox-formula/compile V=s
make package/luci-app-singbox-formula/compile V=s
```

On OpenWrt 25.12 targets, install the generated `.apk` packages with the target package manager.

## Manual overlay test

For quick testing on a Linksys E8450/aarch64 device, copy `manual-overlay` to `/`:

```sh
scp -r manual-overlay/* root@10.10.10.1:/
ssh root@10.10.10.1
chmod +x /usr/bin/sb-sub-c /etc/init.d/singbox-formula /usr/share/singbox-formula/*.sh /usr/libexec/rpcd/singbox-formula
/etc/uci-defaults/99-singbox-formula || /usr/share/singbox-formula/generate-config.sh
/etc/init.d/rpcd restart
/etc/init.d/uhttpd restart
```

Then open LuCI: **Services -> SingBox Formula**.

## Runtime commands

```sh
# Generate converter config.yaml from UCI
/usr/share/singbox-formula/generate-config.sh

# Start converter manually, no boot delay
/etc/init.d/singbox-formula start

# Refresh subscription cache
/usr/share/singbox-formula/update.sh refresh

# Check generated sing-box config only
/usr/share/singbox-formula/update.sh check

# Generate, validate and install /etc/momo/profiles/config.json only
/usr/share/singbox-formula/update.sh apply
```

Boot delay is controlled by `/etc/config/singbox_formula` option `boot_delay`; default is 300 seconds and is applied only in the init script `boot()` path.


## LuCI RPC backend note

The LuCI package installs both `singbox_formula` and `singbox-formula` rpcd objects for compatibility. After installing or upgrading packages manually, restart `rpcd` and `uhttpd` if the page reports `RPCError: Object not found`:

```sh
/etc/init.d/rpcd restart
/etc/init.d/uhttpd restart
```
