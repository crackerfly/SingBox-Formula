#!/bin/sh

# Shared validation and cleanup helpers for the FakeHTTP and FakeSIP
# procd services. Keep this file compatible with BusyBox ash.

tf_valid_interface() {
	local value="$1"

	[ -n "$value" ] && [ "${#value}" -le 15 ] || return 1
	[ "$value" != 'lo' ] || return 1
	case "$value" in
		*[!A-Za-z0-9_.-]*) return 1 ;;
	esac
	return 0
}

tf_interface_exists() {
	local sys_class_net="${LFDPI_SYS_CLASS_NET:-/sys/class/net}"

	tf_valid_interface "$1" || return 1
	[ -e "$sys_class_net/$1" ] || [ -L "$sys_class_net/$1" ]
}

tf_valid_hostname() {
	local hostname="$1" old_ifs label

	[ -n "$hostname" ] && [ "${#hostname}" -le 253 ] || return 1
	case "$hostname" in
		.*|*.|*..*|*[!A-Za-z0-9.-]*) return 1 ;;
	esac

	old_ifs="$IFS"
	IFS='.'
	set -- $hostname
	IFS="$old_ifs"

	for label in "$@"; do
		[ -n "$label" ] && [ "${#label}" -le 63 ] || return 1
		case "$label" in
			-*|*-) return 1 ;;
		esac
	done
	return 0
}

tf_valid_uint_range() {
	local value="$1" minimum="$2" maximum="$3" normalized

	case "$value" in
		''|*[!0-9]*|0?*) return 1 ;;
	esac

	# Avoid implementation-defined overflow in the shell arithmetic parser.
	[ "${#value}" -le 10 ] || return 1
	normalized="$value"
	while [ "${#normalized}" -gt 1 ] && [ "${normalized#0}" != "$normalized" ]; do
		normalized="${normalized#0}"
	done

	[ "$normalized" -ge "$minimum" ] 2>/dev/null || return 1
	[ "$normalized" -le "$maximum" ] 2>/dev/null || return 1
	return 0
}

tf_valid_port_spec() {
	local spec="$1" old_ifs item first last

	[ -n "$spec" ] || return 1
	case "$spec" in
		*[!0-9,-]*|,*|*,|*,,*) return 1 ;;
	esac

	old_ifs="$IFS"
	IFS=','
	set -- $spec
	IFS="$old_ifs"

	for item in "$@"; do
		case "$item" in
			*-*)
				first="${item%%-*}"
				last="${item#*-}"
				[ "$last" = "${last#*-}" ] || return 1
				tf_valid_uint_range "$first" 1 65535 || return 1
				tf_valid_uint_range "$last" 1 65535 || return 1
				[ "$first" -le "$last" ] 2>/dev/null || return 1
				;;
			*)
				tf_valid_uint_range "$item" 1 65535 || return 1
				;;
		esac
	done
	return 0
}

tf_valid_u32() {
	local value="$1" digits normalized numeric

	case "$value" in
		0[xX]*)
			digits="${value#??}"
			[ -n "$digits" ] && [ "${#digits}" -le 8 ] || return 1
			case "$digits" in *[!0-9A-Fa-f]*) return 1 ;; esac
			numeric=$((value))
			;;
		*)
			case "$value" in ''|*[!0-9]*|0?*) return 1 ;; esac
			[ "${#value}" -le 10 ] || return 1
			normalized="$value"
			while [ "${#normalized}" -gt 1 ] && [ "${normalized#0}" != "$normalized" ]; do
				normalized="${normalized#0}"
			done
			[ "$normalized" -le 4294967295 ] 2>/dev/null || return 1
			numeric="$normalized"
			;;
	esac

	[ "$numeric" -gt 0 ] 2>/dev/null && [ "$numeric" -le 4294967295 ] 2>/dev/null
}

tf_mark_fits_mask() {
	local mark="$1" mask="$2" mark_num mask_num

	tf_valid_u32 "$mark" || return 1
	tf_valid_u32 "$mask" || return 1
	mark_num=$((mark))
	mask_num=$((mask))
	[ $((mark_num & mask_num)) -eq "$mark_num" ]
}

tf_valid_sip_uri() {
	local uri="$1" body user authority host port

	[ -n "$uri" ] && [ "${#uri}" -le 255 ] || return 1
	case "$uri" in
		sip:*) ;;
		*) return 1 ;;
	esac
	case "$uri" in
		*[!A-Za-z0-9_.!~*+\&=%@:-]*) return 1 ;;
	esac

	body="${uri#sip:}"
	user="${body%%@*}"
	authority="${body#*@}"
	[ "$body" != "$authority" ] && [ -n "$user" ] && [ -n "$authority" ] || return 1
	[ "$authority" = "${authority#*@}" ] || return 1

	case "$user" in *[!A-Za-z0-9_.!~*+\&=%-]*) return 1 ;; esac
	case "$authority" in
		*:*)
			host="${authority%%:*}"
			port="${authority#*:}"
			[ "$port" = "${port#*:}" ] || return 1
			tf_valid_uint_range "$port" 1 65535 || return 1
			;;
		*) host="$authority" ;;
	esac

	tf_valid_hostname "$host"
}

