#!/bin/sh
# Usage:
#   update.sh apply    # generate, validate and atomically install a sing-box profile
#   update.sh check    # generate and validate without installing
#   update.sh refresh  # call converter refresh without regenerating config.yaml
#   update.sh generate # generate converter config.yaml only

umask 077

FUNCTIONS_SH=${SBF_FUNCTIONS_SH:-/lib/functions.sh}
CONFIG=${SBF_CONFIG_NAME:-singbox_formula}
GEN=${SBF_GENERATOR:-/usr/share/singbox-formula/generate-config.sh}
INIT=${SBF_INIT_SCRIPT:-/etc/init.d/singbox-formula}
LOG=${SBF_LOG_FILE:-/var/log/singbox-formula/update.log}
LOCK_DIR=${SBF_LOCK_DIR:-/var/run/singbox-formula/update.lock}
TMP_ROOT=${SBF_TMP_ROOT:-${TMPDIR:-/tmp}}
LOG_LIMIT=262144

WORK_DIR=
OUTPUT_STAGE=
BACKUP_STAGE=
CLAIM_FILE=
LOCK_HELD=0
LOCK_TOKEN=
LOCK_PID=
LOCK_START=

plain_error() {
	printf 'update: %s\n' "$*" >&2
}

[ -r "$FUNCTIONS_SH" ] || {
	plain_error "cannot read $FUNCTIONS_SH"
	exit 1
}
. "$FUNCTIONS_SH" || {
	plain_error "cannot load $FUNCTIONS_SH"
	exit 1
}

stat_start_time() {
	printf '%s\n' "$1" | awk '{ line=$0; sub(/^.*\) /, "", line); split(line, field, " "); print field[20] }'
}

process_start_time() {
	local pid="$1" stat_line
	[ -r "/proc/$pid/stat" ] || return 1
	IFS= read -r stat_line < "/proc/$pid/stat" || return 1
	stat_start_time "$stat_line"
}

self_identity() {
	local stat_line
	IFS= read -r stat_line < /proc/self/stat || return 1
	LOCK_PID=${stat_line%% *}
	LOCK_START=$(stat_start_time "$stat_line")
	case "$LOCK_PID:$LOCK_START" in
		*[!0-9:]*) return 1 ;;
		:|*:) return 1 ;;
	esac
}

read_lock_owner() {
	local owner_file="$LOCK_DIR/owner" extra
	OWNER_PID=
	OWNER_START=
	OWNER_TOKEN=
	OWNER_LINE=
	[ -f "$owner_file" ] && [ ! -L "$owner_file" ] || return 1
	IFS=' ' read -r OWNER_PID OWNER_START OWNER_TOKEN extra < "$owner_file" || return 1
	[ -z "$extra" ] || return 1
	case "$OWNER_PID" in ''|*[!0-9]*) return 1 ;; esac
	case "$OWNER_START" in ''|*[!0-9]*) return 1 ;; esac
	case "$OWNER_TOKEN" in ''|*[!A-Za-z0-9._-]*) return 1 ;; esac
	OWNER_LINE="$OWNER_PID $OWNER_START $OWNER_TOKEN"
	return 0
}

