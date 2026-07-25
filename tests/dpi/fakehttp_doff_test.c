/*
 * fakehttp_doff_test.c - 畸形 TCP data offset 的拒绝测试
 *
 * 起因: 上游 FakeHTTP 0.9.18 不校验 doff >= 5。remove_tfo_cookie() 里的
 * "doff * 4 - sizeof(struct tcphdr)" 是 size_t 运算, doff 为 0..4 时下溢成
 * 巨大值, 随后的选项扫描会走出 NFQUEUE 缓冲区。转发的 SYN 在内核解析 TCP
 * 头之前就到达 NFQUEUE, 所以默认的 outbound 方向就足以触发。
 *
 * 这里直接喂构造好的包, 断言解析器拒绝 doff < 5 且接受合法值。
 */

#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/ip.h>
#include <netinet/ip6.h>
#include <netinet/tcp.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

#include "globvar.h"
#include "ipv4pkt.h"
#include "ipv6pkt.h"
#include "logging.h"

static int checks;
static int failures;

static void expect(int condition, const char *description)
{
    checks++;
    if (condition) {
        printf("ok %d - %s\n", checks, description);
    } else {
        failures++;
        printf("not ok %d - %s\n", checks, description);
    }
}

static int parse4_with_doff(unsigned int doff)
{
    uint8_t packet[128];
    struct iphdr *iph;
    struct tcphdr *tcph;
    struct sockaddr_storage saddr, daddr;
    struct tcphdr *tcph_out;
    uint8_t ttl;
    int payload_len;

    memset(packet, 0, sizeof(packet));

    iph = (struct iphdr *) packet;
    iph->version = 4;
    iph->ihl = 5;
    iph->protocol = IPPROTO_TCP;
    iph->saddr = inet_addr("192.0.2.1");
    iph->daddr = inet_addr("192.0.2.2");
    iph->ttl = 64;
    iph->tot_len = htons(sizeof(packet));

    tcph = (struct tcphdr *) (packet + 20);
    tcph->source = htons(12345);
    tcph->dest = htons(80);
    tcph->syn = 1;
    tcph->doff = doff & 0xf;

    return fh_pkt4_parse(packet, (int) sizeof(packet),
                         (struct sockaddr *) &saddr, (struct sockaddr *) &daddr,
                         &ttl, &tcph_out, &payload_len);
}

static int parse6_with_doff(unsigned int doff)
{
    uint8_t packet[128];
    struct ip6_hdr *ip6h;
    struct tcphdr *tcph;
    struct sockaddr_storage saddr, daddr;
    struct tcphdr *tcph_out;
    uint8_t ttl;
    int payload_len;

    memset(packet, 0, sizeof(packet));

    ip6h = (struct ip6_hdr *) packet;
    ip6h->ip6_vfc = 0x60;
    ip6h->ip6_nxt = IPPROTO_TCP;
    ip6h->ip6_hlim = 64;
    ip6h->ip6_plen = htons(sizeof(packet) - 40);
    inet_pton(AF_INET6, "2001:db8::1", &ip6h->ip6_src);
    inet_pton(AF_INET6, "2001:db8::2", &ip6h->ip6_dst);

    tcph = (struct tcphdr *) (packet + 40);
    tcph->source = htons(12345);
    tcph->dest = htons(80);
    tcph->syn = 1;
    tcph->doff = doff & 0xf;

    return fh_pkt6_parse(packet, (int) sizeof(packet),
                         (struct sockaddr *) &saddr, (struct sockaddr *) &daddr,
                         &ttl, &tcph_out, &payload_len);
}

int main(void)
{
    unsigned int doff;
    char description[128];

    /* 解析器出错时会走 E() 日志宏, 它需要一个已打开的输出流。 */
    g_ctx.logfp = stderr;
    g_ctx.silent = 1;

    /* doff 0..4 都在 20 字节固定头之下, 必须拒绝。 */
    for (doff = 0; doff < 5; doff++) {
        snprintf(description, sizeof(description),
                 "IPv4 parser rejects TCP data offset %u", doff);
        expect(parse4_with_doff(doff) < 0, description);

        snprintf(description, sizeof(description),
                 "IPv6 parser rejects TCP data offset %u", doff);
        expect(parse6_with_doff(doff) < 0, description);
    }

    /* 合法值必须照常通过, 否则修复就把正常流量也挡了。 */
    for (doff = 5; doff <= 8; doff++) {
        snprintf(description, sizeof(description),
                 "IPv4 parser accepts TCP data offset %u", doff);
        expect(parse4_with_doff(doff) == 0, description);

        snprintf(description, sizeof(description),
                 "IPv6 parser accepts TCP data offset %u", doff);
        expect(parse6_with_doff(doff) == 0, description);
    }

    printf("%d checks, %d failures\n", checks, failures);
    return failures ? 1 : 0;
}