tf_valid_fakehttp_payload_location() {
	local path="$1" name

	case "$path" in
		/etc/liquid-formula/fakehttp-payloads/*.bin) ;;
		*) return 1 ;;
	esac
	name="${path##*/}"
	[ "$path" = "/etc/liquid-formula/fakehttp-payloads/$name" ] || return 1
	[ -n "$name" ] && [ "${#name}" -le 96 ] || return 1
	case "$name" in .*|*[!A-Za-z0-9._-]*) return 1 ;; esac
	case "$name" in [A-Za-z0-9]*.bin) ;; *) return 1 ;; esac
	return 0
}

tf_valid_fakehttp_payload_path() {
	local path="$1" size

	tf_valid_fakehttp_payload_location "$path" || return 1
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	size="$(wc -c < "$path" 2>/dev/null)" || return 1
	tf_valid_uint_range "$size" 1 1200
}

# FakeHTTP / FakeSIP 在 nft 不可用时会自动回退到 iptables, 在 mangle 表里建
# <NAME>_R / _S / _D 三条链, 并从 PREROUTING / POSTROUTING 跳进去。进程被
# SIGKILL 或崩溃时它自己的拆除逻辑跑不到, 而原先这里只删 nft 表, 那些 mangle
# 钩子就会永久留在系统里, 下次启动还可能因为链已存在而失败。
#
# 拆除顺序照搬 C 代码: 先 flush, 再摘掉 PREROUTING/POSTROUTING 的跳转, 最后
# 删链 —— 链上还有引用时 -X 会失败。
tf_cleanup_iptables_chains() {
	local name="$1" upper bin

	case "$name" in
		fakehttp) upper=FAKEHTTP ;;
		fakesip) upper=FAKESIP ;;
		*) return 1 ;;
	esac

	for bin in iptables ip6tables; do
		command -v "$bin" >/dev/null 2>&1 || continue

		# 链不存在时直接跳过, 免得每次停服务都刷一堆无谓的错误。
		"$bin" -w -t mangle -n -L "${upper}_S" >/dev/null 2>&1 || continue

		"$bin" -w -t mangle -F "${upper}_R" >/dev/null 2>&1 || true
		"$bin" -w -t mangle -F "${upper}_S" >/dev/null 2>&1 || true
		"$bin" -w -t mangle -F "${upper}_D" >/dev/null 2>&1 || true

		# 跳转规则可能被插了不止一条(反复异常退出), 循环删干净。
		while "$bin" -w -t mangle -D PREROUTING -j "${upper}_S" >/dev/null 2>&1; do :; done
		while "$bin" -w -t mangle -D POSTROUTING -j "${upper}_D" >/dev/null 2>&1; do :; done

		"$bin" -w -t mangle -X "${upper}_R" >/dev/null 2>&1 || true
		"$bin" -w -t mangle -X "${upper}_S" >/dev/null 2>&1 || true
		"$bin" -w -t mangle -X "${upper}_D" >/dev/null 2>&1 || true
	done
	return 0
}

tf_cleanup_firewall_state() {
	tf_cleanup_nft_table "$1" || return 1
	tf_cleanup_iptables_chains "$1" || return 1
	return 0
}

tf_cleanup_nft_table() {
	local table="$1"

	case "$table" in
		fakehttp|fakesip) ;;
		*) return 1 ;;
	esac
	command -v nft >/dev/null 2>&1 || return 0
	nft delete table ip "$table" >/dev/null 2>&1 || true
	nft delete table ip6 "$table" >/dev/null 2>&1 || true
}

LFDPI_BOOT_STATE_DIR="${LFDPI_BOOT_STATE_DIR:-/var/run/liquid-formula-boot-delay}"
LFDPI_UPTIME_FILE="${LFDPI_UPTIME_FILE:-/proc/uptime}"
LFDPI_LIFECYCLE_LOCKED=''
LFDPI_LIFECYCLE_LOCK_DEPTH=0

tf_boot_delay_value() {
	local value="$1" fallback="$2"

	tf_valid_uint_range "$fallback" 0 600 || return 1
	tf_valid_uint_range "$value" 0 600 || value="$fallback"
	printf '%s\n' "$value"
}

tf_boot_uptime() {
	local value rest

	IFS=' ' read -r value rest <"$LFDPI_UPTIME_FILE" || return 1
	value="${value%%.*}"
	tf_valid_uint_range "$value" 0 4294967295 || return 1
	printf '%s\n' "$value"
}

