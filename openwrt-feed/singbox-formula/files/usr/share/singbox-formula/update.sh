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

CONFIG='singbox_formula'
GEN='/usr/share/singbox-formula/generate-config.sh'
LOG='/var/log/singbox-formula/update.log'
TMPDIR='/tmp/singbox-formula'
mkdir -p "$(dirname "$LOG")" "$TMPDIR"

log() {
	printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$LOG" >&2
}

urlencode() {
	# RFC 3986 percent-encoder, byte-accurate (UTF-8 safe).
	printf '%s' "$1" | LC_ALL=C awk '
	BEGIN{ ORS=""; for(i=0;i<256;i++) ord[sprintf("%c",i)]=i }
	{
		n=length($0)
		for(i=1;i<=n;i++){
			c=substr($0,i,1)
			if(c ~ /[A-Za-z0-9._~-]/) printf "%s", c
			else printf "%%%02X", ord[c]
		}
	}'
}

load_cfg() {
	config_load "$CONFIG"
	config_get port main port '9716'
	config_get password main password '890716'
	config_get sub_url main subscription_url ''
	config_get default_template main default_template 'momo_template'
	config_get output_config main output_config '/etc/momo/profiles/config.json'
	pass_q=$(urlencode "$password")
	template_q=$(urlencode "$default_template")
}

ensure_converter() {
	# Bring the converter up (if not already) and wait until its HTTP port
	# actually answers. Returns non-zero if it never becomes ready, so callers
	# can abort with a clear message instead of hitting a connection error.
	if curl -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
		return 0
	fi
	log "converter is not running/ready on port ${port}; starting it"
	/etc/init.d/singbox-formula start >/dev/null 2>&1 || true
	local i=0
	while [ "$i" -lt 20 ]; do
		if curl -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
		i=$((i + 1))
	done
	log "converter did not become ready on port ${port} within 20s"
	return 1
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
		ensure_converter || { log "cannot reach converter; enable/start it and check the subscription URL"; exit 1; }
		refresh_converter && log "refresh ok" || { log "refresh failed"; exit 1; }
		;;
	check)
		[ -n "$sub_url" ] || { log "subscription_url is empty"; exit 1; }
		"$GEN" >/dev/null || exit 1
		ensure_converter || { log "cannot reach converter; enable/start it and check the subscription URL"; exit 1; }
		refresh_converter || log "refresh failed; trying cached data"
		OUT="$TMPDIR/generated-check.json"
		fetch_config "$OUT" || { log "failed to fetch generated config"; exit 1; }
		validate_generated "$OUT" || exit 1
		log "check ok: $OUT"
		;;
	apply)
		[ -n "$sub_url" ] || { log "subscription_url is empty"; exit 1; }
		"$GEN" >/dev/null || exit 1
		ensure_converter || { log "cannot reach converter; enable/start it and check the subscription URL"; exit 1; }
		refresh_converter || log "refresh failed; trying cached data"
		OUT="$TMPDIR/generated.json"
		fetch_config "$OUT" || { log "failed to fetch generated config"; exit 1; }
		validate_generated "$OUT" || exit 1
		mkdir -p "$(dirname "$output_config")"
		if [ -s "$output_config" ]; then
			cp -f "$output_config" "$output_config.bak.$(date +%Y%m%d-%H%M%S)"
			# Keep only the 5 most recent backups so router flash does not fill up.
			ls -1t "$output_config".bak.* 2>/dev/null | tail -n +6 | while IFS= read -r old; do
				rm -f "$old"
			done
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
