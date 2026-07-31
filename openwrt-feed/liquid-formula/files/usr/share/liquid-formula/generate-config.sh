#!/bin/sh
# Generate /etc/liquid-formula/config.yaml from UCI.

umask 077

FUNCTIONS_SH=${SBF_FUNCTIONS_SH:-/lib/functions.sh}
CONFIG=${SBF_CONFIG_NAME:-liquid_formula}
OUT=${SBF_CONFIG_OUT:-/etc/liquid-formula/config.yaml}
INCLUDE_DISABLED_URLS=${SBF_INCLUDE_DISABLED_URLS:-0}
TMP=

die() {
	printf 'generate-config: %s\n' "$*" >&2
	exit 1
}

case "$INCLUDE_DISABLED_URLS" in
	0|1) ;;
	*) die "SBF_INCLUDE_DISABLED_URLS must be 0 or 1" ;;
esac

# --------------------------------------------------------------- 串行化 ----
# config.yaml 有 7 个生成入口: Overview 保存、generate RPC、模板写入/删除、
# 两条模板回滚路径, 以及 update.sh 的 apply/generate。它们之间原本没有任何
# 共同的锁 —— 模板锁只保护模板 RPC, 动作锁只保护后台 refresh/check/update。
#
# 两个并发生成会各自读一份 UCI, 后写出的那个未必是读到新值的那个, 于是
# config.yaml 落后于 UCI, 而且要等下一次保存才自愈。
#
# 锁放在这里而不是逐个调用点: 只有一处要维护, 将来新增的入口自动受保护。
GEN_LOCK=${SBF_GEN_LOCK:-/var/lock/liquid-formula-generate.lock}
GEN_LOCK_WAIT=${SBF_GEN_LOCK_WAIT:-60}

if [ "${LFGEN_LOCKED:-0}" != 1 ]; then
	gen_flock=$(command -v flock 2>/dev/null) || gen_flock=''
	gen_flock_ok=0
	gen_probe=
	# busybox 的 flock applet 没有 -w/-E。先探测再用, 免得在缺少超时支持的
	# 环境里无限期阻塞。探针必须使用本次调用独占的 inode；/dev/null 是全局
	# 共享 inode，任何无关进程碰巧锁住它都会让能力探测误判并绕过真正的生成锁。
	if [ -n "$gen_flock" ]; then
		gen_probe=$(mktemp "${TMPDIR:-/tmp}/liquid-formula-flock.XXXXXX" 2>/dev/null) || gen_probe=
		if [ -n "$gen_probe" ] && "$gen_flock" -w 0 -E 99 "$gen_probe" true >/dev/null 2>&1; then
			gen_flock_ok=1
		fi
		[ -z "$gen_probe" ] || rm -f "$gen_probe"
	fi
	if [ "$gen_flock_ok" = 1 ]; then
		mkdir -p "$(dirname "$GEN_LOCK")" 2>/dev/null || true
		LFGEN_LOCKED=1
		export LFGEN_LOCKED
		"$gen_flock" -w "$GEN_LOCK_WAIT" -E 75 "$GEN_LOCK" "$0" "$@"
		gen_rc=$?
		[ "$gen_rc" != 75 ] || \
			printf 'generate-config: another generation is still running\n' >&2
		exit "$gen_rc"
	fi
	printf 'generate-config: flock unavailable; concurrent generations are not serialised\n' >&2
fi

[ -r "$FUNCTIONS_SH" ] || die "cannot read $FUNCTIONS_SH"
. "$FUNCTIONS_SH" || die "cannot load $FUNCTIONS_SH"

cleanup() {
	[ -n "$TMP" ] && rm -f "$TMP"
}

trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

valid_scalar() {
	! printf '%s' "$1" | LC_ALL=C grep -q '[[:cntrl:]]'
}

yaml_quote() {
	if [ -z "$1" ]; then
		printf "''"
	else
		printf '%s' "$1" | sed "s/'/''/g; s/^/'/; s/$/'/"
	fi
}

bool_yaml() {
	case "$1" in
		1|true|yes|on|enabled) printf 'true' ;;
		*) printf 'false' ;;
	esac
}

