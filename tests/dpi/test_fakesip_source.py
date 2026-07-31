#!/usr/bin/env python3

from __future__ import annotations

import tarfile
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
PKG = ROOT / "openwrt-feed/liquid-formula"
# 本包用 package.mk 直接编译 vendored 源码, 没有 TaoistFuchen 那层 src/Makefile
# 包装器, 等效的构建规则写在包 Makefile 里。
WRAPPER = PKG / "Makefile"
SOURCE = PKG / "src-dpi/fakesip"
FAKEHTTP_SOURCE = PKG / "src-dpi/fakehttp"
ARCHIVE = ROOT / "third_party/sources/FakeSIP-Formula-0.9.8.tar.gz"
ARCHIVE_ROOT = "FakeSIP-Formula-0.9.8"


def text(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def verify_packet_headers() -> None:
    with tempfile.TemporaryDirectory() as directory:
        temporary = Path(directory)
        include = temporary / "include"
        netfilter = include / "libnetfilter_queue"
        netfilter.mkdir(parents=True)
        (include / "globvar.h").write_text("#pragma once\n", encoding="utf-8")
        (include / "logging.h").write_text(
            "#pragma once\n#define E(...) ((void) 0)\n", encoding="utf-8"
        )
        (netfilter / "libnetfilter_queue_ipv6.h").write_text(
            "#pragma once\n", encoding="utf-8"
        )
        (netfilter / "libnetfilter_queue_ipv4.h").write_text(
            """#pragma once
#include <netinet/ip.h>
static inline void nfq_ip_set_checksum(struct iphdr *ip)
{
    (void) ip;
}
""",
            encoding="utf-8",
        )
        (netfilter / "libnetfilter_queue_udp.h").write_text(
            """#pragma once
#include <stdint.h>
#include <netinet/ip.h>
#include <netinet/ip6.h>
#include <netinet/udp.h>
static inline uint16_t nfq_udp_compute_checksum_ipv6(
    struct udphdr *udp, struct ip6_hdr *ip6)
{
    (void) udp;
    (void) ip6;
    return 0;
}
static inline uint16_t nfq_udp_compute_checksum_ipv4(
    struct udphdr *udp, struct iphdr *ip)
{
    (void) udp;
    (void) ip;
    return 0;
}
""",
            encoding="utf-8",
        )
        harness = temporary / "ipv6_packet_test.c"
        harness.write_text(
            """#define _GNU_SOURCE
#include <stdint.h>
#include <string.h>
#include <arpa/inet.h>
#include <netinet/ip.h>
#include <netinet/ip6.h>
#include <netinet/udp.h>
#include <sys/socket.h>

#include "ipv4pkt.h"
#include "ipv6pkt.h"

int main(void)
{
    uint8_t buffer[256] = {0};
    uint8_t buffer4[256] = {0};
    uint8_t payload[5] = {1, 2, 3, 4, 5};
    struct sockaddr_in6 source = {0}, destination = {0};
    struct sockaddr_in source4 = {0}, destination4 = {0};
    struct ip6_hdr *ip6 = (struct ip6_hdr *) buffer;
    struct udphdr *udp = (struct udphdr *) (buffer + sizeof(*ip6));
    size_t expected_udp_length = sizeof(*udp) + sizeof(payload);
    int result;

    source.sin6_family = AF_INET6;
    destination.sin6_family = AF_INET6;
    source.sin6_addr.s6_addr[15] = 1;
    destination.sin6_addr.s6_addr[15] = 2;
    result = fs_pkt6_make(buffer, sizeof(buffer),
                          (struct sockaddr *) &source,
                          (struct sockaddr *) &destination,
                          3, htons(12345), htons(443),
                          payload, sizeof(payload));
    if (result != (int) (sizeof(*ip6) + expected_udp_length))
        return 1;
    if (ntohs(ip6->ip6_plen) != expected_udp_length)
        return 2;
    if (ntohs(udp->len) != expected_udp_length)
        return 3;

    source4.sin_family = AF_INET;
    destination4.sin_family = AF_INET;
    source4.sin_addr.s_addr = htonl(0xc0000201);
    destination4.sin_addr.s_addr = htonl(0xc6336401);
    result = fs_pkt4_make(buffer4, sizeof(buffer4),
                          (struct sockaddr *) &source4,
                          (struct sockaddr *) &destination4,
                          3, htons(12345), htons(443),
                          payload, sizeof(payload));
    if (result != (int) (sizeof(struct iphdr) + expected_udp_length))
        return 4;
    udp = (struct udphdr *) (buffer4 + sizeof(struct iphdr));
    if (ntohs(udp->len) != expected_udp_length)
        return 5;
    return 0;
}
""",
            encoding="utf-8",
        )
        binary = temporary / "ipv6_packet_test"
        subprocess.run(
            [
                "cc",
                "-std=c99",
                "-Wall",
                "-Wextra",
                "-Werror",
                f"-I{include}",
                f"-I{SOURCE / 'include'}",
                str(SOURCE / "src/ipv4pkt.c"),
                str(SOURCE / "src/ipv6pkt.c"),
                str(harness),
                "-o",
                str(binary),
            ],
            check=True,
        )
        subprocess.run([str(binary)], check=True)


def main() -> None:
    assert not (PKG / "root/usr/bin/fakesip").exists()

    wrapper = text(WRAPPER)
    assert "FAKESIP_VERSION:=0.9.8" in wrapper
    assert "$(MAKE) -C $(PKG_BUILD_DIR)/dpi/$(1)" in wrapper
    assert "Build/Compile/dpi,fakesip" in wrapper
    # 交叉工具链必须传进 vendored 构建, 否则会用宿主 gcc 编出跑不了的二进制。
    assert 'CC="$(TARGET_CC)"' in wrapper

    expected = {
        "Makefile",
        "LICENSE",
        "README.md",
        "README.TaoistFuchen.md",
        "include/globvar.h",
        "include/ipv4nft.h",
        "include/ipv6nft.h",
        "include/mainfun.h",
        "include/nfqueue.h",
        "src/globvar.c",
        "src/ipv4nft.c",
        "src/ipv6nft.c",
        "src/mainfun.c",
        "src/nfqueue.c",
    }
    for relative in expected:
        assert (SOURCE / relative).is_file(), relative

    source_makefile = text(SOURCE / "Makefile")
    assert "VERSION ?= 0.9.5" in source_makefile
    assert "STATIC ?= 0" in source_makefile
    assert "PREFIX ?= /usr" in source_makefile

    ipv6nft = text(SOURCE / "src/ipv6nft.c")
    assert "icmpv6 type time-exceeded counter drop" in ipv6nft
    assert '"        icmp type time-exceeded counter drop' not in ipv6nft
    assert 'insert rule ip6 fakesip fs_prerouting iifname \\\"%s\\\" "' in ipv6nft
    assert '"icmpv6 type time-exceeded counter drop"' in ipv6nft
    assert '"        icmpv6 type time-exceeded counter drop' not in ipv6nft
    assert ipv6nft.count("fs_execute_command(nft_cmd, 0, nft_conf_buff)") == 1

    ipv4nft = text(SOURCE / "src/ipv4nft.c")
    assert 'insert rule ip fakesip fs_prerouting iifname \\\"%s\\\" "' in ipv4nft
    assert '"icmp type time-exceeded counter drop"' in ipv4nft
    assert '"        icmp type time-exceeded counter drop' not in ipv4nft

    ipv6ipt = text(SOURCE / "src/ipv6ipt.c")
    ipv6ipt_compact = " ".join(ipv6ipt.split())
    assert '"-I", "FAKESIP_S", "1", "-i", iface_str, "-p", "icmpv6"' in ipv6ipt_compact
    ipv6ipt_setup = ipv6ipt[ipv6ipt.index("int fs_ipt6_setup") :]
    assert '"FAKESIP_S", "-p", "icmpv6"' not in ipv6ipt_setup
    ipv4ipt = text(SOURCE / "src/ipv4ipt.c")
    ipv4ipt_compact = " ".join(ipv4ipt.split())
    assert '"-I", "FAKESIP_S", "1", "-i", iface_str, "-p", "icmp"' in ipv4ipt_compact
    ipv4ipt_setup = ipv4ipt[ipv4ipt.index("int fs_ipt4_setup") :]
    assert '"FAKESIP_S", "-p", "icmp"' not in ipv4ipt_setup

    ipv6pkt = text(SOURCE / "src/ipv6pkt.c")
    assert "udph->len = htons(sizeof(*udph) + udp_payload_size);" in ipv6pkt
    ipv4pkt = text(SOURCE / "src/ipv4pkt.c")
    assert "udph->len = htons(sizeof(*udph) + udp_payload_size);" in ipv4pkt
    mainfun = text(SOURCE / "src/mainfun.c")
    assert '"  -0                 process outbound packets\\n"' in mainfun
    assert '"  -1                 process inbound packets\\n"' in mainfun
    assert 'ipproto_info = " (IPv4 only)";' in mainfun
    assert 'ipproto_info = " (IPv6 only)";' in mainfun
    assert 'ipproto_info = " (IPv4 and IPv6)";' in mainfun
    assert 'direction_info = " (outbound only)";' in mainfun
    assert 'direction_info = " (inbound only)";' in mainfun
    assert 'direction_info = " (inbound and outbound)";' in mainfun
    reporting = " ".join(
        mainfun[
            mainfun.index("if (g_ctx.inbound && !g_ctx.outbound)") :
            mainfun.index('E("listening on')
        ].split()
    )
    assert (
        'if (g_ctx.inbound && !g_ctx.outbound) { direction_info = " (outbound only)"; '
        '} else if (!g_ctx.inbound && g_ctx.outbound) { direction_info = " (inbound only)"; '
        '} else { direction_info = " (inbound and outbound)"; }'
    ) in reporting
    option_zero = mainfun[mainfun.index("case '0':") : mainfun.index("case '1':")]
    option_one = mainfun[mainfun.index("case '1':") : mainfun.index("case '4':")]
    assert "g_ctx.inbound = 1;" in option_zero
    assert "g_ctx.outbound = 1;" in option_one
    rules_failure = mainfun.index("res = fs_nfrules_setup();")
    signal_setup = mainfun.index("res = fs_signal_setup();")
    assert "goto cleanup_nfrules;" in mainfun[rules_failure:signal_setup]
    assert "cleanup_nfq:" not in mainfun

    # A dual-stack FakeHTTP setup can install IPv4 rules and then fail while
    # installing IPv6. Its setup failure must traverse the rules cleanup label,
    # just like FakeSIP, rather than leaking the first address family's rules.
    fakehttp_mainfun = text(FAKEHTTP_SOURCE / "src/mainfun.c")
    rules_failure = fakehttp_mainfun.index("res = fh_nfrules_setup();")
    signal_setup = fakehttp_mainfun.index("res = fh_signal_setup();")
    assert "goto cleanup_nfrules;" in fakehttp_mainfun[rules_failure:signal_setup]
    assert "cleanup_nfq:" not in fakehttp_mainfun

    rawsend = text(SOURCE / "src/rawsend.c")
    send_payload = rawsend[
        rawsend.index("static int send_payload") : rawsend.index("int rawsock_setup")
    ]
    assert "ssize_t nbytes;" in send_payload
    outgoing = rawsend[rawsend.index("PACKET_OUTGOING") :]
    next_branch = outgoing.index("} else {")
    outgoing_branch = outgoing[:next_branch]
    assert "sendto_snat(sll, daddr, pkt_data, pkt_len)" not in outgoing_branch
    assert "return NF_ACCEPT;" in outgoing_branch
    assert "return NF_DROP;" not in outgoing_branch
    handle_locals = rawsend[rawsend.index("int fs_rawsend_handle") : rawsend.index("*modified = 0")]
    assert "ssize_t nbytes;" not in handle_locals
    packet_host = rawsend[rawsend.index("PACKET_HOST") : rawsend.index("PACKET_OUTGOING")]
    packet_outgoing = rawsend[rawsend.index("PACKET_OUTGOING") :]
    assert "if (!g_ctx.outbound)" in packet_host
    assert "if (!g_ctx.inbound)" in packet_outgoing

    globvar = text(SOURCE / "include/globvar.h")
    assert "volatile sig_atomic_t exit;" in globvar
    assert "-0 enables PACKET_OUTGOING" in globvar
    assert "-1 enables PACKET_HOST" in globvar
    source_readme = text(SOURCE / "README.md")
    assert "-0                 process outbound packets" in source_readme
    assert "-1                 process inbound packets" in source_readme
    verify_packet_headers()

    package_makefile = text(PKG / "Makefile")
    # 本包不是 LuCI 应用, 依赖写在标准 DEPENDS 里。
    assert "libnetfilter-queue" in package_makefile
    assert "kmod-nft-queue" in package_makefile

    provenance = text(ROOT / "THIRD_PARTY_SOURCES.md")
    assert "FakeSIP 0.9.8" in provenance
    assert "bb6fdd88e7fa6f6d4fb1b02e359e5e68c7d778b6" in provenance
    assert "icmpv6 type time-exceeded" in provenance
    assert "compiled by the pinned OpenWrt SDK" in provenance

    assert ARCHIVE.is_file()
    assert not (
        ROOT / "third_party/sources/FakeSIP-TaoistFuchen-0.9.4.tar.gz"
    ).exists()
    assert not (ROOT / "third_party/sources/FakeSIP-Droid-MAX-0.9.3.tar.gz").exists()
    source_files = {
        path.relative_to(SOURCE).as_posix(): path.read_bytes()
        for path in SOURCE.rglob("*")
        if path.is_file() and "build" not in path.relative_to(SOURCE).parts
    }
    with tarfile.open(ARCHIVE, "r:gz") as archive:
        archived_files = {}
        for member in archive.getmembers():
            if not member.isfile():
                continue
            prefix = f"{ARCHIVE_ROOT}/"
            assert member.name.startswith(prefix), member.name
            relative = member.name[len(prefix) :]
            extracted = archive.extractfile(member)
            assert extracted is not None
            archived_files[relative] = extracted.read()
        assert archived_files.keys() == source_files.keys()
        for relative, contents in source_files.items():
            assert archived_files[relative] == contents, relative

    print("FakeSIP source policy tests: ok")


if __name__ == "__main__":
    main()
