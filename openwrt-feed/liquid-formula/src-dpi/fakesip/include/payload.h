/*
 * payload.h - FakeSIP: https://github.com/MikeWang000000/FakeSIP
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

#ifndef FS_PAYLOAD_H
#define FS_PAYLOAD_H

#include <stddef.h>
#include <stdint.h>

enum payload_type {
    FS_PAYLOAD_END = 0,
    FS_PAYLOAD_SIP,
    FS_PAYLOAD_CUSTOM
};

struct payload_info {
    enum payload_type type;
    char *info;
};

int fs_payload_setup(void);

void fs_payload_cleanup(void);

void th_payload_get(uint8_t **payload_ptr, size_t *payload_len);

#endif /* FS_PAYLOAD_H */
