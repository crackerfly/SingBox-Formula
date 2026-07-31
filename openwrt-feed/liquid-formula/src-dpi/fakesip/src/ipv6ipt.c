/*
 * ipv6ipt.c - FakeSIP: https://github.com/MikeWang000000/FakeSIP
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
#include "ipv6ipt.h"

#include <inttypes.h>
#include <stdlib.h>
#include <net/if.h>

#include "globvar.h"
#include "logging.h"
#include "process.h"

static int ipt6_iface_setup(void)
{
    char iface_str[IFNAMSIZ];
    size_t i;
    int res;
    char *ipt_alliface_icmp_cmd[] = {
        "ip6tables", "-w", "-t",   "mangle", "-I",
        "FAKESIP_S", "1",  "-p",   "icmpv6", "--icmpv6-type",
        "3",         "-j", "DROP", NULL};
    char *ipt_alliface_src_cmd[] = {"ip6tables", "-w",        "-t",
                                    "mangle",    "-A",        "FAKESIP_S",
                                    "-j",        "FAKESIP_R", NULL};

    char *ipt_alliface_dst_cmd[] = {"ip6tables", "-w",        "-t",
                                    "mangle",    "-A",        "FAKESIP_D",
                                    "-j",        "FAKESIP_R", NULL};

    char *ipt_iface_src_cmd[] = {"ip6tables", "-w",        "-t", "mangle",
                                 "-A",        "FAKESIP_S", "-i", iface_str,
                                 "-j",        "FAKESIP_R", NULL};
    char *ipt_iface_icmp_cmd[] = {
        "ip6tables", "-w", "-t",      "mangle", "-I",     "FAKESIP_S",
        "1",         "-i", iface_str, "-p",     "icmpv6", "--icmpv6-type",
        "3",         "-j", "DROP",    NULL};

    char *ipt_iface_dst_cmd[] = {"ip6tables", "-w",        "-t", "mangle",
                                 "-A",        "FAKESIP_D", "-o", iface_str,
                                 "-j",        "FAKESIP_R", NULL};

    if (g_ctx.alliface) {
        res = fs_execute_command(ipt_alliface_icmp_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }
        res = fs_execute_command(ipt_alliface_src_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }
        res = fs_execute_command(ipt_alliface_dst_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }
        return 0;
    }

    for (i = 0; g_ctx.iface[i]; i++) {
        res = snprintf(iface_str, sizeof(iface_str), "%s", g_ctx.iface[i]);
        if (res < 0 || (size_t) res >= sizeof(iface_str)) {
            E("ERROR: snprintf(): %s", "failure");
            return -1;
        }

        res = fs_execute_command(ipt_iface_icmp_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        res = fs_execute_command(ipt_iface_src_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }

        res = fs_execute_command(ipt_iface_dst_cmd, 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }
    }
    return 0;
}


int fs_ipt6_setup(void)
{
    char xmark_str[64], nfqnum_str[32];
    size_t i, ipt_cmds_cnt;
    int res;
    char *ipt_cmds[][32] = {
        {"ip6tables", "-w", "-t", "mangle", "-N", "FAKESIP_S", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-N", "FAKESIP_D", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-I", "PREROUTING", "-j",
         "FAKESIP_S", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-I", "POSTROUTING", "-j",
         "FAKESIP_D", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-N", "FAKESIP_R", NULL},

        /*
            exclude non-GUA IPv6 addresses (from source)
        */
        {"ip6tables", "-w", "-t", "mangle", "-A", "FAKESIP_S", "!", "-s",
         "2000::/3", "-j", "RETURN", NULL},

        /*
            exclude non-GUA IPv6 addresses (to destination)
        */
        {"ip6tables", "-w", "-t", "mangle", "-A", "FAKESIP_D", "!", "-d",
         "2000::/3", "-j", "RETURN", NULL},

        /*
            exclude marked packets
        */
        {"ip6tables", "-w", "-t", "mangle", "-A", "FAKESIP_R", "-m", "mark",
         "--mark", xmark_str, "-j", "RETURN", NULL},

        /*
            send to nfqueue
        */
        {"ip6tables",
         "-w",
         "-t",
         "mangle",
         "-A",
         "FAKESIP_R",
         "-p",
         "udp",
         "-m",
         "connbytes",
         "--connbytes",
         "1:5",
         "--connbytes-dir",
         "both",
         "--connbytes-mode",
         "packets",
         "-j",
         "NFQUEUE",
         "--queue-bypass",
         "--queue-num",
         nfqnum_str,
         NULL}};

    ipt_cmds_cnt = sizeof(ipt_cmds) / sizeof(*ipt_cmds);

    res = snprintf(xmark_str, sizeof(xmark_str), "%" PRIu32 "/%" PRIu32,
                   g_ctx.fwmark, g_ctx.fwmask);
    if (res < 0 || (size_t) res >= sizeof(xmark_str)) {
        E("ERROR: snprintf(): %s", "failure");
        return -1;
    }

    res = snprintf(nfqnum_str, sizeof(nfqnum_str), "%" PRIu32, g_ctx.nfqnum);
    if (res < 0 || (size_t) res >= sizeof(nfqnum_str)) {
        E("ERROR: snprintf(): %s", "failure");
        return -1;
    }

    fs_ipt6_cleanup();

    for (i = 0; i < ipt_cmds_cnt; i++) {
        res = fs_execute_command(ipt_cmds[i], 0, NULL);
        if (res < 0) {
            E(T(fs_execute_command));
            return -1;
        }
    }

    res = ipt6_iface_setup();
    if (res < 0) {
        E(T(ipt6_iface_setup));
        return -1;
    }

    return 0;
}


void fs_ipt6_cleanup(void)
{
    size_t i, cnt;
    char *ipt_cmds[][32] = {
        {"ip6tables", "-w", "-t", "mangle", "-F", "FAKESIP_R", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-F", "FAKESIP_S", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-F", "FAKESIP_D", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-D", "PREROUTING", "-j",
         "FAKESIP_S", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-D", "POSTROUTING", "-j",
         "FAKESIP_D", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-X", "FAKESIP_R", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-X", "FAKESIP_S", NULL},

        {"ip6tables", "-w", "-t", "mangle", "-X", "FAKESIP_D", NULL}};

    cnt = sizeof(ipt_cmds) / sizeof(*ipt_cmds);
    for (i = 0; i < cnt; i++) {
        fs_execute_command(ipt_cmds[i], 1, NULL);
    }
}
