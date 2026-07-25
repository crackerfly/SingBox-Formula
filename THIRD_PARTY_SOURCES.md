# Third-party source provenance

`liquid-formula` builds two GNU GPLv3 tools from source: both are
project-maintained forks of their upstreams. No precompiled executable of either tool is
stored in this repository — both are cross-compiled by the pinned OpenWrt SDK
during the normal package build.

The exact source archives that correspond to the shipped binaries live under
`third_party/sources/` and are pinned by `third_party/SHA256SUMS`. Both the
vendored trees under `openwrt-feed/liquid-formula/src-dpi/` and those archives
are verified in CI before anything is compiled.

## Project-maintained FakeHTTP 0.9.21

- Project: <https://github.com/MikeWang000000/FakeHTTP>
- Upstream baseline: <https://github.com/MikeWang000000/FakeHTTP/releases/tag/0.9.18>
- Maintained build source: `openwrt-feed/liquid-formula/src-dpi/fakehttp/`
- Corresponding source archive: `third_party/sources/FakeHTTP-Formula-0.9.21.tar.gz`
- Build: compiled by the pinned OpenWrt SDK as part of `liquid-formula`;
  no precompiled FakeHTTP executable is stored in the repository.

The 0.9.21 delta over upstream 0.9.18 is three fixes. The main one: the TCP data
offset was never validated. `doff` counts 32-bit words and cannot be below 5,
because the fixed TCP header alone is 20 bytes, but upstream computed
`tcph->doff * 4 - sizeof(struct tcphdr)` in `remove_tfo_cookie()` as a `size_t`.
A header with `doff` of 0 to 4 wrapped that subtraction to a huge value and the
option scan walked far past the NFQUEUE buffer.

Forwarded SYN packets reach NFQUEUE before the kernel parses the TCP header, so
the default `direction=outbound` configuration was enough for a crafted packet
from the LAN to reach it. Both packet parsers now reject `doff < 5`, and
`remove_tfo_cookie()` keeps a defensive check of its own.
`tests/dpi/fakehttp_doff_test.c` feeds malformed and well-formed headers to the
real parsers on every build.

The firewall setup path is also transactional across address families. If
installing the second family's rules fails after the first succeeded, startup
now enters the normal `fh_nfrules_cleanup()` path before tearing down NFQUEUE,
so a failed dual-stack start cannot leave partial firewall rules behind.

The upstream base stays at 0.9.18 deliberately. That release builds its payload
ring by inserting each parsed entry immediately after the current node
(`src/payload.c`), so the observed rotation is the reverse of command-line order
and the init script compensates by passing payloads in reverse. A newer upstream
that changes the ring would make that compensation reverse the order twice.

## Project-maintained FakeSIP 0.9.8

- Project: <https://github.com/Droid-MAX/FakeSIP>
- Upstream baseline commit:
  `bb6fdd88e7fa6f6d4fb1b02e359e5e68c7d778b6` (the 0.9.3 release source)
- Maintained build source: `openwrt-feed/liquid-formula/src-dpi/fakesip/`
- Corresponding source archive:
  `third_party/sources/FakeSIP-Formula-0.9.8.tar.gz`
- Source archive SHA-256:
  `36cb43390714b42621ec9832f74dd6bf860f7228232b07cb9fb4c4c70322af84`
- Build: compiled by the pinned OpenWrt SDK as part of `liquid-formula`;
  no precompiled FakeSIP executable is stored in the repository.

The 0.9.4 packet-path maintenance delta is intentionally small:

- use `icmpv6 type time-exceeded` in the nftables IPv6 table;
- submit the generated IPv6 nft batch once;
- encode valid IPv4 and IPv6 UDP lengths, including the UDP header;
- clean partial firewall state after a failed dual-stack setup;
- scope ICMP Time Exceeded drops to the selected inbound interfaces;
- stop raw re-injection of the original outbound datagram and accept its queued
  skb after the decoys, avoiding duplicates while preserving kernel metadata;
- use a signal-safe termination flag for clean procd shutdown.

Version 0.9.8 restores the dynamic TTL estimation: fs_srcinfo_put() was
defined but never called, so the outbound lookup always missed and every decoy
used the fixed initial TTL instead of the measured path. Aside from that the
packet path is unchanged. It corrects the CLI help and
startup log so the legacy `-0`/`-1` fields report their actual outbound/inbound
packet directions, and it explicitly reports single-stack or dual-stack mode.

FakeSIP's general behavior and base CLI are documented by the original project
at <https://github.com/MikeWang000000/FakeSIP/wiki>. The Droid-MAX baseline
adds `-p`/`-P` UDP port filters. Version 0.9.8 retains one known limitation: it
does not parse IPv6 extension headers before UDP.

## Rebuilding the upstream tools

Both tools use their own upstream Makefile, driven from
`openwrt-feed/liquid-formula/Makefile`. The reproducible supported path is
the pinned SDK workflow in `.github/workflows/build.yml`: it supplies the target
compiler and links against OpenWrt's packaged `libnetfilter-queue`.

To build a single tool by hand against an OpenWrt toolchain:

```sh
make -C openwrt-feed/liquid-formula/src-dpi/fakehttp \
	CC=aarch64-openwrt-linux-musl-gcc VERSION=0.9.21
```

Both upstream Makefiles use `override CFLAGS +=` and `override LDFLAGS +=`, so
values passed on the command line are preserved and their own required flags
are appended rather than replaced.
