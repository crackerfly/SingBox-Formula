# Singbox Subscribe Convert for OpenWrt 25.12 arm64/aarch64

This bundle contains two OpenWrt package source directories:

- `singbox-subscribe-convert`: wraps the provided `sb-sub-c` 0.7.2 Linux arm64/aarch64 binary, init service, update scripts and the default OpenWrt template.
- `luci-app-singbox-subscribe-convert`: LuCI JavaScript app for converter settings, file generation, output file updates and template upload/edit/delete management.

The default template is generated from the provided `singbox-config(1).json`. Static DNS, inbounds, route rules, rule-set download settings and Clash API settings are preserved. Node outbounds are replaced by `singbox-subscribe-convert` placeholders:

- `{{ Nodes }}` inserts all subscription node outbounds.
- `{{ "..." | NotesName }}` dynamically fills selector/urltest groups such as Hong Kong, Singapore, US, Japan and Korea.
- `no_node` defaults to `➜ Direct`, so empty groups remain valid.

No real subscription URL or node credentials are included in this bundle. Fill your subscription URL and converter password in LuCI after installing.

## Defaults in this build

- Converter service port: `9716`
- Boot delay: `300` seconds
- Output config path: `/etc/momo/profiles/config.json`
- Default template ID: `openwrt`
- Template directory: `/www/singbox-subscribe-convert/templates`
- Local converted URL format for services running on the router:

```text
http://127.0.0.1:9716/?password=<your-password>&template=openwrt
```

This app only converts subscriptions and updates the configured output JSON file. It does not start, stop, reload or restart sing-box. Use OpenWrt-momo to run sing-box, manage firewall rules, access control, profiles and scheduled restart.

In LuCI, open:

```text
Services -> Sing-box Subscribe Convert
Services -> Momo -> Profile
```

The converter page shows a one-click copy field named `Local converted URL`. Paste that URL into the OpenWrt-momo profile/subscription field when you want Momo to fetch the generated sing-box JSON from this router.

## i18n

All LuCI UI text defaults to English and is wrapped with LuCI `_()` translation calls where applicable. A POT template is included at:

```text
openwrt-feed/luci-app-singbox-subscribe-convert/po/templates/singbox-subscribe-convert.pot
```

No extra language package is included in this bundle.

## Package build with OpenWrt SDK

Copy `openwrt-feed/singbox-subscribe-convert` and `openwrt-feed/luci-app-singbox-subscribe-convert` into your OpenWrt SDK `package/` directory, then build:

```sh
make package/singbox-subscribe-convert/compile V=s
make package/luci-app-singbox-subscribe-convert/compile V=s
```

On OpenWrt 25.12 targets, install the generated `.apk` packages with the target package manager.

## Manual overlay test

For quick testing on a Linksys E8450/aarch64 device, copy `manual-overlay` to `/`:

```sh
scp -r manual-overlay/* root@10.10.10.1:/
ssh root@10.10.10.1
chmod +x /usr/bin/sb-sub-c /etc/init.d/singbox-subscribe-convert /usr/share/singbox-subscribe-convert/*.sh /usr/libexec/rpcd/singbox-subscribe-convert
/etc/uci-defaults/99-singbox-subscribe-convert || /usr/share/singbox-subscribe-convert/generate-config.sh
/etc/init.d/rpcd restart
/etc/init.d/uhttpd restart
```

Then open LuCI: **Services -> Sing-box Subscribe Convert**.

## Runtime commands

```sh
# Generate converter config.yaml from UCI
/usr/share/singbox-subscribe-convert/generate-config.sh

# Start converter manually, no boot delay
/etc/init.d/singbox-subscribe-convert start

# Refresh subscription cache
/usr/share/singbox-subscribe-convert/update.sh refresh

# Check generated sing-box config only
/usr/share/singbox-subscribe-convert/update.sh check

# Generate, validate and install /etc/momo/profiles/config.json only
/usr/share/singbox-subscribe-convert/update.sh apply
```

Boot delay is controlled by `/etc/config/singbox_subscribe_convert` option `boot_delay`; default is 300 seconds and is applied only in the init script `boot()` path.
