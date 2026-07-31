/*
 * srcinfo.h - FakeSIP: https://github.com/MikeWang000000/FakeSIP
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

#ifndef FS_SRCINFO_H
#define FS_SRCINFO_H

#include <stdint.h>
#include <sys/socket.h>

int fs_srcinfo_setup(void);

void fs_srcinfo_cleanup(void);

int fs_srcinfo_put(struct sockaddr *addr, uint8_t ttl, uint8_t hwaddr[8]);

int fs_srcinfo_get(struct sockaddr *addr, uint8_t *ttl, uint8_t hwaddr[8]);

#endif /* FS_SRCINFO_H */
