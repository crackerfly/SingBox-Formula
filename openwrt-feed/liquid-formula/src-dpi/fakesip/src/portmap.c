/*
 * portmap.c - FakeSIP: https://github.com/MikeWang000000/FakeSIP
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
#include "portmap.h"

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>

int fs_portmap_parse(uint8_t **bitmap_ptr, const char *spec)
{
    char *sdup = NULL, *tok, *saveptr;
    int ret = -1;

    if (!spec || !spec[0]) {
        return -1;
    }

    sdup = strdup(spec);
    if (!sdup)
        return -1;

    for (tok = strtok_r(sdup, ",", &saveptr); tok;
         tok = strtok_r(NULL, ",", &saveptr)) {
        char *dash = strchr(tok, '-');
        unsigned long start, end;
        char *endptr;

        if (!dash) {
            start = strtoul(tok, &endptr, 0);
            if (endptr == tok || *endptr != '\0' || start == 0 ||
                start > 65535) {
                goto out;
            }
            end = start;
        } else {
            *dash = '\0';
            start = strtoul(tok, &endptr, 0);
            if (endptr == tok || *endptr != '\0' || start == 0 ||
                start > 65535) {
                goto out;
            }
            end = strtoul(dash + 1, &endptr, 0);
            if (endptr == dash + 1 || *endptr != '\0' || end == 0 ||
                end > 65535) {
                goto out;
            }
            if (start > end)
                goto out;
        }

        if (!*bitmap_ptr) {
            *bitmap_ptr = calloc(1, 8192);
            if (!*bitmap_ptr)
                goto out;
        }

        for (unsigned long p = start; p <= end; p++) {
            fs_portmap_set(*bitmap_ptr, (uint16_t) p);
        }
    }

    ret = 0;
out:
    free(sdup);
    return ret;
}

int fs_portmap_get(const uint8_t *bitmap, uint16_t port)
{
    if (!bitmap)
        return 0;
    return (bitmap[port >> 3] & (uint8_t) (1u << (port & 7))) ? 1 : 0;
}

void fs_portmap_set(uint8_t *bitmap, uint16_t port)
{
    if (!bitmap)
        return;
    bitmap[port >> 3] |= (uint8_t) (1u << (port & 7));
}
