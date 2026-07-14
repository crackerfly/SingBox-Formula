# SingBox Formula

**A LuCI-managed subscription-to-sing-box converter for OpenWrt.**

SingBox Formula builds the pinned [`sb-sub-c`](#the-sb-sub-c-source) converter source (v0.7.2) as a
procd service and gives it a full LuCI front end. It fetches a node subscription, applies a JSON
template, and produces a ready-to-use **sing-box** profile — which a sing-box runtime such as
[OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo) can then load.

> This app **does not run sing-box itself**. It only produces the profile. Use OpenWrt-momo (or
> another runtime) to actually run sing-box, firewall rules, access control and scheduling.

**Version:** 1.5.0 · **Two packages:** `singbox-formula` (service) + `luci-app-singbox-formula` (UI),
versioned together.

---

## Features

- **Subscription → sing-box JSON** conversion driven entirely from LuCI.
- **Two-tab LuCI UI** (`Services → SingBox Formula`): *Overview* (settings, integration, service
  control) and *Templates* (template management).
- **One-click service control** with an auto-refreshing status card, per-button spinners and
  transient floating toast feedback (no sticky banners). Subscription operations (Refresh / Check /
  Update) run in the background, so a slow provider can never stall or time out the page.
- **Truthful three-state status** — Stopped / Running (not ready) / Running — derived from both the
  procd instance and a live health probe, plus the `sb-sub-c` converter version on the status line.
- **Template management**: upload, edit, enable/disable and delete JSON templates; templates are
  served locally to the converter over HTTP.
- **`flag=singbox` helper**: automatically appends `flag=singbox` to your subscription URL so
  providers that return a base64 / URI node list hand back sing-box JSON instead (toggleable).
- **Safe apply model**: *Save & Apply* brings the service in line with the Enable switch — it
  starts the converter when enabled, stops it when disabled, and restarts it when settings actually
  changed (detected via the committed config), so changes always take effect. The boot delay only
  applies to autostart on boot.
- **Local timezone logs**: the converter inherits the system timezone so its timestamps match the
  router clock (when the binary formats in local time).

---

## System requirements

- **OpenWrt 25.12.5** (uses the `apk` package manager and rpcd/LuCI from that release).
- **Supported target:** **Linksys E8450 / Belkin RT3200**, `mediatek/mt7622`,
  `aarch64_cortex-a53`. The package compiles the pinned Go source inside the matching OpenWrt SDK;
  it does not contain a host or prebuilt ELF binary.
- **Runtime dependencies:** `libc`, `curl`, `jsonfilter` (service) and `luci-base`, `rpcd`,
  `rpcd` (UI). These are pulled in automatically by `apk`.

---

## Installation (apk)

OpenWrt 25.12 ships the `apk` package manager. Publish the two packages through a signed feed, then
install them with normal signature verification:

```sh
apk update
apk add luci-app-singbox-formula
```

The service package is pulled in as a dependency. Do not bypass package signature verification on a
production router.

The LuCI post-install step clears its cache and restarts `rpcd`; it deliberately does not restart
`uhttpd`. If the page does not appear, hard-refresh the browser (Ctrl/Cmd+Shift+R).

**Upgrade:**

```sh
apk update
apk upgrade singbox-formula luci-app-singbox-formula
```

Your UCI configuration, generated YAML, and bundled editable template are conffiles and are
preserved across upgrades. Keep the previous signed feed revision available for rollback.

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
Settings (tick/untick + *Save & Apply*), not from this section. Buttons show a spinner while running,
results appear as a transient floating toast, and the status card **refreshes automatically**.
*Restart* waits until the old process has exited and the new one answers its health check before
reporting the real outcome. *Refresh*, *Check* and *Update* run **in the background**: progress
streams into the Recent Update Log, the action buttons stay disabled while one is running (even
across page reloads), and a toast reports success or failure on completion. They start the converter
automatically if it is not running.

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
   converted profile on `:<port>` (all router interfaces). LuCI deliberately displays a loopback
   URL for local consumers. It auto-refreshes every `refresh_interval` minutes and
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

**Q: Will readers see partially written cache files?**
No. Subscription and template responses are bounded, validated, staged in the destination
filesystem, and committed as one batch. Failed downloads, validation, or replacement keep the
last-known-good memory and disk generation.

**Q: Save & Apply used to hang — is that fixed?**
Yes. The config reload only regenerates `config.yaml` (it never starts/stops from inside the reload
trigger), so it returns immediately and *Save & Apply* does not stall. The LuCI page then waits for
the commit to land (via the generated config content digest) and restarts the converter when enabled — so changed
settings actually take effect — or stops it when disabled.

**Q: Refresh / Check / Update returned a toast instantly — did anything actually happen?**
Yes. These operations run in the background so a slow subscription provider can never stall the page
or hit the ubus timeout. Progress appears live in the Recent Update Log, the buttons stay disabled
while an operation runs, and a toast reports the real result when it finishes.

**Q: Does this run sing-box?**
No. It only produces the profile. Install a runtime such as
[OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo) to run sing-box.

---

## Listener and log disclosure policy

Version 1.5.0 intentionally preserves these deployment choices:

- The converter listens on `:<port>`, not only `127.0.0.1:<port>`.
- The first-install password remains `890716`; change it in LuCI when desired.
- Converter diagnostics retain complete authentication values and subscription URLs, including
  tokens and cache-buster query parameters.

Protect router administration and log access accordingly. The generated LuCI integration link uses
`127.0.0.1` for on-device consumers, but that does not narrow the actual listening socket.

## Building from source

The packages live under `openwrt-feed/` and are built with the OpenWrt **25.12.5** SDK for
`mediatek/mt7622` / `aarch64_cortex-a53`. The package pins upstream commit
`8222509aff98229886d304ef72e1d0affb087a62` and compiles it with the OpenWrt Go helper.

GitHub's web uploader records uploaded files without Unix executable bits. The Actions workflow
therefore invokes `.github/scripts/restore-executable-modes.sh` explicitly through `sh` immediately
after checkout, restoring only the reviewed executable allowlist before tests and SDK packaging.
This makes a source tree uploaded entirely through the GitHub web page self-bootstrapping.

GitHub's browser uploader accepts at most 100 files in one upload. This release keeps all 110
tracked files and therefore uses exactly two browser-upload rounds:

- **Batch 1:** `singbox-formula-1.5.0-web-upload-batch-1-openwrt-feed.zip` contains only
  `openwrt-feed/`, exactly 88 tracked files.
- **Batch 2:** `singbox-formula-1.5.0-web-upload-batch-2-repository.zip` contains every tracked path
  outside `openwrt-feed/`, exactly 22 tracked files. Its six top-level entries are `.github`,
  `.gitignore`, `README.md`, `docs`, `momo-template.json`, and `tests`.

The separate `singbox-formula-1.5.0-complete-source.zip` contains all 110 tracked files for source
integrity and reference; it is not a one-round browser-upload bundle.

To upload the release using only GitHub's web interface:

1. Extract both batch ZIP files locally. Do not upload the ZIP files themselves.
2. Reveal hidden files before selecting Batch 2 so `.github` and `.gitignore` are included. In
   macOS Finder press `Command+Shift+.`; in Windows Explorer choose **View → Show → Hidden items**.
3. At the repository root, choose **Add file → Upload files**. For Round 1, open the extracted
   `SingBox-Formula-1.5.0-web-upload-batch-1` wrapper, drag its `openwrt-feed` directory into the
   browser, and commit the upload.
4. Return to the repository root and choose **Add file → Upload files** again. For Round 2, open the
   extracted `SingBox-Formula-1.5.0-web-upload-batch-2` wrapper, drag all six top-level entries
   (`.github`, `.gitignore`, `README.md`, `docs`, `momo-template.json`, and `tests`) into the browser,
   and commit the upload.
5. Do not drag either wrapper directory itself; doing so creates an unwanted extra directory level.
6. Treat the repository after the second commit as authoritative. An intermediate Actions result
   between the two batches may be ignored.

```sh
sh tests/shell/test_generate_config.sh
sh tests/shell/test_update.sh
sh tests/shell/test_procd_service.sh
sh tests/shell/test_rpc_contract.sh
sh tests/shell/test_template_transactions.sh
sh tests/shell/test_source_package.sh
sh tests/shell/test_web_upload_permissions.sh
(cd openwrt-feed/singbox-formula/src && go test -race ./... && go vet ./...)
```

Build both packages in the pinned SDK, publish them through a signed repository, and keep the prior
revision available for rollback.

---

## Credits

- Converter source: `sb-sub-c` v0.7.2, pinned and locally patched for OpenWrt lifecycle safety.
- Intended companion runtime: [OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo).
