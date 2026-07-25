#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
COMMON="$ROOT/openwrt-feed/liquid-formula-dpi/files/usr/share/liquid-formula-dpi/service-common.sh"

[ -f "$COMMON" ] || {
	echo "missing service-common.sh" >&2
	exit 1
}

# shellcheck source=/dev/null
. "$COMMON"

expect_ok() {
	"$@" || {
		echo "expected success: $*" >&2
		exit 1
	}
}

expect_fail() {
	if "$@"; then
		echo "expected failure: $*" >&2
		exit 1
	fi
}

expect_ok tf_valid_interface eth0
expect_ok tf_valid_interface pppoe-wan
expect_ok tf_valid_interface br-lan.10
expect_fail tf_valid_interface ''
expect_fail tf_valid_interface lo
expect_fail tf_valid_interface 'wan";drop'
expect_fail tf_valid_interface 'interface-name-is-too-long'

expect_ok tf_valid_hostname www.speedtest.net
expect_ok tf_valid_hostname xn--fiqs8s.example
expect_fail tf_valid_hostname 'https://example.com'
expect_fail tf_valid_hostname '.example.com'
expect_fail tf_valid_hostname 'example..com'
expect_fail tf_valid_hostname "$(printf 'a%.0s' $(seq 1 64)).example"

expect_ok tf_valid_port_spec 53
expect_ok tf_valid_port_spec 443,51820,6000-7000
expect_fail tf_valid_port_spec 0
expect_fail tf_valid_port_spec 65536
expect_fail tf_valid_port_spec 7000-6000
expect_fail tf_valid_port_spec '53, 443'

expect_ok tf_valid_uint_range 1 1 10
expect_ok tf_valid_uint_range 10 1 10
expect_fail tf_valid_uint_range 0 1 10
expect_fail tf_valid_uint_range 11 1 10
expect_fail tf_valid_uint_range 1x 1 10

[ "$(tf_boot_delay_value 0 40)" = 0 ]
[ "$(tf_boot_delay_value 600 40)" = 600 ]
[ "$(tf_boot_delay_value 601 40)" = 40 ]
[ "$(tf_boot_delay_value 00 40)" = 40 ]

expect_ok tf_valid_u32 0x8000
expect_ok tf_valid_u32 4294967295
expect_fail tf_valid_u32 0
expect_fail tf_valid_u32 0x0
expect_fail tf_valid_u32 4294967296
expect_fail tf_valid_u32 0x100000000

expect_ok tf_mark_fits_mask 0x8000 0x8000
expect_ok tf_mark_fits_mask 0x8000 0xffff
expect_fail tf_mark_fits_mask 0x8000 0x4000

expect_ok tf_valid_sip_uri 'sip:user@example.com'
expect_ok tf_valid_sip_uri 'sip:user@203.0.113.10'
expect_fail tf_valid_sip_uri 'user@example.com'
expect_fail tf_valid_sip_uri "sip:user@example.com
Injected: yes"

expect_ok tf_valid_fakehttp_payload_location \
	/etc/liquid-formula/fakehttp-payloads/safe.bin
expect_fail tf_valid_fakehttp_payload_location \
	/etc/liquid-formula/fakehttp-payloads/../secret.bin
expect_fail tf_valid_fakehttp_payload_location \
	/etc/liquid-formula/fakehttp-payloads/nested/payload.bin
expect_fail tf_valid_fakehttp_payload_location \
	/etc/liquid-formula/fakehttp-payloads/.hidden.bin

echo "service-common tests: ok"