uint_between() {
	local value="$1" minimum="$2" maximum="$3"
	case "$value" in
		''|*[!0-9]*) return 1 ;;
		0|[1-9]*) ;;
		*) return 1 ;;
	esac
	[ "${#value}" -le "${#maximum}" ] || return 1
	[ "$value" -ge "$minimum" ] 2>/dev/null || return 1
	[ "$value" -le "$maximum" ] 2>/dev/null
}

INT32_MAX=2147483647
CHECKED_RESULT=

checked_add_int32() {
	local left="$1" right="$2" remaining
	uint_between "$left" 0 "$INT32_MAX" || return 1
	uint_between "$right" 0 "$INT32_MAX" || return 1
	remaining=$((INT32_MAX - right))
	[ "$left" -le "$remaining" ] || return 1
	CHECKED_RESULT=$((left + right))
}

checked_multiply_int32() {
	local left="$1" right="$2" quotient
	uint_between "$left" 0 "$INT32_MAX" || return 1
	uint_between "$right" 0 "$INT32_MAX" || return 1
	if [ "$left" -ne 0 ]; then
		quotient=$((INT32_MAX / left))
		[ "$right" -le "$quotient" ] || return 1
	fi
	CHECKED_RESULT=$((left * right))
}

valid_ipv6_literal() {
	printf '%s\n' "$1" | LC_ALL=C awk '
	function valid_decimal(part) {
		return part ~ /^[0-9]+$/ &&
			(length(part) == 1 || substr(part, 1, 1) != "0") &&
			part + 0 <= 255
	}
	function valid_ipv4(value, octets, count, idx) {
		count = split(value, octets, ".")
		if (count != 4)
			return 0
		for (idx = 1; idx <= count; idx++)
			if (!valid_decimal(octets[idx]))
				return 0
		return 1
	}
	function valid_ipv6(value, zone_at, zone, compression, left, right,
			groups, temporary, count, idx, part, units) {
		zone_at = index(value, "%25")
		if (zone_at > 0) {
			zone = substr(value, zone_at + 3)
			if (zone == "" || index(zone, "%") > 0)
				return 0
			value = substr(value, 1, zone_at - 1)
		}
		if (index(value, "%") > 0 || index(value, ":") == 0 ||
				index(value, ":::") > 0)
			return 0
		compression = index(value, "::")
		if (compression > 0) {
			left = substr(value, 1, compression - 1)
			right = substr(value, compression + 2)
			if (index(right, "::") > 0)
				return 0
		} else {
			left = value
			right = ""
		}
		count = 0
		if (left != "") {
			part = split(left, temporary, ":")
			for (idx = 1; idx <= part; idx++)
				groups[++count] = temporary[idx]
		}
		if (right != "") {
			part = split(right, temporary, ":")
			for (idx = 1; idx <= part; idx++)
				groups[++count] = temporary[idx]
		}
		units = 0
		for (idx = 1; idx <= count; idx++) {
			part = groups[idx]
			if (index(part, ".") > 0) {
				if (idx != count || !valid_ipv4(part))
					return 0
				units += 2
			} else {
				if (length(part) < 1 || length(part) > 4 ||
						part !~ /^[0-9A-Fa-f]+$/)
					return 0
				units++
			}
		}
		return compression > 0 ? units < 8 : units == 8
	}
	{ exit(valid_ipv6($0) ? 0 : 1) }
	'
}

