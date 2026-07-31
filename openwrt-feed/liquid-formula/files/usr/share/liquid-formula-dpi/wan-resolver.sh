#!/bin/sh

# Resolve OpenWrt's current default WAN networks to their real L3 devices.
# This helper deliberately uses only /lib/functions/network.sh. It must not
# guess from hotplug's DEVICE, saved UCI device lists, or /proc/net/route.

tf_wan_load_network_helpers() {
	local helpers="${LFDPI_NETWORK_HELPERS:-/lib/functions/network.sh}"

	if command -v network_flush_cache >/dev/null 2>&1 &&
	   command -v network_find_wan >/dev/null 2>&1 &&
	   command -v network_find_wan6 >/dev/null 2>&1 &&
	   command -v network_get_device >/dev/null 2>&1; then
		return 0
	fi
	[ -r "$helpers" ] || return 1
	# shellcheck source=/dev/null
	. "$helpers"
	command -v network_flush_cache >/dev/null 2>&1 &&
		command -v network_find_wan >/dev/null 2>&1 &&
		command -v network_find_wan6 >/dev/null 2>&1 &&
		command -v network_get_device >/dev/null 2>&1
}

tf_wan_resolve_ipv4() {
	local network='' l3_device=''

	network_find_wan network 2>/dev/null || return 1
	tf_valid_network_name "$network" || return 1
	network_get_device l3_device "$network" 2>/dev/null || return 1
	tf_interface_exists "$l3_device" || return 1
	TF_WAN_IPV4_NETWORK="$network"
	TF_WAN_IPV4_DEVICE="$l3_device"
	return 0
}

tf_wan_resolve_ipv6() {
	local network='' l3_device=''

	network_find_wan6 network 2>/dev/null || return 1
	tf_valid_network_name "$network" || return 1
	network_get_device l3_device "$network" 2>/dev/null || return 1
	tf_interface_exists "$l3_device" || return 1
	TF_WAN_IPV6_NETWORK="$network"
	TF_WAN_IPV6_DEVICE="$l3_device"
	return 0
}

tf_wan_resolve() {
	local family="${1:-}" ipv4_ok=1 ipv6_ok=1

	TF_WAN_IPV4_NETWORK=''
	TF_WAN_IPV4_DEVICE=''
	TF_WAN_IPV6_NETWORK=''
	TF_WAN_IPV6_DEVICE=''
	TF_WAN_DEVICES=''

	case "$family" in
		ipv4|ipv6|dual) ;;
		*) return 1 ;;
	esac
	tf_wan_load_network_helpers || return 1
	network_flush_cache 2>/dev/null || true

	case "$family" in
		ipv4)
			tf_wan_resolve_ipv4 || ipv4_ok=0
			;;
		ipv6)
			tf_wan_resolve_ipv6 || ipv6_ok=0
			;;
		dual)
			tf_wan_resolve_ipv4 || ipv4_ok=0
			tf_wan_resolve_ipv6 || ipv6_ok=0
			;;
	esac

	if [ -n "$TF_WAN_IPV4_DEVICE" ]; then
		TF_WAN_DEVICES="$TF_WAN_IPV4_DEVICE"
	fi
	if [ -n "$TF_WAN_IPV6_DEVICE" ]; then
		case " $TF_WAN_DEVICES " in
			*" $TF_WAN_IPV6_DEVICE "*) ;;
			*) TF_WAN_DEVICES="${TF_WAN_DEVICES:+$TF_WAN_DEVICES }$TF_WAN_IPV6_DEVICE" ;;
		esac
	fi

	case "$family" in
		ipv4) [ "$ipv4_ok" = 1 ] ;;
		ipv6) [ "$ipv6_ok" = 1 ] ;;
		dual) [ "$ipv4_ok" = 1 ] || [ "$ipv6_ok" = 1 ] ;;
	esac
}

tf_wan_state_path() {
	tf_boot_service_valid "$1" || return 1
	printf '%s/%s.wan\n' "$LFDPI_BOOT_STATE_DIR" "$1"
}

tf_wan_state_set() {
	local service="$1" n4="${TF_WAN_IPV4_NETWORK:-}" d4="${TF_WAN_IPV4_DEVICE:-}"
	local n6="${TF_WAN_IPV6_NETWORK:-}" d6="${TF_WAN_IPV6_DEVICE:-}" path tmp

	tf_boot_service_valid "$service" || return 1
	[ "$LFDPI_LIFECYCLE_LOCKED" = "$service" ] || return 1
	if [ -n "$n4" ] || [ -n "$d4" ]; then
		tf_valid_network_name "$n4" && tf_valid_interface "$d4" || return 1
	else
		n4='-'
		d4='-'
	fi
	if [ -n "$n6" ] || [ -n "$d6" ]; then
		tf_valid_network_name "$n6" && tf_valid_interface "$d6" || return 1
	else
		n6='-'
		d6='-'
	fi

	tf_boot_prepare_state_dir || return 1
	path="$(tf_wan_state_path "$service")" || return 1
	tmp="$LFDPI_BOOT_STATE_DIR/.${service}.wan.$$"
	umask 077
	printf 'v1 %s %s %s %s\n' "$n4" "$d4" "$n6" "$d6" >"$tmp" || return 1
	chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
	mv -f "$tmp" "$path" || { rm -f "$tmp"; return 1; }
}

tf_wan_state_get() {
	local service="$1" path version n4 d4 n6 d6 extra lines

	TF_WAN_STORED_IPV4_NETWORK=''
	TF_WAN_STORED_IPV4_DEVICE=''
	TF_WAN_STORED_IPV6_NETWORK=''
	TF_WAN_STORED_IPV6_DEVICE=''

	path="$(tf_wan_state_path "$service")" || return 1
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	lines="$(wc -l <"$path" 2>/dev/null)" || return 1
	[ "$lines" -eq 1 ] 2>/dev/null || return 1
	IFS=' ' read -r version n4 d4 n6 d6 extra <"$path" || return 1
	[ "$version" = v1 ] && [ -z "$extra" ] || return 1

	if [ "$n4" != '-' ] || [ "$d4" != '-' ]; then
		tf_valid_network_name "$n4" && tf_valid_interface "$d4" || return 1
		TF_WAN_STORED_IPV4_NETWORK="$n4"
		TF_WAN_STORED_IPV4_DEVICE="$d4"
	fi
	if [ "$n6" != '-' ] || [ "$d6" != '-' ]; then
		tf_valid_network_name "$n6" && tf_valid_interface "$d6" || return 1
		TF_WAN_STORED_IPV6_NETWORK="$n6"
		TF_WAN_STORED_IPV6_DEVICE="$d6"
	fi
	return 0
}

tf_wan_state_clear() {
	local service="$1" path

	tf_boot_service_valid "$service" || return 1
	[ "$LFDPI_LIFECYCLE_LOCKED" = "$service" ] || return 1
	path="$(tf_wan_state_path "$service")" || return 1
	[ ! -L "$LFDPI_BOOT_STATE_DIR" ] || return 1
	rm -f "$path"
}
