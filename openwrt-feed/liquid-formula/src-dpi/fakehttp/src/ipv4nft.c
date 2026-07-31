/*
 * ipv4nft.c - FakeHTTP: https://github.com/MikeWang000000/FakeHTTP
 *
 * Copyright (C) 2025  MikeWang000000
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

#define _GNU_SOURCE
#include "ipv4nft.h"

#include <inttypes.h>
#include <stdlib.h>

#include "globvar.h"
#include "logging.h"
#include "process.h"

static int nft4_iface_setup(void)
{
    char nftstr[120];
    size_t i;
    int res;
    char *nft_iface_cmd[] = {"nft", nftstr, NULL};

    if (g_ctx.alliface) {
        res = snprintf(nftstr, sizeof(nftstr),
                       "add rule ip fakehttp fh_prerouting jump fh_rules");
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fh_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fh_execute_command));
            return -1;
        }

        res = snprintf(nftstr, sizeof(nftstr),
                       "add rule ip fakehttp fh_postrouting jump fh_rules");
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fh_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fh_execute_command));
            return -1;
        }

        return 0;
    }

    for (i = 0; g_ctx.iface[i]; i++) {
        res = snprintf(
            nftstr, sizeof(nftstr),
            "add rule ip fakehttp fh_prerouting iifname \"%s\" jump fh_rules",
            g_ctx.iface[i]);
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fh_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fh_execute_command));
            return -1;
        }

        res = snprintf(
            nftstr, sizeof(nftstr),
            "add rule ip fakehttp fh_postrouting oifname \"%s\" jump fh_rules",
            g_ctx.iface[i]);
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fh_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fh_execute_command));
            return -1;
        }
    }
    return 0;
}


int fh_nft4_setup(void)
{
    int res;
    char *nft_cmd[] = {"nft", "-f", "-", NULL};
    char nft_conf_buff[2048];
    char *nft_conf_fmt =
        "table ip fakehttp {\n"
        "    chain fh_prerouting {\n"
        "        type filter hook prerouting priority mangle - 5;\n"
        "        policy accept;\n"
        /*
            exclude local IPs (from source)
        */
        "        ip saddr 0.0.0.0/8      return;\n"
        "        ip saddr 10.0.0.0/8     return;\n"
        "        ip saddr 100.64.0.0/10  return;\n"
        "        ip saddr 127.0.0.0/8    return;\n"
        "        ip saddr 169.254.0.0/16 return;\n"
        "        ip saddr 172.16.0.0/12  return;\n"
        "        ip saddr 192.168.0.0/16 return;\n"
        "        ip saddr 224.0.0.0/3    return;\n"
        "    }\n"
        "\n"
        "    chain fh_postrouting {\n"
        "        type filter hook postrouting priority srcnat + 5;\n"
        "        policy accept;\n"
        /*
            exclude local IPs (to destination)
        */
        "        ip daddr 0.0.0.0/8      return;\n"
        "        ip daddr 10.0.0.0/8     return;\n"
        "        ip daddr 100.64.0.0/10  return;\n"
        "        ip daddr 127.0.0.0/8    return;\n"
        "        ip daddr 169.254.0.0/16 return;\n"
        "        ip daddr 172.16.0.0/12  return;\n"
        "        ip daddr 192.168.0.0/16 return;\n"
        "        ip daddr 224.0.0.0/3    return;\n"
        "    }\n"
        "\n"
        "    chain fh_rules {\n"
        /*
            exclude marked packets
        */
        "        meta mark and %" PRIu32 " == %" PRIu32 " return;\n"
        /*
            send to nfqueue
        */
        "        tcp flags & (syn | fin | rst) == syn queue num %" PRIu32
        " bypass;\n"

        "    }\n"
        "}\n";

    char *nft_conf_opt_fmt =
        "add rule ip fakehttp fh_rules tcp flags & (syn | ack | fin | rst) "
        "== ack ct packets 2-4 queue num %" PRIu32 " bypass;\n";

    fh_nft4_cleanup();

    res = snprintf(nft_conf_buff, sizeof(nft_conf_buff), nft_conf_fmt,
                   g_ctx.fwmask, g_ctx.fwmark, g_ctx.nfqnum);
    if (res < 0 || (size_t) res >= sizeof(nft_conf_buff)) {
        E("ERROR: snprintf(): %s", "failure");
        return -1;
    }

    res = fh_execute_command(nft_cmd, 0, nft_conf_buff);
    if (res < 0) {
        E(T(fh_execute_command));
        return -1;
    }

    /*
        Also enqueue some of the early ACK packets to ensure the packet order.
        This rule is optional. We do not verify its execution result.
    */
    res = snprintf(nft_conf_buff, sizeof(nft_conf_buff), nft_conf_opt_fmt,
                   g_ctx.nfqnum);
    if (res < 0 || (size_t) res >= sizeof(nft_conf_buff)) {
        E("ERROR: snprintf(): %s", "failure");
        return -1;
    }

    fh_execute_command(nft_cmd, 0, nft_conf_buff);

    res = nft4_iface_setup();
    if (res < 0) {
        E(T(nft4_iface_setup));
        return -1;
    }

    return 0;
}


void fh_nft4_cleanup(void)
{
    char *nft_delete_cmd[] = {"nft", "delete table ip fakehttp", NULL};

    fh_execute_command(nft_delete_cmd, 1, NULL);
}