tf_boot_prepare_state_dir() {
	local owner

	umask 077
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	if [ -e "$LFDPI_BOOT_STATE_DIR" ]; then
		[ -d "$LFDPI_BOOT_STATE_DIR" ] || return 1
	else
		mkdir -p "$LFDPI_BOOT_STATE_DIR" || return 1
	fi
	[ -d "$LFDPI_BOOT_STATE_DIR" ] && [ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	owner="$(stat -c '%u' "$LFDPI_BOOT_STATE_DIR" 2>/dev/null)" || return 1
	[ "$owner" = 0 ] || return 1
	chmod 0700 "$LFDPI_BOOT_STATE_DIR" || return 1
	[ -d "$LFDPI_BOOT_STATE_DIR" ] && [ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	owner="$(stat -c '%u' "$LFDPI_BOOT_STATE_DIR" 2>/dev/null)" || return 1
	[ "$owner" = 0 ]
}

tf_boot_service_valid() {
	case "$1" in
		fakehttp|fakesip) return 0 ;;
		*) return 1 ;;
	esac
}

tf_boot_token_valid() {
	case "$1" in
		''|*[!A-Za-z0-9_.-]*) return 1 ;;
		*) return 0 ;;
	esac
}

tf_boot_state_path() {
	tf_boot_service_valid "$1" || return 1
	printf '%s/%s.state\n' "$LFDPI_BOOT_STATE_DIR" "$1"
}

tf_boot_state_set() {
	local service="$1" phase="$2" token="$3" deadline="$4" path tmp

	tf_boot_service_valid "$service" || return 1
	case "$phase" in wait|manual|done|cancel) ;; *) return 1 ;; esac
	tf_boot_token_valid "$token" || return 1
	tf_valid_uint_range "$deadline" 0 600 || return 1
	tf_boot_prepare_state_dir || return 1
	path="$(tf_boot_state_path "$service")" || return 1
	tmp="$LFDPI_BOOT_STATE_DIR/.${service}.state.$$"
	umask 077
	printf '%s %s %s\n' "$phase" "$token" "$deadline" >"$tmp" || return 1
	chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
	mv -f "$tmp" "$path" || { rm -f "$tmp"; return 1; }
}

tf_boot_state_get() {
	local service="$1" path phase token deadline extra lines

	path="$(tf_boot_state_path "$service")" || return 1
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	lines="$(wc -l <"$path" 2>/dev/null)" || return 1
	[ "$lines" -eq 1 ] 2>/dev/null || return 1
	IFS=' ' read -r phase token deadline extra <"$path" || return 1
	[ -z "$extra" ] || return 1
	case "$phase" in wait|manual|done|cancel) ;; *) return 1 ;; esac
	tf_boot_token_valid "$token" || return 1
	tf_valid_uint_range "$deadline" 0 600 || return 1
	printf '%s %s %s\n' "$phase" "$token" "$deadline"
}

tf_boot_state_is() {
	local actual

	actual="$(tf_boot_state_get "$1" 2>/dev/null)" || return 1
	[ "$actual" = "$2 $3 $4" ]
}

tf_boot_state_clear() {
	local path

	path="$(tf_boot_state_path "$1")" || return 1
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	rm -f "$path"
}

tf_boot_remaining() {
	local delay="$1" now

	tf_valid_uint_range "$delay" 0 600 || return 1
	now="$(tf_boot_uptime)" || return 1
	if [ "$now" -ge "$delay" ]; then
		printf '0\n'
	else
		printf '%s\n' $((delay - now))
	fi
}

tf_boot_should_defer() {
	local service="$1" delay="$2" state phase token deadline now

	tf_boot_service_valid "$service" || return 1
	tf_valid_uint_range "$delay" 0 600 || return 1
	state="$(tf_boot_state_get "$service" 2>/dev/null || true)"
	if [ -n "$state" ]; then
		set -- $state
		phase="$1"
		case "$phase" in
			wait)
				now="$(tf_boot_uptime)" || return 0
				[ "$now" -lt "$3" ]
				return
				;;
			cancel) return 0 ;;
			manual|done) return 1 ;;
		esac
	fi
	now="$(tf_boot_uptime)" || return 0
	[ "$now" -lt "$delay" ]
}

