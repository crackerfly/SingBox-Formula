# SingBox Formula

**A LuCI-managed subscription-to-sing-box converter for OpenWrt.**

SingBox Formula wraps the prebuilt [`sb-sub-c`](#the-sb-sub-c-binary) converter (v0.7.2) as a
procd service and gives it a full LuCI front end. It fetches a node subscription, applies a JSON
template, and produces a ready-to-use **sing-box** profile — which a sing-box runtime such as
[OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo) can then load.

> This app **does not run sing-box itself**. It only produces the profile. Use OpenWrt-momo (or
> another runtime) to actually run sing-box, firewall rules, access control and scheduling.

**Version:** 1.3.1 · **Two packages:** `singbox-formula` (service) + `luci-app-singbox-formula` (UI),
versioned together.

---

## Features

- **Subscription → sing-box JSON** conversion driven entirely from LuCI.
- **Two-tab LuCI UI** (`Services → SingBox Formula`): *Overview* (settings, integration, service
  control) and *Templates* (template management).
- **One-click service control** with an auto-refreshing status card and spinner feedback: restart,
  refresh subscription, validate the generated config, and write the output file. The converter is
  started/stopped from the *Enable converter service* switch in Basic Settings.
- **Converter version** (`sb-sub-c`) is shown on the status line.
- **Template management**: upload, edit, enable/disable and delete JSON templates; templates are
  served locally to the converter over HTTP.
- **`flag=singbox` helper**: automatically appends `flag=singbox` to your subscription URL so
  providers that return a base64 / URI node list hand back sing-box JSON instead (toggleable).
- **Safe apply model**: enabling the service and hitting *Save & Apply* starts it immediately; the
  boot delay only applies to autostart on boot.
- **Local timezone logs**: the converter inherits the system timezone so its timestamps match the
  router clock (when the binary formats in local time).

---

## System requirements

- **OpenWrt 25.12** (uses the `apk` package manager and rpcd/LuCI from that release).
- **Architecture:** the bundled `sb-sub-c` binary is **Linux AArch64 (arm64)**, built for
  `aarch64` targets such as the **Linksys E8450 / Belkin RT3200** (MediaTek MT7622).
- **Other architectures:** replace the bundled binary with the matching `sb-sub-c` v0.7.2 build for
  your platform — see [Using a different architecture](#using-a-different-architecture).
- **Runtime dependencies:** `libc`, `curl`, `jsonfilter` (service) and `luci-base`, `rpcd`,
  `rpcd-mod-file` (UI). These are pulled in automatically by `apk`.

---

## Installation (apk)

OpenWrt 25.12 ships the `apk` package manager. Build or obtain the two `.apk` files, copy them to the
router (e.g. via `scp` to `/tmp`), then install:

```sh
apk add --allow-untrusted \
    /tmp/singbox-formula-1.3.1-r1.apk \
    /tmp/luci-app-singbox-formula-1.3.1-r1.apk
```

`--allow-untrusted` is needed for locally built, unsigned packages. If you serve the packages from a
signed custom feed instead, you can simply `apk add luci-app-singbox-formula` (the service package is
pulled in as a dependency).

The LuCI post-install step clears the menu/template cache and restarts `rpcd` and `uhttpd`
automatically. If the new page does not appear, hard-refresh the browser (Ctrl/Cmd+Shift+R).

**Upgrade:**

```sh
apk add --allow-untrusted /tmp/singbox-formula-1.3.1-r1.apk /tmp/luci-app-singbox-formula-1.3.1-r1.apk
```

Your `/etc/config/singbox_formula` is a conffile and is preserved across upgrades.

**Uninstall:**

```sh
apk del luci-app-singbox-formula singbox-formula
```

---

## Quick start

1. Open **Services → SingBox Formula → Overview**.
2. Fill in **Source subscription URL** and, if you like, change the **Converter access password**
   (default `890716`).
3. Pick a **Default template** (the bundled `Momo Template` is preselected).
4. Tick **Enable converter service** and click **Save & Apply** — the converter starts immediately.
5. Point your sing-box runtime at the profile. Two options:
   - **Read the output file** `/etc/momo/profiles/config.json` — refreshed when you click
     **Update output file**; or
   - **Fetch the local converted URL** shown in the *Sing-Box Integration* section
     (`http://127.0.0.1:9716/?password=…&template=…`) — always up to date, since the converter
     auto-refreshes on its own interval.

---

## The LuCI interface

### Overview tab

- **Basic Settings** — all options below.
- **Sing-Box Integration** — the local converted URL (with a copy button) and a link to the
  OpenWrt-momo project.
- **Converter Service** — live status plus the action buttons.

### Converter Service buttons

| Button | Action |
| --- | --- |
| **Restart converter** | Stop then start the running process. |
| **Generate config.yaml** | Rebuild `/etc/singbox-formula/config.yaml` from your saved settings. |
| **Refresh subscription** | Tell the converter to re-fetch the subscription and rebuild its node list. |
| **Check generated config** | Dry run: generate the final sing-box JSON, validate it, and leave it in `/tmp` — nothing is installed. |
| **Update output file** | Generate + validate, then install the result to the **Output config path** (with a rotating backup). Does not restart sing-box. |

Starting and stopping the converter is done from the **Enable converter service** switch in Basic
Settings (tick/untick + *Save & Apply*), not from this section. Buttons show a spinner while running
and the status card **refreshes automatically**, so it reflects the new state (e.g. right after Save
& Apply, while the service is still coming up) without a manual page reload. *Refresh*, *Check* and
*Update* start the converter automatically if it is not running.

### Templates tab

Manage the JSON templates the converter uses. Each template has an **Enabled** flag (whether the
converter may use it) and is referenced by the **Default template** setting in Overview. The current
default template cannot be deleted. Templates live in `/www/singbox-formula/templates/` and are
served to the converter over `http://127.0.0.1/singbox-formula/templates/`.

---

## Configuration reference

UCI config file: `/etc/config/singbox_formula`, section `config global 'main'`.

| Option | Default | Unit / notes |
| --- | --- | --- |
| `enabled` | `0` | Master switch. Start on *Save & Apply* + autostart on boot. |
| `boot_delay` | `90` | **Seconds.** Applies only to autostart on boot; manual/apply starts are immediate. |
| `port` | `9716` | Converter HTTP port. |
| `password` | `890716` | Converter access password (used in the converted URL). |
| `subscription_url` | *(empty)* | Your source subscription URL. |
| `subscription_timeout` | `60` | **Seconds.** HTTP timeout when the converter fetches the subscription. |
| `refresh_interval` | `360` | **Minutes.** Converter auto-update interval (`subscription.refresh_interval`). |
| `singbox_flag` | `1` | Append `flag=singbox` to the subscription URL (see FAQ). |
| `default_template` | `momo_template` | Template ID used when a request does not specify one. Must be an enabled template. |
| `cache_dir` | `/var/lib/singbox-formula/cache` | Converter cache directory. |
| `log_file` | `/var/log/singbox-formula/server.log` | Converter log file. |
| `output_config` | `/etc/momo/profiles/config.json` | Where the validated profile is written by *Update output file*. |
| `template_base_url` | `http://127.0.0.1/singbox-formula/templates` | Local URL prefix the converter uses to fetch templates. |

Template sections look like:

```
config template 'momo_template'
	option enabled '1'
	option name 'Momo Template'
	option file 'momo-template.json'
	option no_node '➜ Direct'
```

You can also change everything from the CLI, e.g.:

```sh
uci set singbox_formula.main.refresh_interval='30'
uci commit singbox_formula
/etc/init.d/singbox-formula restart
```

---

## How it works

```
Subscription URL ──▶ sb-sub-c (HTTP :9716) ──▶ converted sing-box JSON
        ▲                    ▲                          │
        │                    │                          ├─▶ served at /?password=…&template=…
  config.yaml         JSON templates                    │
 (from your UCI)   (/www/singbox-formula/…)             └─▶ written to output_config
                                                             by "Update output file"
                                                                     │
                                                                     ▼
                                                     sing-box runtime (OpenWrt-momo)
```

1. `generate-config.sh` renders `/etc/singbox-formula/config.yaml` from your UCI settings.
2. `sb-sub-c` runs under procd, fetches the subscription, applies the template, and serves the
   converted profile on `127.0.0.1:<port>`. It auto-refreshes every `refresh_interval` minutes and
   hot-reloads template/node changes via a file watcher.
3. `update.sh apply` (the *Update output file* button) fetches the served profile, validates it, and
   installs it to `output_config` with a rotating backup.
4. OpenWrt-momo (or another runtime) reads the output file — or fetches the local URL directly — and
   runs sing-box.

Key paths:

- Service: `/etc/init.d/singbox-formula` · RPC backend: `/usr/libexec/rpcd/singbox_formula`
- Converter config: `/etc/singbox-formula/config.yaml`
- Helper scripts: `/usr/share/singbox-formula/{generate-config.sh,update.sh,validate-template.sh}`
- Logs: `/var/log/singbox-formula/{update.log,server.log}`

---

## FAQ

**Q: I ticked "Enable" and saved — does the converter start right away?**
Yes. From 1.3.0, enabling the service and hitting *Save & Apply* starts it immediately. The
**Boot delay** only delays autostart on boot; it never affects a save- or button-triggered start.

**Q: How do I start or stop the converter?**
Use the **Enable converter service** switch in Overview → Basic Settings, then *Save & Apply*.
Ticking it starts the converter immediately (and enables autostart on boot); unticking it stops it.
There is no separate Start/Stop button — use **Restart converter** if you just need to restart the
running process.

**Q: Where do I change the auto-update interval (the `"interval"` value in the log)?**
That is `refresh_interval` (in **minutes**, default `360`) under Overview → Basic Settings. Change it,
*Save & Apply*, then **Restart converter** so the scheduler re-reads it. The log line will then show
your new value (e.g. `360m0s`).

**Q: What are the units of `subscription_timeout`?**
Seconds. `refresh_interval` is minutes; `subscription_timeout` is a plain seconds value.

**Q: Default template vs. a template's Enabled flag?**
A template's **Enabled** flag decides whether the converter may use it at all. **Default template**
decides which enabled template is used when a request does not specify one. The default must point at
an **enabled** template.

**Q: My subscription returns base64/URI text instead of sing-box JSON.**
Keep **Request sing-box format (flag=singbox)** enabled (the default). It appends `flag=singbox` to
your subscription URL so the provider returns sing-box format. It joins with `?` or `&` correctly and
is skipped automatically if your URL already has a `flag=` parameter. Turn it off if a provider
misbehaves with the flag.

**Q: The converter's log timestamps are in UTC, not my timezone.**
The service injects the system timezone (`TZ`) so `sb-sub-c` can log local time. If it still logs
UTC (a trailing `Z`), that build of the binary hardcodes UTC internally and cannot be changed from
outside. The app's own `update.log` already uses local time.

**Q: I see `ERROR ... parse node file error: unexpected end of JSON input`.**
Harmless and transient: the file watcher read `node.json` mid-write. The converter immediately
re-fetches and succeeds (the following `File fetched successfully` / update lines). Only investigate
if it repeats continuously **and** the node count stays at 0.

**Q: Save & Apply used to hang — is that fixed?**
Yes. The service reconciles its running state in a detached background step, so the reload trigger
returns immediately and *Save & Apply* does not stall.

**Q: Does this run sing-box?**
No. It only produces the profile. Install a runtime such as
[OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo) to run sing-box.

---

## Using a different architecture

The bundled `sb-sub-c` is a statically linked Linux **AArch64** Go binary
(`ELF 64-bit LSB, ARM aarch64, statically linked`, ~14 MB). For any other target:

1. Obtain the matching `sb-sub-c` **v0.7.2** build for your architecture.
2. Replace the binary before building the package:
   `openwrt-feed/singbox-formula/files/usr/bin/sb-sub-c` (keep the filename `sb-sub-c` and the
   executable bit).
3. Adjust `PKGARCH` if you want the package marked for that architecture, then rebuild.

Already installed? You can also swap it live on the device:

```sh
/etc/init.d/singbox-formula stop
cp /tmp/sb-sub-c /usr/bin/sb-sub-c && chmod 0755 /usr/bin/sb-sub-c
/etc/init.d/singbox-formula start
```

---

## Building from source

The packages live under `openwrt-feed/` (`singbox-formula` and `luci-app-singbox-formula`) and build
with the standard OpenWrt SDK/buildroot for OpenWrt 25.12. Add the feed, select both packages, and
build `.apk` outputs. A GitHub Actions workflow is included under `.github/workflows/`.

---

## Credits

- Converter binary: `sb-sub-c` v0.7.2 (bundled).
- Intended companion runtime: [OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo).