make_lock_token() {
	local random
	random=$(od -An -N12 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')
	[ -n "$random" ] || random="$(date +%s).$LOCK_PID"
	printf '%s.%s.%s' "$LOCK_PID" "$LOCK_START" "$random"
}

acquire_lock() {
	local parent lock_name attempts=0 current_start observed empty_wait
	parent=$(dirname "$LOCK_DIR") || return 1
	lock_name=${LOCK_DIR##*/}
	mkdir -p "$parent" || {
		plain_error "cannot create lock parent $parent"
		return 1
	}
	[ -d "$parent" ] && [ ! -L "$parent" ] || {
		plain_error "unsafe lock parent $parent"
		return 1
	}
	self_identity || {
		plain_error "cannot inspect updater process start time"
		return 1
	}
	LOCK_TOKEN=$(make_lock_token) || return 1
	CLAIM_FILE=$(mktemp "$parent/.${lock_name}.owner.XXXXXX") || {
		plain_error "cannot create updater owner claim"
		return 1
	}
	printf '%s %s %s\n' "$LOCK_PID" "$LOCK_START" "$LOCK_TOKEN" > "$CLAIM_FILE" || {
		plain_error "cannot prepare updater owner claim"
		return 1
	}
	chmod 0600 "$CLAIM_FILE" || {
		plain_error "cannot secure updater owner claim"
		return 1
	}

	while [ "$attempts" -lt 3 ]; do
		if mkdir "$LOCK_DIR" 2>/dev/null; then
			# The complete owner record is prepared before the atomic mkdir. Moving
			# it into an empty claimed directory is atomic on this filesystem, so an
			# interruption leaves either an empty recoverable directory or a complete
			# owner record, never a partial owner file.
			mv "$CLAIM_FILE" "$LOCK_DIR/owner" || {
				rmdir "$LOCK_DIR" 2>/dev/null || true
				plain_error "cannot publish updater lock owner"
				return 1
			}
			CLAIM_FILE=
			LOCK_HELD=1
			return 0
		fi

		[ -d "$LOCK_DIR" ] && [ ! -L "$LOCK_DIR" ] || {
			plain_error "unsafe updater lock path"
			return 1
		}
		if [ ! -e "$LOCK_DIR/owner" ]; then
			empty_wait=0
			while [ "$empty_wait" -lt 2 ] && [ ! -e "$LOCK_DIR/owner" ]; do
				sleep 1
				empty_wait=$((empty_wait + 1))
			done
			if [ ! -e "$LOCK_DIR/owner" ]; then
				if rmdir "$LOCK_DIR" 2>/dev/null; then
					attempts=$((attempts + 1))
					continue
				fi
				plain_error "updater lock has an incomplete non-empty claim"
				return 75
			fi
		fi
		if ! read_lock_owner; then
			plain_error "updater lock is busy with unreadable owner metadata"
			return 75
		fi
		current_start=$(process_start_time "$OWNER_PID" 2>/dev/null || true)
		# A matching /proc start time proves both liveness and PID identity. This is
		# stronger than kill -0 alone, which cannot distinguish PID reuse.
		if [ -n "$current_start" ] && [ "$current_start" = "$OWNER_START" ]; then
			plain_error "another update operation is already running"
			return 75
		fi

		observed=$(cat "$LOCK_DIR/owner" 2>/dev/null) || {
			plain_error "updater lock changed while recovering"
			return 75
		}
		[ "$observed" = "$OWNER_LINE" ] || {
			plain_error "updater lock owner changed while recovering"
			return 75
		}
		rm -f "$LOCK_DIR/owner" || return 1
		rmdir "$LOCK_DIR" 2>/dev/null || {
			plain_error "cannot recover stale updater lock"
			return 75
		}
		attempts=$((attempts + 1))
	done
	plain_error "cannot acquire updater lock"
	return 75
}

release_lock() {
	local observed
	[ "$LOCK_HELD" = 1 ] || return 0
	if read_lock_owner; then
		observed="$OWNER_PID $OWNER_START $OWNER_TOKEN"
		if [ "$observed" = "$LOCK_PID $LOCK_START $LOCK_TOKEN" ]; then
			rm -f "$LOCK_DIR/owner" 2>/dev/null || true
			rmdir "$LOCK_DIR" 2>/dev/null || true
		fi
	fi
	LOCK_HELD=0
}

cleanup() {
	[ -n "$OUTPUT_STAGE" ] && rm -f "$OUTPUT_STAGE"
	[ -n "$BACKUP_STAGE" ] && rm -f "$BACKUP_STAGE"
	[ -n "$CLAIM_FILE" ] && rm -f "$CLAIM_FILE"
	[ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
	release_lock
}

trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

prepare_log() {
	local directory
	directory=$(dirname "$LOG") || return 1
	mkdir -p "$directory" || return 1
	[ ! -L "$LOG" ] || return 1
	: >> "$LOG" || return 1
	chmod 0600 "$LOG"
}

rotate_log_for() {
	local additional="$1" current=0
	[ -f "$LOG" ] && current=$(wc -c < "$LOG" 2>/dev/null)
	case "$current" in ''|*[!0-9]*) current=0 ;; esac
	[ $((current + additional)) -lt "$LOG_LIMIT" ] && return 0
	rm -f "$LOG.2" "$LOG.3" || return 1
	[ ! -f "$LOG.1" ] || mv "$LOG.1" "$LOG.2" || return 1
	[ ! -f "$LOG" ] || mv "$LOG" "$LOG.1" || return 1
	: > "$LOG" || return 1
	chmod 0600 "$LOG" || return 1
	[ ! -f "$LOG.1" ] || chmod 0600 "$LOG.1" || return 1
	[ ! -f "$LOG.2" ] || chmod 0600 "$LOG.2" || return 1
}

log() {
	local line bytes
	line="$(date '+%Y-%m-%d %H:%M:%S') $*"
	bytes=$(printf '%s\n' "$line" | wc -c)
	bytes=$(printf '%s' "$bytes" | tr -d '[:space:]')
	rotate_log_for "$bytes" || {
		plain_error "cannot rotate $LOG"
		return 1
	}
	printf '%s\n' "$line" >> "$LOG" || return 1
	printf '%s\n' "$line" >&2
}

append_command_log() {
	local file="$1" line
	[ -s "$file" ] || return 0
	while IFS= read -r line || [ -n "$line" ]; do
		log "$line" || return 1
	done < "$file"
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

valid_scalar() {
	! printf '%s' "$1" | LC_ALL=C grep -q '[[:cntrl:]]'
}

valid_http_url() {
	valid_scalar "$1" || return 1
	case "$1" in http://?*|https://?*) return 0 ;; *) return 1 ;; esac
}

urlencode() {
	printf '%s' "$1" | LC_ALL=C awk '
	BEGIN { ORS=""; for (i=0; i<256; i++) ord[sprintf("%c", i)]=i }
	{
		n=length($0)
		for (i=1; i<=n; i++) {
			c=substr($0, i, 1)
			if (c ~ /[A-Za-z0-9._~-]/) printf "%s", c
			else printf "%%%02X", ord[c]
		}
	}'
}

load_cfg() {
	config_load "$CONFIG" || {
		log "failed to load UCI config $CONFIG"
		return 1
	}
	config_get port main port '9716'
	config_get password main password '890716'
	config_get sub_url main subscription_url ''
	config_get sub_timeout main subscription_timeout '60'
	config_get default_template main default_template 'momo_template'
	config_get output_config main output_config '/etc/momo/profiles/config.json'
	uint_between "$port" 1 65535 || {
		log "invalid port"
		return 1
	}
	uint_between "$sub_timeout" 5 600 || {
		log "invalid subscription_timeout"
		return 1
	}
	valid_scalar "$password" && valid_scalar "$default_template" || {
		log "password or default_template contains a control character"
		return 1
	}
	[ -n "$password" ] || {
		log "password must not be empty"
		return 1
	}
	request_timeout=$((sub_timeout + 60))
	pass_q=$(urlencode "$password")
	template_q=$(urlencode "$default_template")
}

health_ok() {
	local health_file="$WORK_DIR/health.json" service status
	rm -f "$health_file"
	curl -fsS --connect-timeout 1 --max-time 2 \
		"http://127.0.0.1:${port}/health" -o "$health_file" >/dev/null 2>&1 || return 1
	service=$(jsonfilter -i "$health_file" -e '@.service' 2>/dev/null) || return 1
	status=$(jsonfilter -i "$health_file" -e '@.status' 2>/dev/null) || return 1
	[ "$service" = singbox-subscribe-convert ] && [ "$status" = ok ]
}

ensure_converter_for_generation() {
	local i=0
	health_ok && return 0
	log "converter is not running/ready on port ${port}; starting it" || return 1
	"$INIT" start manual >/dev/null 2>&1 || {
		log "converter start failed" || true
		return 1
	}
	while [ "$i" -lt 20 ]; do
		health_ok && return 0
		sleep 1
		i=$((i + 1))
	done
	log "converter did not become ready on port ${port} within 20s" || true
	return 1
}

fetch_config() {
	local output="$1"
	curl -fsS --connect-timeout 10 --max-time "$request_timeout" \
		"http://127.0.0.1:${port}/?password=${pass_q}&template=${template_q}" \
		-o "$output"
}

refresh_converter() {
	local output="$WORK_DIR/refresh.json"
	curl -fsS --connect-timeout 10 --max-time "$request_timeout" \
		"http://127.0.0.1:${port}/refresh?password=${pass_q}" \
		-o "$output"
}

validate_generated() {
	local file="$1" command_log="$WORK_DIR/sing-box-check.log"
	[ -s "$file" ] || {
		log "generated file is empty"
		return 1
	}
	jsonfilter -i "$file" -e '@.outbounds' >/dev/null 2>&1 || {
		log "jsonfilter check failed"
		return 1
	}
	if command -v sing-box >/dev/null 2>&1; then
		: > "$command_log"
		if ! sing-box check -c "$file" > "$command_log" 2>&1; then
			append_command_log "$command_log" || true
			log "sing-box check failed" || true
			return 1
		fi
		append_command_log "$command_log" || return 1
	fi
	return 0
}

prune_backups() {
	LC_ALL=C ls -1t "$output_config".bak.* 2>/dev/null | awk 'NR > 5 { print }' |
		while IFS= read -r old; do
			rm -f "$old" || return 1
		done
}

install_output() {
	local generated="$1" output_dir output_base timestamp cmp_status
	output_dir=$(dirname "$output_config") || return 1
	output_base=${output_config##*/}
	mkdir -p "$output_dir" || {
		log "cannot create output directory"
		return 1
	}
	[ ! -L "$output_config" ] || {
		log "refusing symlink output path"
		return 1
	}
	if [ -f "$output_config" ]; then
		cmp -s "$generated" "$output_config"
		cmp_status=$?
		case "$cmp_status" in
			0)
				chmod 0600 "$output_config" || return 1
				prune_backups || {
					log "failed to prune old output backups" || true
					return 1
				}
				log "generated output is unchanged"
				return 0
				;;
			1) ;;
			*)
				log "failed to compare generated output with installed output" || true
				return 1
				;;
		esac
	fi

	OUTPUT_STAGE=$(mktemp "$output_dir/.${output_base}.new.XXXXXX") || return 1
	cp "$generated" "$OUTPUT_STAGE" || return 1
	chmod 0600 "$OUTPUT_STAGE" || return 1

	if [ -f "$output_config" ]; then
		timestamp=$(date +%Y%m%d-%H%M%S)
		BACKUP_STAGE=$(mktemp "${output_config}.bak.${timestamp}.XXXXXX") || return 1
		cp "$output_config" "$BACKUP_STAGE" || return 1
		chmod 0600 "$BACKUP_STAGE" || return 1
	fi

	mv "$OUTPUT_STAGE" "$output_config" || return 1
	OUTPUT_STAGE=
	BACKUP_STAGE=
	prune_backups || {
		log "failed to prune old output backups" || true
		return 1
	}
	log "installed generated config to $output_config" || return 1
	log "sing-box was not restarted; manage runtime from OpenWrt Momo or another app"
}

cmd=${1:-apply}
case "$cmd" in
	generate|refresh|check|apply) ;;
	*)
		printf 'usage: %s {apply|check|refresh|generate}\n' "$0" >&2
		exit 2
		;;
