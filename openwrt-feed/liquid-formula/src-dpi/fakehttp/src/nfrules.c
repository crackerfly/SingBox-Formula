/*
 * nfrules.c - FakeHTTP: https://github.com/MikeWang000000/FakeHTTP
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
#include "nfrules.h"

#include <stdlib.h>

#include "globvar.h"
#include "ipv4ipt.h"
#include "ipv6ipt.h"
#include "ipv4nft.h"
#include "ipv6nft.h"
#include "logging.h"
#include "process.h"

static int nft_is_working(void)
{
    char *nft_ver_cmd[] = {"nft", "--version", NULL};

    return !fh_execute_command(nft_ver_cmd, 1, NULL);
}


int fh_nfrules_setup(void)
{
    int res;

    if (g_ctx.skipfw) {
        E("Skip firewall rules as requested.");
        return 0;
    }

    if (!g_ctx.use_iptables && !nft_is_working()) {
        E("WARNING: Falling back to iptables command, as nft command is not "
          "working.");
        g_ctx.use_iptables = 1;
    }

    if (g_ctx.use_iptables) {
        if (g_ctx.use_ipv4) {
            res = fh_ipt4_setup();
            if (res < 0) {
                E(T(fh_ipt4_setup));
                return -1;
            }
        }

        if (g_ctx.use_ipv6) {
            res = fh_ipt6_setup();
            if (res < 0) {
                E(T(fh_ipt6_setup));
                return -1;
            }
        }
    } else {
        if (g_ctx.use_ipv4) {
            res = fh_nft4_setup();
            if (res < 0) {
                E(T(fh_nft4_setup));
                return -1;
            }
        }

        if (g_ctx.use_ipv6) {
            res = fh_nft6_setup();
            if (res < 0) {
                E(T(fh_nft6_setup));
                return -1;
            }
        }
    }

    return 0;
}


void fh_nfrules_cleanup(void)
{
    if (g_ctx.skipfw) {
        return;
    }

    if (g_ctx.use_iptables) {
        if (g_ctx.use_ipv4) {
            fh_ipt4_cleanup();
        }

        if (g_ctx.use_ipv6) {
            fh_ipt6_cleanup();
        }
    } else {
        if (g_ctx.use_ipv4) {
            fh_nft4_cleanup();
        }

        if (g_ctx.use_ipv6) {
            fh_nft6_cleanup();
        }
    }
}
