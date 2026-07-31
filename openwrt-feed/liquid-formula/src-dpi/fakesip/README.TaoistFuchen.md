# TaoistFuchen-maintained FakeSIP 0.9.5

This source tree is based on Droid-MAX/FakeSIP commit
`bb6fdd88e7fa6f6d4fb1b02e359e5e68c7d778b6`, which was released as 0.9.3.
It remains licensed under GNU GPL version 3 or later.

TaoistFuchen 0.9.4 applied the following focused packet and IPv6 fixes:

- use `icmpv6 type time-exceeded` in the nftables `ip6` table;
- load the generated IPv6 nftables batch once instead of twice;
- encode generated IPv4 and IPv6 UDP lengths in network byte order and include
  the UDP header in the length;
- clean up partially installed rules when one half of a dual-stack setup fails;
- limit ICMP Time Exceeded drops to selected inbound interfaces;
- stop raw re-injection of the original outbound datagram and accept its queued
  skb after the decoys, preventing duplicates while preserving kernel metadata;
- use a signal-safe termination flag.

TaoistFuchen 0.9.5 keeps the 0.9.4 packet path unchanged and corrects its
operator-facing reporting:

- describe `-0` as outbound and `-1` as inbound, matching the packet types
  those legacy fields actually enable;
- report IPv4-only, IPv6-only, and dual-stack operation explicitly;
- report outbound-only, inbound-only, and bidirectional operation explicitly.

The OpenWrt package builds this directory with the target SDK and installs the
result as `/usr/bin/fakesip`. No precompiled FakeSIP executable is stored in the
source package. The baseline's router direction flag behavior is unchanged and
is compensated by TaoistFuchen's init script.

IPv6 extension headers are not parsed by this version; queued IPv6 packets must
carry UDP directly after the base IPv6 header to receive a generated decoy.
