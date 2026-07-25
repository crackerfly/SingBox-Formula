#!/bin/sh

set -eu

COMMON="${LFDPI_COMMON:-/usr/share/liquid-formula-dpi/service-common.sh}"
. "$COMMON"

[ "$#" -eq 3 ] || exit 1
service="$1"
token="$2"
deadline="$3"

tf_boot_service_valid "$service" || exit 1
tf_boot_token_valid "$token" || exit 1
tf_valid_uint_range "$deadline" 0 600 || exit 1

while tf_boot_state_is "$service" wait "$token" "$deadline"; do
	now="$(tf_boot_uptime)" || exit 1
	[ "$now" -lt "$deadline" ] || break
	sleep 1
done

tf_boot_state_is "$service" wait "$token" "$deadline" || exit 0
"${LFDPI_INIT_DIR:-/etc/init.d}/$service" boot_expired "$token" "$deadline"
