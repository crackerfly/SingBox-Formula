#!/bin/sh
# Usage:
#   update.sh apply    # generate and install the converted sing-box JSON profile
#   update.sh check    # generate only, validate, leave temporary file in /tmp
#   update.sh refresh  # call converter refresh only
#   update.sh generate # generate converter config.yaml only
#
# This script intentionally does not start, stop, reload or restart sing-box.
# Runtime management is expected to be handled by OpenWrt Momo or another app.

. /lib/functions.sh

CONFIG='singbox_subscribe_convert'
GEN='/usr/share/singbox-subscribe-convert/generate-config.sh'
LOG='/var/log/singbox-subscribe-convert/update.log'
TMPDIR='/tmp/singbox-subscribe-convert'
mkdir -p "$(dirname "$LOG")" "$TMPDIR"

log() {
	printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$LOG" >&2
}

urlencode() {
	# Minimal percent encoder for ASCII URL query values.
	local s="$1" out="" i c hex
	i=1
	while [ $i -le ${#s} ]; do
		c=$(printf '%s' "$s" | cut -c "$i")
		case "$c" in
			[a-zA-Z0-9.~_-]) out="$out$c" ;;
			*) hex=$(printf '%02X' "'${c}"); out="$out%$hex" ;;
		esac
		i=$((i + 1))
	done
	printf '%s' "$out"
}

load_cfg() {
	config_load "$CONFIG"
	config_get port main port '9716'
	config_get password main password 'change_me'
	config_get sub_url main subscription_url ''
	config_get default_template main default_template 'openwrt'
	config_get output_config main output_config '/etc/momo/profiles/config.json'
	pass_q=$(urlencode "$password")
	template_q=$(urlencode "$default_template")
}

ensure_converter() {
	if ! /etc/init.d/singbox-subscribe-convert running >/dev/null 2>&1; then
		log "converter service is not running; starting it"
		/etc/init.d/singbox-subscribe-convert start >/dev/null 2>&1 || true
		sleep 3
	fi
}

validate_generated() {
	local file="$1"
	[ -s "$file" ] || { log "generated file is empty: $file"; return 1; }
	if command -v sing-box >/dev/null 2>&1; then
		sing-box check -c "$file" >>"$LOG" 2>&1 || { log "sing-box check failed"; return 1; }
	elif command -v jsonfilter >/dev/null 2>&1; then
		jsonfilter -i "$file" -e '@.outbounds' >/dev/null 2>&1 || { log "jsonfilter check failed"; return 1; }
	fi
	return 0
}

fetch_config() {
	local out="$1"
	curl -fsS --connect-timeout 10 --max-time 180 \
		"http://127.0.0.1:${port}/?password=${pass_q}&template=${template_q}" \
		-o "$out"
}

refresh_converter() {
	curl -fsS --connect-timeout 10 --max-time 180 \
		"http://127.0.0.1:${port}/refresh?password=${pass_q}" \
		-o "$TMPDIR/refresh.json"
}

cmd="${1:-apply}"
load_cfg
case "$cmd" in
	generate)
		"$GEN" >/dev/null || exit 1
		log "generated converter config"
		;;
	refresh)
		"$GEN" >/dev/null || exit 1
		ensure_converter
		refresh_converter && log "refresh ok" || { log "refresh failed"; exit 1; }
		;;
	check)
		[ -n "$sub_url" ] || { log "subscription_url is empty"; exit 1; }
		"$GEN" >/dev/null || exit 1
		ensure_converter
		refresh_converter || log "refresh failed; trying cached data"
		OUT="$TMPDIR/generated-check.json"
		fetch_config "$OUT" || { log "failed to fetch generated config"; exit 1; }
		validate_generated "$OUT" || exit 1
		log "check ok: $OUT"
		;;
	apply)
		[ -n "$sub_url" ] || { log "subscription_url is empty"; exit 1; }
		"$GEN" >/dev/null || exit 1
		ensure_converter
		refresh_converter || log "refresh failed; trying cached data"
		OUT="$TMPDIR/generated.json"
		fetch_config "$OUT" || { log "failed to fetch generated config"; exit 1; }
		validate_generated "$OUT" || exit 1
		mkdir -p "$(dirname "$output_config")"
		if [ -s "$output_config" ]; then
			cp -f "$output_config" "$output_config.bak.$(date +%Y%m%d-%H%M%S)"
		fi
		install -m 0600 "$OUT" "$output_config.new" || exit 1
		mv "$output_config.new" "$output_config" || exit 1
		log "installed generated config to $output_config"
		log "sing-box was not restarted; manage runtime from OpenWrt Momo or another app"
		;;
	*)
		echo "usage: $0 {apply|check|refresh|generate}" >&2
		exit 2
		;;
esac
