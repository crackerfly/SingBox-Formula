/*
 * srcinfo_wrap_test.c - 源信息环形缓存的回绕回归
 *
 * 反向扫描原本写成 "(srci_end - i - 1) % CAPACITY"。srci_end 是 size_t, 当它
 * 刚好绕回 0 时 "0 - 0 - 1" 会变成 SIZE_MAX, 而 SIZE_MAX % 500 是 115, 不是
 * 反向扫描期望的 499。于是扫描从错误的槽位开始, 同一个 IP 若在环首尾各出现
 * 一次, 查到的会是旧那条 —— 诱饵 TTL 就用了过期的路径估算值。
 *
 * 这里精确构造那个场景: 把环填满整整一圈(srci_end 回到 0), 让目标 IP 在第
 * 115 槽和第 499 槽各出现一次, 然后要求查出来的是后写入的那条。
 */

#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

#include "globvar.h"
#include "logging.h"
#include "srcinfo.h"

#define CAPACITY 500
#define TARGET_EARLY_SLOT 115
#define TTL_OLD 11
#define TTL_NEW 99

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

static void make_addr(struct sockaddr_in *sin, uint32_t host)
{
    memset(sin, 0, sizeof(*sin));
    sin->sin_family = AF_INET;
    sin->sin_addr.s_addr = htonl(host);
}

static int put_host(uint32_t host, uint8_t ttl)
{
    struct sockaddr_in sin;
    uint8_t hwaddr[8];

    make_addr(&sin, host);
    memset(hwaddr, (int) (host & 0xff), sizeof(hwaddr));
    return SRCINFO_PUT((struct sockaddr *) &sin, ttl, hwaddr);
}

int main(void)
{
    struct sockaddr_in target;
    uint8_t hwaddr[8];
    uint8_t ttl = 0;
    uint32_t filler;
    uint32_t i;
    int res;

    g_ctx.logfp = stderr;
    g_ctx.silent = 1;

    if (SRCINFO_SETUP() < 0) {
        printf("not ok 1 - srcinfo setup\n1 checks, 1 failures\n");
        return 1;
    }

    /* 目标 IP 用一个不会和填充项撞车的地址。 */
    make_addr(&target, 0xC0000201u); /* 192.0.2.1 */

    filler = 0x0A000000u;

    /* 槽 0..114: 填充。 */
    for (i = 0; i < TARGET_EARLY_SLOT; i++) {
        put_host(filler + i, 60);
    }

    /* 槽 115: 目标 IP 的旧记录。 */
    put_host(0xC0000201u, TTL_OLD);

    /* 槽 116..498: 继续填充。 */
    for (i = TARGET_EARLY_SLOT + 1; i < CAPACITY - 1; i++) {
        put_host(filler + i, 60);
    }

    /* 槽 499: 目标 IP 的新记录。写完这条 srci_end 正好绕回 0。 */
    put_host(0xC0000201u, TTL_NEW);

    memset(hwaddr, 0, sizeof(hwaddr));
    res = SRCINFO_GET((struct sockaddr *) &target, &ttl, hwaddr);

    expect(res == 0, "a fully wrapped ring still resolves a known peer");
    expect(ttl == TTL_NEW,
           "the reverse scan returns the newest record, not an older duplicate");

    /* 从未见过的地址必须查不到, 否则说明扫描读到了不该读的槽。 */
    {
        struct sockaddr_in unknown;
        uint8_t unknown_ttl = 0;

        make_addr(&unknown, 0xC0000299u); /* 192.0.2.153 */
        res = SRCINFO_GET((struct sockaddr *) &unknown, &unknown_ttl, hwaddr);
        expect(res != 0, "an address that was never recorded is not resolved");
    }

    SRCINFO_CLEANUP();

    printf("%d checks, %d failures\n", checks, failures);
    return failures ? 1 : 0;
}
