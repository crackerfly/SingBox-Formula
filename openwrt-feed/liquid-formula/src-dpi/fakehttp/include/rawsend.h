/*
 * rawsend.h - FakeHTTP: https://github.com/MikeWang000000/FakeHTTP
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

#ifndef FH_RAWSEND_H
#define FH_RAWSEND_H

#include <stdint.h>
#include <linux/if_packet.h>

int fh_rawsend_setup(void);

void fh_rawsend_cleanup(void);

int fh_rawsend_handle(struct sockaddr_ll *sll, uint8_t *pkt_data, int pkt_len,
                      int *modified);

#endif /* FH_RAWSEND_H */