esac

acquire_lock || exit $?
mkdir -p "$TMP_ROOT" || {
	plain_error "cannot create temporary root $TMP_ROOT"
	exit 1
}
WORK_DIR=$(mktemp -d "$TMP_ROOT/singbox-formula-update.XXXXXX") || {
	plain_error "cannot create updater working directory"
	exit 1
}
prepare_log || {
	plain_error "cannot prepare $LOG"
	exit 1
}
load_cfg || exit 1

case "$cmd" in
	generate)
		"$GEN" >/dev/null || {
			log "converter config generation failed" || true
			exit 1
		}
		log "generated converter config" || exit 1
		;;
	refresh)
		health_ok || {
			log "cannot refresh because the converter is not running and healthy" || true
			exit 1
		}
		refresh_converter || {
			log "refresh failed" || true
			exit 1
		}
		log "refresh ok" || exit 1
		;;
	check|apply)
		[ -n "$sub_url" ] && valid_http_url "$sub_url" || {
			log "subscription_url must use HTTP or HTTPS" || true
			exit 1
		}
		"$GEN" >/dev/null || {
			log "converter config generation failed" || true
			exit 1
		}
		ensure_converter_for_generation || {
			log "cannot reach converter; enable/start it and check the subscription URL" || true
			exit 1
		}
		refresh_converter || log "refresh failed; trying cached data" || exit 1
		GENERATED="$WORK_DIR/generated.json"
		fetch_config "$GENERATED" || {
			log "failed to fetch generated config" || true
			exit 1
		}
		validate_generated "$GENERATED" || exit 1
		if [ "$cmd" = check ]; then
			log "check ok" || exit 1
		else
			install_output "$GENERATED" || exit 1
		fi
		;;
esac