tf_lifecycle_lock_acquire() {
	local service="$1" path

	tf_boot_service_valid "$service" || return 1
	if [ -n "$LFDPI_LIFECYCLE_LOCKED" ]; then
		[ "$LFDPI_LIFECYCLE_LOCKED" = "$service" ] || return 1
		LFDPI_LIFECYCLE_LOCK_DEPTH=$((LFDPI_LIFECYCLE_LOCK_DEPTH + 1))
		return 0
	fi
	tf_boot_prepare_state_dir || return 1
	command -v flock >/dev/null 2>&1 || return 1
	path="$LFDPI_BOOT_STATE_DIR/$service.lock"
	[ ! -L "$path" ] || return 1
	: >>"$path" || return 1
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	chmod 0600 "$path" || return 1
	exec 9>"$path" || return 1
	flock -x 9 || { exec 9>&-; return 1; }
	LFDPI_LIFECYCLE_LOCKED="$service"
	LFDPI_LIFECYCLE_LOCK_DEPTH=1
}

tf_lifecycle_lock_release() {
	local service="$1"

	[ "$LFDPI_LIFECYCLE_LOCKED" = "$service" ] || return 1
	[ "$LFDPI_LIFECYCLE_LOCK_DEPTH" -ge 1 ] 2>/dev/null || return 1
	LFDPI_LIFECYCLE_LOCK_DEPTH=$((LFDPI_LIFECYCLE_LOCK_DEPTH - 1))
	[ "$LFDPI_LIFECYCLE_LOCK_DEPTH" -eq 0 ] || return 0
	flock -u 9 || return 1
	exec 9>&-
	LFDPI_LIFECYCLE_LOCKED=''
}

tf_link_state_path() {
	tf_boot_service_valid "$1" || return 1
	printf '%s/%s.link\n' "$LFDPI_BOOT_STATE_DIR" "$1"
}

tf_link_state_get() {
	local service="$1" path phase generation extra lines

	path="$(tf_link_state_path "$service")" || return 1
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	lines="$(wc -l <"$path" 2>/dev/null)" || return 1
	[ "$lines" -eq 1 ] 2>/dev/null || return 1
	IFS=' ' read -r phase generation extra <"$path" || return 1
	[ -z "$extra" ] || return 1
	case "$phase" in unknown|up|down) ;; *) return 1 ;; esac
	tf_valid_uint_range "$generation" 0 4294967295 || return 1
	printf '%s %s\n' "$phase" "$generation"
}

tf_link_state_set() {
	local service="$1" phase="$2" state generation path tmp

	tf_boot_service_valid "$service" || return 1
	[ "$LFDPI_LIFECYCLE_LOCKED" = "$service" ] || return 1
	case "$phase" in unknown|up|down) ;; *) return 1 ;; esac
	state="$(tf_link_state_get "$service" 2>/dev/null || true)"
	if [ -n "$state" ]; then
		set -- $state
		generation="$2"
	else
		generation=0
	fi
	[ "$generation" -lt 4294967295 ] || return 1
	generation=$((generation + 1))
	tf_boot_prepare_state_dir || return 1
	path="$(tf_link_state_path "$service")" || return 1
	tmp="$LFDPI_BOOT_STATE_DIR/.${service}.link.$$"
	umask 077
	printf '%s %s\n' "$phase" "$generation" >"$tmp" || return 1
	chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
	mv -f "$tmp" "$path" || { rm -f "$tmp"; return 1; }
}

tf_link_state_is_down() {
	local state

	state="$(tf_link_state_get "$1" 2>/dev/null)" || return 1
	case "$state" in down\ *) return 0 ;; *) return 1 ;; esac
}

tf_valid_network_name() {
	local value="$1"

	[ -n "$value" ] && [ "${#value}" -le 64 ] || return 1
	case "$value" in *[!A-Za-z0-9_.-]*) return 1 ;; esac
}

tf_hotplug_device_cache_path() {
	tf_valid_network_name "$1" || return 1
	printf '%s/interface-%s.device\n' "$LFDPI_BOOT_STATE_DIR" "$1"
}

tf_hotplug_device_cache_set() {
	local interface="$1" device="$2" path tmp

	tf_valid_network_name "$interface" || return 1
	tf_valid_interface "$device" || return 1
	tf_boot_prepare_state_dir || return 1
	path="$(tf_hotplug_device_cache_path "$interface")" || return 1
	tmp="$LFDPI_BOOT_STATE_DIR/.interface-${interface}.device.$$"
	umask 077
	printf '%s %s\n' "$interface" "$device" >"$tmp" || return 1
	chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
	mv -f "$tmp" "$path" || { rm -f "$tmp"; return 1; }
}

tf_hotplug_device_cache_get() {
	local interface="$1" path cached_interface device extra lines

	path="$(tf_hotplug_device_cache_path "$interface")" || return 1
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	lines="$(wc -l <"$path" 2>/dev/null)" || return 1
	[ "$lines" -eq 1 ] 2>/dev/null || return 1
	IFS=' ' read -r cached_interface device extra <"$path" || return 1
	[ -z "$extra" ] && [ "$cached_interface" = "$interface" ] || return 1
	tf_valid_interface "$device" || return 1
	printf '%s\n' "$device"
}