valid_http_url() {
	local url="$1" rest authority userinfo hostport hostname suffix port escaped
	local before_query fragment percent_part host_escaped
	[ -n "$url" ] || return 1
	# Match net/url's raw-byte boundary: no ASCII space, C0 control, or DEL.
	! printf '%s' "$url" | LC_ALL=C grep -q '[[:cntrl:] ]' || return 1
	case "$url" in
		[Hh][Tt][Tt][Pp]://?*|[Hh][Tt][Tt][Pp][Ss]://?*) ;;
		*) return 1 ;;
	esac
	# net/url decodes authority/path/fragment and rejects malformed escapes
	# there; RawQuery remains opaque until a caller explicitly decodes it.
	before_query=${url%%\?*}
	fragment=
	case "$url" in *#*) fragment=${url#*#} ;; esac
	for percent_part in "$before_query" "$fragment"; do
		escaped=$percent_part
		while [ "${escaped#*%}" != "$escaped" ]; do
			escaped=${escaped#*%}
			case "$escaped" in
				[0-9A-Fa-f][0-9A-Fa-f]*) escaped=${escaped#??} ;;
				*) return 1 ;;
			esac
		done
	done
	rest=${url#*://}
	authority=${rest%%[/?#]*}
	[ -n "$authority" ] || return 1
	userinfo=
	case "$authority" in
		*@*)
			userinfo=${authority%@*}
			case "$userinfo" in
				*'"'*|*'<'*|*'>'*|*'\'*|*'^'*|*'`'*|*'{'*|*'|'*|*'}'*|*'['*|*']'*|*@*) return 1 ;;
			esac
			;;
	esac
	hostport=${authority##*@}
	[ -n "$hostport" ] || return 1
	case "$hostport" in
		\[* )
			case "$hostport" in *\]*) ;; *) return 1 ;; esac
			hostname=${hostport#\[}
			hostname=${hostname%%\]*}
			[ -n "$hostname" ] || return 1
			valid_ipv6_literal "$hostname" || return 1
			suffix=${hostport#*\]}
			case "$suffix" in
				'') ;;
				:*)
					port=${suffix#:}
					case "$port" in *[!0-9]*) return 1 ;; esac
					;;
				*) return 1 ;;
			esac
			;;
		*\]*) return 1 ;;
		*)
			case "$hostport" in
				*:*)
					hostname=${hostport%%:*}
					port=${hostport#*:}
					case "$port" in *[!0-9]*) return 1 ;; esac
					;;
				*) hostname=$hostport ;;
			esac
			[ -n "$hostname" ] || return 1
			case "$hostname" in *[\[\]]*) return 1 ;; esac
			;;
	esac
	case "$hostname" in
		*'"'*|*'<'*|*'>'*|*'\'*|*'^'*|*'`'*|*'{'*|*'|'*|*'}'*|*'['*|*']'*) return 1 ;;
	esac
	# net/url forbids percent-encoding ASCII bytes in a host, except %25
	# which introduces an RFC 6874 IPv6 zone identifier.
	host_escaped=$hostname
	while [ "${host_escaped#*%}" != "$host_escaped" ]; do
		host_escaped=${host_escaped#*%}
		case "$host_escaped" in
			25*) ;;
			[0-7][0-9A-Fa-f]*) return 1 ;;
		esac
		host_escaped=${host_escaped#??}
	done
	return 0
}

valid_template_base_url() {
	local without_scheme authority host port
	valid_scalar "$1" || return 1
	case "$1" in
		http://127.0.0.1|http://127.0.0.1/*|http://localhost|http://localhost/*) return 0 ;;
		http://127.0.0.1:*|http://localhost:*) ;;
		*) return 1 ;;
	esac
	without_scheme=${1#http://}
	authority=${without_scheme%%/*}
	host=${authority%%:*}
	port=${authority#*:}
	case "$host" in 127.0.0.1|localhost) ;; *) return 1 ;; esac
	[ "$authority" = "$host:$port" ] || return 1
	uint_between "$port" 1 65535 || return 1
	case "$without_scheme" in "$authority"|"$authority"/*) return 0 ;; *) return 1 ;; esac
}

valid_user_agent() {
	# 允许为空（表示交给转换器使用内置默认值）。
	# 非空时必须是可打印 ASCII，因为它会原样进入 HTTP 请求头。
	[ -z "$1" ] && return 0
	[ "${#1}" -le 200 ] || return 1
	case "$1" in
		*[!\ !-~]*) return 1 ;;
	esac
	return 0
}

valid_output_path() {
	valid_scalar "$1" || return 1
	case "$1" in
		*//*|*/../*|*/./*|*/..|*/.) return 1 ;;
	esac
	case "$1" in
		/etc/momo/profiles/*.json|/etc/sing-box/*.json|/var/lib/liquid-formula/output/*.json) return 0 ;;
		*) return 1 ;;
	esac
}

valid_template_id() {
	case "$1" in
		''|*[!A-Za-z0-9_]*) return 1 ;;
		*) return 0 ;;
	esac
}

valid_template_file() {
	local stem
	valid_scalar "$1" || return 1
	case "$1" in
		''|*[!A-Za-z0-9._-]*) return 1 ;;
		*.json)
			stem=${1%.json}
			[ -n "$stem" ]
			return
			;;
		*) return 1 ;;
	esac
}

VALIDATION_ERROR=
DEFAULT_FOUND=0
DEFAULT_ENABLED=0
TEMPLATE_COUNT=0
ENABLED_TEMPLATE_COUNT=0
SUBSCRIPTION_ERROR=
SUBSCRIPTION_URL_COUNT=0
GATEWAY_URLS_YAML=

collect_subscription_url() {
	local url="$1" url_yaml
	checked_add_int32 "$SUBSCRIPTION_URL_COUNT" 1 || {
		SUBSCRIPTION_ERROR='too many subscription URLs'
		return 1
	}
	[ "$CHECKED_RESULT" -le 8 ] || {
		SUBSCRIPTION_ERROR='at most 8 subscription URLs are allowed'
		return 1
	}
	[ -n "$url" ] && valid_http_url "$url" || {
		SUBSCRIPTION_ERROR='subscription_url list items must use HTTP or HTTPS'
		return 1
	}
	url_yaml=$(yaml_quote "$url") || {
		SUBSCRIPTION_ERROR='failed to quote a subscription URL'
		return 1
	}
	SUBSCRIPTION_URL_COUNT=$CHECKED_RESULT
	if [ -z "$GATEWAY_URLS_YAML" ]; then
		GATEWAY_URLS_YAML="    - $url_yaml"
	else
		GATEWAY_URLS_YAML="$GATEWAY_URLS_YAML
    - $url_yaml"
	fi
}

validate_template() {
	local sid="$1" enabled name file no_node
	checked_add_int32 "$TEMPLATE_COUNT" 1 || {
		VALIDATION_ERROR='too many templates'
		return 0
	}
	TEMPLATE_COUNT=$CHECKED_RESULT
	valid_template_id "$sid" || {
		VALIDATION_ERROR="invalid template id: $sid"
		return 0
	}
	config_get_bool enabled "$sid" enabled 1
	if [ "$enabled" = 1 ]; then
		checked_add_int32 "$ENABLED_TEMPLATE_COUNT" 1 || {
			VALIDATION_ERROR='too many enabled templates'
			return 0
		}
		ENABLED_TEMPLATE_COUNT=$CHECKED_RESULT
	fi
	config_get name "$sid" name "$sid"
	config_get file "$sid" file "$sid.json"
	config_get no_node "$sid" no_node '➜ Direct'
	valid_template_file "$file" || VALIDATION_ERROR="invalid template file for $sid"
	valid_scalar "$name" || VALIDATION_ERROR="invalid template name for $sid"
	valid_scalar "$no_node" || VALIDATION_ERROR="invalid no_node value for $sid"
	if [ "$sid" = "$default_template" ]; then
		DEFAULT_FOUND=1
		[ "$enabled" = 1 ] && DEFAULT_ENABLED=1
	fi
	return 0
}

emit_template() {
	local sid="$1" enabled name file no_node url url_yaml name_yaml no_node_yaml enabled_yaml status
	config_get_bool enabled "$sid" enabled 1
	config_get name "$sid" name "$sid"
	config_get file "$sid" file "$sid.json"
	config_get no_node "$sid" no_node '➜ Direct'
	url="${template_base_url%/}/$file"
	url_yaml=$(yaml_quote "$url") || {
		EMIT_ERROR="failed to quote template URL for $sid"
		return 1
	}
	name_yaml=$(yaml_quote "$name") || {
		EMIT_ERROR="failed to quote template name for $sid"
		return 1
	}
	no_node_yaml=$(yaml_quote "$no_node") || {
		EMIT_ERROR="failed to quote template no_node for $sid"
		return 1
	}
	enabled_yaml=$(bool_yaml "$enabled") || {
		EMIT_ERROR="failed to format template enabled value for $sid"
		return 1
	}
	cat <<EOF
  $sid:
    url: $url_yaml
    name: $name_yaml
    no_node: $no_node_yaml
    enabled: $enabled_yaml
EOF
	status=$?
	[ "$status" -eq 0 ] || EMIT_ERROR="failed to emit template $sid"
	return "$status"
}

emit_config() {
	cat <<EOF || return 1
# Generated by /usr/share/liquid-formula/generate-config.sh
server:
  port: $port
  read_timeout: 15
  write_timeout: $write_timeout
  idle_timeout: 60

auth:
  password: $password_yaml

subscription:
  url: $sub_url_yaml
  user_agent: $user_agent_yaml
  timeout: $aggregate_timeout
  refresh_interval: $refresh_interval

liquid_formula_gateway:
  listen_address: '127.0.0.1'
  listen_port: $gateway_port
  source_timeout: $sub_timeout
  aggregate_timeout: $aggregate_timeout
  user_agent: $user_agent_yaml
EOF
	if { [ "$enabled" != 1 ] && [ "$INCLUDE_DISABLED_URLS" != 1 ]; } ||
		[ "$SUBSCRIPTION_URL_COUNT" -eq 0 ]; then
		printf '%s\n' '  urls: []' || return 1
	else
		printf '%s\n%s\n' '  urls:' "$GATEWAY_URLS_YAML" || return 1
	fi
	cat <<EOF || return 1

templates:
EOF
	EMIT_ERROR=
	config_foreach emit_template template || return 1
	[ -z "$EMIT_ERROR" ] || return 1
	cat <<EOF || return 1

default_template: $default_template_yaml

cache:
  directory: $cache_dir_yaml
  node_file: 'node.json'
  template_file: 'template.json'

cloudflare:
  enabled: false
  purge_url: ''
  api_token: ''
  api_key: ''
  api_email: ''

logging:
  production: true
  file: $log_file_yaml
  level: 'info'
  max_size: 10
  max_backups: 3
  max_age: 7
EOF
}

config_load "$CONFIG" || die "failed to load UCI config $CONFIG"

config_get_bool enabled main enabled 0
config_get boot_delay main boot_delay '90'
config_get port main port '9716'
config_get password main password '890716'
config_get user_agent main user_agent ''
config_get sub_timeout main subscription_timeout '60'
config_get refresh_interval main refresh_interval '360'
config_get default_template main default_template 'momo_template'
config_get cache_dir main cache_dir '/var/lib/liquid-formula/cache'
config_get log_file main log_file '/var/log/liquid-formula/server.log'
config_get output_config main output_config '/etc/momo/profiles/config.json'
config_get template_base_url main template_base_url 'http://127.0.0.1/liquid-formula/templates'

uint_between "$port" 1 65535 || die "port must be an integer from 1 to 65535"
uint_between "$sub_timeout" 5 600 || die "subscription_timeout must be an integer from 5 to 600"
uint_between "$refresh_interval" 1 10080 || die "refresh_interval must be an integer from 1 to 10080"
uint_between "$boot_delay" 0 600 || die "boot_delay must be an integer from 0 to 600"

[ -n "$password" ] || die "password must not be empty"
valid_scalar "$password" || die "password contains a control character"
valid_user_agent "$user_agent" || die "user_agent must be printable ASCII and at most 200 characters"
valid_scalar "$default_template" || die "default_template contains a control character"
valid_template_id "$default_template" || die "default_template is not canonical"
valid_scalar "$cache_dir" || die "cache_dir contains a control character"
valid_scalar "$log_file" || die "log_file contains a control character"
case "$cache_dir" in /*) ;; *) die "cache_dir must be absolute" ;; esac
case "$log_file" in /*) ;; *) die "log_file must be absolute" ;; esac
# 日志由 root 服务追加并轮转。只校验"是绝对路径"的话, 一个手滑的值就能让
# 它去改写 /etc/config/* 这种文件。
case "$log_file" in
	*..*) die "log_file must not contain .." ;;
esac
case "$log_file" in
	/etc/*|/usr/*|/bin/*|/sbin/*|/lib/*|/proc/*|/sys/*|/dev/*|/www/*)
		die "log_file must not point inside a system directory" ;;
esac
case "$log_file" in
	*/) die "log_file must name a file, not a directory" ;;
esac
valid_template_base_url "$template_base_url" || die "template_base_url must use local HTTP loopback"
valid_output_path "$output_config" || die "output_config is outside the permitted JSON directories"

if ! config_list_foreach main subscription_url collect_subscription_url; then
	[ -z "$SUBSCRIPTION_ERROR" ] || die "$SUBSCRIPTION_ERROR"
	die "failed to inspect subscription_url list"
fi
[ -z "$SUBSCRIPTION_ERROR" ] || die "$SUBSCRIPTION_ERROR"
[ "$enabled" != 1 ] && [ "$INCLUDE_DISABLED_URLS" != 1 ] ||
	[ "$SUBSCRIPTION_URL_COUNT" -gt 0 ] || \
	die "at least one subscription_url list item is required while the service is enabled"

config_foreach validate_template template || die "failed to inspect templates"
[ -z "$VALIDATION_ERROR" ] || die "$VALIDATION_ERROR"
[ "$TEMPLATE_COUNT" -gt 0 ] || die "at least one template is required"
[ "$DEFAULT_FOUND" = 1 ] || die "default_template does not exist"
[ "$DEFAULT_ENABLED" = 1 ] || die "default_template is disabled"

# 网关串行拉取 S 个订阅源，每个源最多 T 秒，再留 60 秒完成转换器响应。
# 即使当前禁用且 S=0，转换器端仍保留一份 T 的基础预算。
source_budget_count=0
if [ "$enabled" = 1 ] || [ "$INCLUDE_DISABLED_URLS" = 1 ]; then
	source_budget_count=$SUBSCRIPTION_URL_COUNT
fi
[ "$source_budget_count" -gt 0 ] || source_budget_count=1
checked_multiply_int32 "$source_budget_count" "$sub_timeout" || \
	die "aggregate timeout exceeds signed 32-bit range"
checked_add_int32 "$CHECKED_RESULT" 60 || \
	die "aggregate timeout exceeds signed 32-bit range"
aggregate_timeout=$CHECKED_RESULT

# 整个 HTTP 请求还要串行处理 E 个启用模板，每个模板最多 T 秒，最后再留
# 60 秒写出完整响应。每一步都在执行 shell 算术之前验证 signed-int32 边界。
checked_multiply_int32 "$ENABLED_TEMPLATE_COUNT" "$sub_timeout" || \
	die "server write timeout exceeds signed 32-bit range"
template_budget=$CHECKED_RESULT
checked_add_int32 "$aggregate_timeout" "$template_budget" || \
	die "server write timeout exceeds signed 32-bit range"
checked_add_int32 "$CHECKED_RESULT" 60 || \
	die "server write timeout exceeds signed 32-bit range"
write_timeout=$CHECKED_RESULT

if [ "$port" -eq 65535 ]; then
	gateway_port=65534
else
	checked_add_int32 "$port" 1 || die "cannot derive gateway port"
	gateway_port=$CHECKED_RESULT
fi
aggregate_url="http://127.0.0.1:$gateway_port/v1/aggregate"

password_yaml=$(yaml_quote "$password") || die "failed to quote password"
sub_url_yaml=$(yaml_quote "$aggregate_url") || die "failed to quote aggregate URL"
user_agent_yaml=$(yaml_quote "$user_agent") || die "failed to quote subscription user agent"
default_template_yaml=$(yaml_quote "$default_template") || die "failed to quote default template"
cache_dir_yaml=$(yaml_quote "$cache_dir") || die "failed to quote cache directory"
log_file_yaml=$(yaml_quote "$log_file") || die "failed to quote log file"

OUT_DIR=$(dirname "$OUT") || die "cannot determine config directory"
OUT_BASE=${OUT##*/}
mkdir -p "$OUT_DIR" "$cache_dir" "$(dirname "$log_file")" || die "cannot create runtime directories"
TMP=$(mktemp "$OUT_DIR/.${OUT_BASE}.XXXXXX") || die "cannot create config staging file"

emit_config > "$TMP" || die "failed to write complete config staging file${EMIT_ERROR:+: $EMIT_ERROR}"

chmod 0600 "$TMP" || die "failed to secure config staging file"
if [ -f "$OUT" ]; then
	cmp -s "$TMP" "$OUT"
	cmp_status=$?
	case "$cmp_status" in
		0)
			rm -f "$TMP" || die "failed to remove unchanged staging file"
			TMP=
			chmod 0600 "$OUT" || die "failed to secure existing config"
			;;
		1)
			if [ -n "${SBF_TEST_FAULT_HOOK:-}" ]; then
				"$SBF_TEST_FAULT_HOOK" before_config_rename || \
					die "fault injected before config rename"
			fi
			mv "$TMP" "$OUT" || die "failed to atomically replace config"
			TMP=
			;;
		*) die "failed to compare config staging file with existing config" ;;
	esac
else
	if [ -n "${SBF_TEST_FAULT_HOOK:-}" ]; then
		"$SBF_TEST_FAULT_HOOK" before_config_rename || \
			die "fault injected before config rename"
	fi
	mv "$TMP" "$OUT" || die "failed to atomically replace config"
	TMP=
fi

printf '%s\n' "$OUT"
