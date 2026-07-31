/*
 * srcinfo.c - FakeSIP: https://github.com/MikeWang000000/FakeSIP
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
#include "srcinfo.h"

#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <netinet/in.h>
#include <sys/socket.h>

#include "logging.h"

#define CAPACITY 500

struct srcinfo {
    int initialized;
    uint8_t ttl;
    uint8_t hwaddr[8];
    struct sockaddr_storage addr;
};

static struct srcinfo *srci = NULL;
static size_t srci_end = 0;

static int sameip(struct sockaddr *addr1, struct sockaddr *addr2)
{
    struct sockaddr_in *addr_in_1, *addr_in_2;
    struct sockaddr_in6 *addr_in6_1, *addr_in6_2;

    if (addr1->sa_family != addr2->sa_family) {
        return 0;
    }

    if (addr1->sa_family == AF_INET) {
        addr_in_1 = (struct sockaddr_in *) addr1;
        addr_in_2 = (struct sockaddr_in *) addr2;

        return addr_in_1->sin_addr.s_addr == addr_in_2->sin_addr.s_addr;
    } else if (addr1->sa_family == AF_INET6) {
        addr_in6_1 = (struct sockaddr_in6 *) addr1;
        addr_in6_2 = (struct sockaddr_in6 *) addr2;

        return memcmp(&addr_in6_1->sin6_addr, &addr_in6_2->sin6_addr,
                      sizeof(addr_in6_1->sin6_addr)) == 0;
    }
    return 0;
}


int fs_srcinfo_setup(void)
{
    srci = calloc(CAPACITY, sizeof(*srci));
    if (!srci) {
        E("ERROR: calloc(): %s", strerror(errno));
        return -1;
    }
    srci_end = 0;

    return 0;
}


void fs_srcinfo_cleanup(void)
{
    free(srci);
}


int fs_srcinfo_put(struct sockaddr *addr, uint8_t ttl, uint8_t hwaddr[8])
{
    struct srcinfo *info;

    info = &srci[srci_end];

    if (addr->sa_family == AF_INET) {
        memcpy(&info->addr, addr, sizeof(struct sockaddr_in));
    } else if (addr->sa_family == AF_INET6) {
        memcpy(&info->addr, addr, sizeof(struct sockaddr_in6));
    } else {
        E("ERROR: Unknown sa_family: %d", (int) addr->sa_family);
        return -1;
    }

    info->ttl = ttl;
    memcpy(info->hwaddr, hwaddr, sizeof(info->hwaddr));
    info->initialized = 1;

    srci_end = (srci_end + 1) % CAPACITY;

    return 0;
}


int fs_srcinfo_get(struct sockaddr *addr, uint8_t *ttl, uint8_t hwaddr[8])
{
    size_t i;
    struct srcinfo *info;

    for (i = 0; i < CAPACITY; i++) {
        /*
            "srci_end - i - 1" is size_t arithmetic. With srci_end at 0 and
            i at 0 it wraps to SIZE_MAX, and SIZE_MAX % 500 is 115, not the
            499 the reverse walk expects. Adding CAPACITY first keeps every
            intermediate value positive, so the scan really does start at the
            most recent entry and visits each slot exactly once.
        */
        info = &srci[(srci_end + CAPACITY - i - 1) % CAPACITY];
        if (!info->initialized) {
            return 1;
        }
        if (sameip(addr, (struct sockaddr *) &info->addr)) {
            *ttl = info->ttl;
            memcpy(hwaddr, info->hwaddr, sizeof(info->hwaddr));
            return 0;
        }
    }
    return 1;
}
