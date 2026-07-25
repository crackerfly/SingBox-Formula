/*
 * ipv6nft.c - FakeSIP: https://github.com/MikeWang000000/FakeSIP
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
#include "ipv6nft.h"

#include <inttypes.h>
#include <stdlib.h>

#include "globvar.h"
#include "logging.h"
#include "process.h"

static int nft6_iface_setup(void)
{
    char nftstr[120];
    size_t i;
    int res;
    char *nft_iface_cmd[] = {"nft", nftstr, NULL};

    if (g_ctx.alliface) {
        res = snprintf(nftstr, sizeof(nftstr),
                       "insert rule ip6 fakesip fs_prerouting "
                       "icmpv6 type time-exceeded counter drop");
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fs_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        res = snprintf(nftstr, sizeof(nftstr),
                       "add rule ip6 fakesip fs_prerouting jump fs_rules");
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fs_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        res = snprintf(nftstr, sizeof(nftstr),
                       "add rule ip6 fakesip fs_postrouting jump fs_rules");
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fs_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        return 0;
    }

    for (i = 0; g_ctx.iface[i]; i++) {
        res = snprintf(nftstr, sizeof(nftstr),
                       "insert rule ip6 fakesip fs_prerouting iifname \"%s\" "
                       "icmpv6 type time-exceeded counter drop",
                       g_ctx.iface[i]);
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fs_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        res = snprintf(nftstr, sizeof(nftstr),
                       "add rule ip6 fakesip fs_prerouting iifname \"%s\" "
                       "jump fs_rules",
                       g_ctx.iface[i]);
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fs_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        res = snprintf(nftstr, sizeof(nftstr),
                       "add rule ip6 fakesip fs_postrouting oifname \"%s\" "
                       "jump fs_rules",
                       g_ctx.iface[i]);
        if (res < 0 || (size_t) res >= sizeof(nftstr)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }
        res = fs_execute_command(nft_iface_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }
    }
    return 0;
}


int fs_nft6_setup(void)
{
    int res;
    char *nft_cmd[] = {"nft", "-f", "-", NULL};
    char nft_conf_buff[2048];
    char *nft_conf_fmt =
        "table ip6 fakesip {\n"
        "    chain fs_prerouting {\n"
        "        type filter hook prerouting priority mangle - 5;\n"
        "        policy accept;\n"
        /*
            exclude non-GUA IPv6 addresses (from source)
        */
        "        ip6 saddr != 2000::/3 return;\n"
        "    }\n"
        "\n"
        "    chain fs_postrouting {\n"
        "        type filter hook postrouting priority mangle - 5;\n"
        "        policy accept;\n"
        /*
            exclude non-GUA IPv6 addresses (to destination)
        */
        "        ip6 daddr != 2000::/3 return;\n"
        "    }\n"
        "\n"
        "    chain fs_rules {\n"

        /*
            exclude marked packets
        */
        "        meta mark and %" PRIu32 " == %" PRIu32 " return;\n"

        /*
            send to nfqueue
        */
        "        meta l4proto udp ct packets 1-5 queue num %" PRIu32
        " bypass;\n"

        "    }\n"
        "}\n";

    fs_nft6_cleanup();

    res = snprintf(nft_conf_buff, sizeof(nft_conf_buff), nft_conf_fmt,
                   g_ctx.fwmask, g_ctx.fwmark, g_ctx.nfqnum);
    if (res < 0 || (size_t) res >= sizeof(nft_conf_buff)) {
        E("ERROR: snprintf(): %s", "failure");
        return -1;
    }

    res = fs_execute_command(nft_cmd, 0, nft_conf_buff);
    if (res < 0) {
        E(T(fs_execute_command));
        return -1;
    }

    res = nft6_iface_setup();
    if (res < 0) {
        E(T(nft6_iface_setup));
        return -1;
    }

    return 0;
}


void fs_nft6_cleanup(void)
{
    char *nft_delete_cmd[] = {"nft", "delete table ip6 fakesip", NULL};

    fs_execute_command(nft_delete_cmd, 1, NULL);
}
