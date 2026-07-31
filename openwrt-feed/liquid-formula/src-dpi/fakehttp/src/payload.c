/*
 * payload.c - FakeHTTP: https://github.com/MikeWang000000/FakeHTTP
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
#include "payload.h"

#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "logging.h"
#include "globvar.h"

#define BUFFLEN 1200
#define SET_BE16(a, u16)         \
    do {                         \
        (a)[0] = (u16) >> (8);   \
        (a)[1] = (u16) & (0xff); \
    } while (0)

struct payload_node {
    uint8_t payload[BUFFLEN];
    size_t payload_len;
    struct payload_node *next;
};

static const char *http_fmt =
    "GET / HTTP/1.1\r\n"
    "Host: %s\r\n"
    "Accept: */*\r\n"
    "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36\r\n"
    "\r\n";

struct tls_ext_server_name_head {
    uint8_t type[2];
    uint8_t length[2];
    uint8_t server_name_list_length[2];
    uint8_t server_name_type;
    uint8_t server_name_length[2];
};

struct tls_ext_padding_head {
    uint8_t type[2];
    uint8_t length[2];
};

static const struct tls_client_hello {
    uint8_t data_01[11];
    uint8_t random[32];
    uint8_t session_id_length;
    uint8_t session_id[32];
    uint8_t data_02[39];
    uint8_t data_sni[275];
} cli_hello_tmpl = {
    .data_01 =
        {
            0x16,             /* handshake */
            0x03, 0x03,       /* tlsv1.2 */
            0x01, 0x81,       /* length */
            0x01,             /* client hello */
            0x00, 0x01, 0x7d, /* client hello length */
            0x03, 0x03        /* tlsv1.2 */
        },
    .session_id_length = 32,
    .data_02 =
        {
            0x00, 0x02, /* cipher suites length */
            0xc0, 0x2b, /* TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 */
            0x01,       /* compression methods length */
            0x00,       /* null */
            0x01, 0x32, /* extensions length */
            0x00, 0x0a, /* ext. supported_groups */
            0x00, 0x04, /* ext. length */
            0x00, 0x02, /* list length */
            0x00, 0x17, /* secp256r1 */
            0x00, 0x0d, /* ext. signature_algorithms */
            0x00, 0x04, /* ext. length */
            0x00, 0x02, /* list length */
            0x04, 0x03, /* ecdsa_secp256r1_sha256 */
            0x00, 0x10, /* ext. alpn */
            0x00, 0x0b, /* ext. length */
            0x00, 0x09, /* alpn length */
            0x08,       /* alpn string length */
            'h',  't',  't', 'p', '/', '1', '.', '1' /* alpn string */
        },
};

static struct payload_node *current_node;

static int make_http_get(uint8_t *buffer, size_t *len, char *hostname)
{
    int len_, buffsize;

    buffsize = *len;
    len_ = snprintf((char *) buffer, buffsize, http_fmt, hostname);

    if (len_ < 0) {
        E("ERROR: snprintf(): %s", "failure");
        return -1;
    } else if (len_ >= buffsize) {
        E("ERROR: hostname is too long");
        return -1;
    }

    *len = len_;

    return 0;
}


