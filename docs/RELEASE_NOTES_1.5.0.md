# SingBox Formula 1.5.0-r1

Target: OpenWrt 25.12.5, Linksys E8450 / Belkin RT3200,
`mediatek/mt7622`, `aarch64_cortex-a53`.

## Main changes

- Builds pinned converter source commit `8222509aff98229886d304ef72e1d0affb087a62` in the OpenWrt SDK; no prebuilt ELF is shipped.
- Replaces recursive in-process server creation with one serialized supervisor. Same-port reload now closes the old HTTP listener, watcher, ticker, and workers before rebinding.
- Serializes all refresh entry points and commits validated node/template cache generations atomically with last-known-good rollback.
- Makes config generation and output updates atomic, bounded, lock-safe, and honest about failures.
- Runs one named procd `main` instance with bounded respawn, cancellable boot delay, configuration digest, and explicit manual-start mode.
- Splits broad RPC actions into narrow methods, uses root-only runtime state, validates health identity, and makes LuCI responses fail closed.
- Coordinates Save & Apply by content digest rather than second-resolution mtime.
- Makes template file and UCI persistence one rollback-capable transaction with strict identity/path validation and a 1 MiB limit.

## Intentionally unchanged

- The converter listens on `:<port>` (all interfaces).
- First-install password remains `890716`.
- Full passwords, authentication comparisons, subscription URLs/tokens, and cache-buster parameters remain visible in logs.

## Router validation checklist

- [ ] Install both signed 1.5.0-r1 packages over 1.4.0; confirm UCI, generated YAML, and edited templates remain.
- [ ] Reboot enabled and disabled; confirm only enabled boot waits for the configured delay.
- [ ] Stop during boot delay; confirm no delayed process starts afterward.
- [ ] Manually start/restart from LuCI; confirm boot delay is bypassed.
- [ ] Run stop/start/restart repeatedly; confirm one procd `main` instance and one listener.
- [ ] Click Refresh subscription repeatedly and in multiple browser tabs; confirm one operation runs and no `address already in use` appears.
- [ ] Run Refresh, Check, and Update output; confirm status and terminal result match `update.log`.
- [ ] Occupy the configured port with another process; confirm start/reload fails clearly and does not report a false healthy state.
- [ ] Change settings twice within one second without changing file size; confirm exactly one digest-driven restart.
- [ ] Edit, add, disable, and delete templates; confirm default-template safeguards and upgrade preservation.
- [ ] Simulate provider timeout/malformed/oversized responses; confirm cached and in-memory last-known-good output remains usable.
- [ ] Reboot after successful configuration and repeat Refresh/Check/Update once.
- [ ] Roll back through the previous signed feed revision and confirm conffiles remain readable.
