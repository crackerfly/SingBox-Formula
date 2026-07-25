/*
 * logging.h - FakeHTTP: https://github.com/MikeWang000000/FakeHTTP
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

#ifndef FH_LOGGING_H
#define FH_LOGGING_H

#define T(FUNC)    ("    at " #FUNC "()")
#define E(...)     fh_logger(__func__, __FILE__, __LINE__, 0, __VA_ARGS__)
#define EE(...)    fh_logger(__func__, __FILE__, __LINE__, 1, __VA_ARGS__)
#define E_RAW(...) fh_logger_raw(__VA_ARGS__)
#define E_INFO(...)      \
    if (!g_ctx.silent) { \
        E(__VA_ARGS__);  \
    }

int fh_logger_setup(void);

void fh_logger_cleanup(void);

void fh_logger(const char *funcname, const char *filename, unsigned long line,
               int end, const char *fmt, ...);

void fh_logger_raw(const char *fmt, ...);

#endif /* FH_LOGGING_H */