static int make_tls_client_hello(uint8_t *buffer, size_t *len, char *hostname)
{
    int padding_len;
    size_t i, buffsize;
    struct tls_client_hello *tls_data;
    struct tls_ext_server_name_head *server_name_head;
    struct tls_ext_padding_head *padding_head;

    buffsize = *len;

    if (buffsize < (int) sizeof(*tls_data)) {
        E("ERROR: buffer is too small");
        return -1;
    }

    tls_data = (struct tls_client_hello *) buffer;
    memcpy(tls_data, &cli_hello_tmpl, sizeof(cli_hello_tmpl));

    for (i = 0; i < sizeof(tls_data->random); i++) {
        tls_data->random[i] = rand();
    }

    for (i = 0; i < sizeof(tls_data->session_id); i++) {
        tls_data->session_id[i] = rand();
    }

    size_t hostname_len = strlen(hostname);

    padding_len = sizeof(tls_data->data_sni) -
                  sizeof(struct tls_ext_server_name_head) - strlen(hostname) -
                  sizeof(struct tls_ext_padding_head);

    if (padding_len < 0) {
        E("ERROR: hostname is too long");
        return -1;
    }

    server_name_head = (struct tls_ext_server_name_head *) tls_data->data_sni;
    SET_BE16(server_name_head->type, 0);
    SET_BE16(server_name_head->length, hostname_len + 5);
    SET_BE16(server_name_head->server_name_list_length, hostname_len + 3);
    SET_BE16(server_name_head->server_name_length, hostname_len);
    memcpy((uint8_t *) server_name_head + sizeof(*server_name_head), hostname,
           hostname_len);

    padding_head = (struct tls_ext_padding_head *) (tls_data->data_sni +
                                                    sizeof(*server_name_head) +
                                                    hostname_len);
    SET_BE16(padding_head->type, 21);
    SET_BE16(padding_head->length, padding_len);
    memset((uint8_t *) padding_head + sizeof(*padding_head), 0, padding_len);

    *len = sizeof(*tls_data);

    return 0;
}


static int make_custom(uint8_t *buffer, size_t *len, char *filepath)
{
    int res, len_, buffsize;
    FILE *fp;

    len_ = 0;
    buffsize = *len;

    fp = fopen(filepath, "rb");
    if (!fp) {
        E("ERROR: fopen(): %s: %s", filepath, strerror(errno));
        return -1;
    }

    while (!feof(fp) && !ferror(fp) && len_ < buffsize) {
        len_ += fread(buffer + len_, 1, buffsize - len_, fp);
    }

    if (ferror(fp)) {
        E("ERROR: fread(): %s: %s", filepath, "failure");
        fclose(fp);
        return -1;
    }

    if (!feof(fp)) {
        E("ERROR: %s: Data too long. Maximum length is %d", filepath,
          buffsize);
        fclose(fp);
        return -1;
    }

    res = fclose(fp);
    if (res < 0) {
        E("ERROR: fclose(): %s", strerror(errno));
        return -1;
    }

    *len = len_;

    return 0;
}


int fh_payload_setup(void)
{
    int res;
    size_t len;
    struct payload_info *pinfo;
    struct payload_node *node, *next;

    for (pinfo = g_ctx.plinfo; pinfo->type; pinfo++) {
        node = malloc(sizeof(*node));
        if (!node) {
            E("ERROR: malloc(): %s", strerror(errno));
            goto cleanup;
        }

        if (current_node) {
            next = current_node->next;
            current_node->next = node;
            node->next = next;
        } else {
            current_node = node;
            node->next = node;
        }

        switch (pinfo->type) {
            case FH_PAYLOAD_CUSTOM:
                len = sizeof(node->payload);
                res = make_custom(node->payload, &len, pinfo->info);
                if (res < 0) {
                    E(T(make_custom));
                    goto cleanup;
                }
                node->payload_len = len;
                break;

            case FH_PAYLOAD_HTTP:
                len = sizeof(node->payload);
                res = make_http_get(node->payload, &len, pinfo->info);
                if (res < 0) {
                    E(T(make_custom));
                    goto cleanup;
                }
                node->payload_len = len;
                break;

            case FH_PAYLOAD_HTTPS:
                len = sizeof(node->payload);
                res = make_tls_client_hello(node->payload, &len, pinfo->info);
                if (res < 0) {
                    E(T(make_custom));
                    goto cleanup;
                }
                node->payload_len = len;
                break;

            default:
                E("ERROR: Unknown payload type");
                goto cleanup;
        }
    }

    if (!current_node) {
        E("ERROR: No payload is available");
        goto cleanup;
    }

    current_node = current_node->next;

    return 0;

cleanup:
    fh_payload_cleanup();

    return -1;
}


void fh_payload_cleanup(void)
{
    struct payload_node *node, *next_node;

    node = current_node;
    while (node) {
        next_node = node->next;
        free(node);
        if (next_node == current_node) {
            break;
        }
        node = next_node;
    }
}


void th_payload_get(uint8_t **payload_ptr, size_t *payload_len)
{
    *payload_ptr = current_node->payload;
    *payload_len = current_node->payload_len;
    current_node = current_node->next;
}
