#!/bin/sh
# Usage:
#   update.sh apply    # generate, validate and atomically install a sing-box profile
#   update.sh check    # generate and validate without installing
#   update.sh refresh  # call converter refresh without regenerating config.yaml
#   update.sh generate # generate converter config.yaml only

umask 077

FUNCTIONS_SH=${SBF_FUNCTIONS_SH:-/lib/functions.sh}
CONFIG=${SBF_CONFIG_NAME:-liquid_formula}
GEN=${SBF_GENERATOR:-/usr/share/liquid-formula/generate-config.sh}
INIT=${SBF_INIT_SCRIPT:-/etc/init.d/liquid-formula}
LOG=${SBF_LOG_FILE:-/var/log/liquid-formula/update.log}
LOCK_DIR=${SBF_LOCK_DIR:-/var/run/liquid-formula/update.lock}
LIFECYCLE_LOCK_DIR=${SBF_LIFECYCLE_LOCK_DIR:-/var/run/liquid-formula/lifecycle.lock}
TMP_ROOT=${SBF_TMP_ROOT:-${TMPDIR:-/tmp}}
GENERATED_CONFIG=${SBF_GENERATED_CONFIG:-/etc/liquid-formula/config.yaml}
SUBSCRIPTION_STATE=/var/lib/liquid-formula/subscriptions
CURRENT_FILE=$SUBSCRIPTION_STATE/current
SUBSCRIPTION_LOCK_FILE=${SBF_SUBSCRIPTION_LOCK_FILE:-/var/run/liquid-formula/subscription.lock}
SUBSCRIPTION_BARRIER_FILE=${SBF_SUBSCRIPTION_BARRIER_FILE:-${SUBSCRIPTION_LOCK_FILE}.barrier}
FAULT_HOOK=${SBF_TEST_FAULT_HOOK:-}
LOG_LIMIT=262144

WORK_DIR=
OUTPUT_STAGE=
STARTED_CONVERTER=0
RESTORE_AT_REST_CONFIG=0
SERVICE_ENABLED=0
BACKUP_STAGE=
CLAIM_FILE=
LOCK_HELD=0
LOCK_TOKEN=
LOCK_PID=
LOCK_START=
LIFECYCLE_LOCK_HELD=0
LIFECYCLE_TOKEN=
SUBSCRIPTION_LOCK_HELD=0
SUBSCRIPTION_LOCK_ID=
SUBSCRIPTION_BARRIER_ACTIVE=0
SUBSCRIPTION_BARRIER_ID=
SUBSCRIPTION_BARRIER_CLAIM=

LOG_READY=0
CHECKED_RESULT=0
SUBSCRIPTION_URL_COUNT=0
ENABLED_TEMPLATE_COUNT=0
CONFIG_DIGEST=
GENERATION_BEFORE_REFRESH=
VALIDATED_GENERATION=
VALIDATED_AGGREGATE_SHA=
PINNED_GENERATION=
PINNED_AGGREGATE_SHA=
CACHE_NODE=

# 这些错误在 rpcd 后台执行时 stderr 会被丢弃, 所以日志一就绪就同时落盘,
# 否则界面提示“见 update log”而那个文件根本不存在。
plain_error() {
	printf 'update: %s\n' "$*" >&2
	[ "$LOG_READY" = 1 ] || return 0
	printf '%s update: %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >> "$LOG" 2>/dev/null || true
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

read_lifecycle_owner() {
	local owner_file="$LIFECYCLE_LOCK_DIR/owner" extra
	LIFECYCLE_OWNER_PID=
	LIFECYCLE_OWNER_START=
	LIFECYCLE_OWNER_TOKEN=
	LIFECYCLE_OWNER_LINE=
	[ -f "$owner_file" ] && [ ! -L "$owner_file" ] || return 1
	IFS=' ' read -r LIFECYCLE_OWNER_PID LIFECYCLE_OWNER_START LIFECYCLE_OWNER_TOKEN extra < "$owner_file" || return 1
	[ -z "$extra" ] || return 1
	case "$LIFECYCLE_OWNER_PID" in ''|*[!0-9]*) return 1 ;; esac
	case "$LIFECYCLE_OWNER_START" in ''|*[!0-9]*) return 1 ;; esac
	case "$LIFECYCLE_OWNER_TOKEN" in ''|*[!A-Za-z0-9._-]*) return 1 ;; esac
	LIFECYCLE_OWNER_LINE="$LIFECYCLE_OWNER_PID $LIFECYCLE_OWNER_START $LIFECYCLE_OWNER_TOKEN"
}

lifecycle_delegate_blocks_recovery() {
	local delegate_file="$LIFECYCLE_LOCK_DIR/delegate" extra current_start observed
	[ -e "$delegate_file" ] || [ -L "$delegate_file" ] || return 1
	LIFECYCLE_DELEGATE_PID=
	LIFECYCLE_DELEGATE_START=
	LIFECYCLE_DELEGATE_TOKEN=
	[ -f "$delegate_file" ] && [ ! -L "$delegate_file" ] || return 0
	IFS=' ' read -r LIFECYCLE_DELEGATE_PID LIFECYCLE_DELEGATE_START LIFECYCLE_DELEGATE_TOKEN extra < "$delegate_file" || return 0
	[ -z "$extra" ] || return 0
	case "$LIFECYCLE_DELEGATE_PID" in ''|*[!0-9]*) return 0 ;; esac
	case "$LIFECYCLE_DELEGATE_START" in ''|*[!0-9]*) return 0 ;; esac
	case "$LIFECYCLE_DELEGATE_TOKEN" in ''|*[!A-Za-z0-9._-]*) return 0 ;; esac
	current_start=$(process_start_time "$LIFECYCLE_DELEGATE_PID" 2>/dev/null || true)
	if [ -n "$current_start" ] && [ "$current_start" = "$LIFECYCLE_DELEGATE_START" ]; then
		return 0
	fi
	observed=$(cat "$delegate_file" 2>/dev/null) || return 0
	[ "$observed" = "$LIFECYCLE_DELEGATE_PID $LIFECYCLE_DELEGATE_START $LIFECYCLE_DELEGATE_TOKEN" ] || return 0
	rm -f "$delegate_file" || return 0
	return 1
}

acquire_lifecycle_lock() {
	local parent lock_name claim attempts=0 current_start observed empty_wait
	parent=$(dirname "$LIFECYCLE_LOCK_DIR") || return 1
	lock_name=${LIFECYCLE_LOCK_DIR##*/}
	mkdir -p "$parent" || return 1
	[ -d "$parent" ] && [ ! -L "$parent" ] || return 1
	[ -n "$LOCK_PID" ] && [ -n "$LOCK_START" ] || self_identity || return 1
	LIFECYCLE_TOKEN=$(make_lock_token) || return 1
	claim=$(mktemp "$parent/.${lock_name}.owner.XXXXXX") || return 1
	printf '%s %s %s\n' "$LOCK_PID" "$LOCK_START" "$LIFECYCLE_TOKEN" > "$claim" || {
		rm -f "$claim"
		return 1
	}
	chmod 0600 "$claim" || {
		rm -f "$claim"
		return 1
	}
	while [ "$attempts" -lt 3 ]; do
		if mkdir "$LIFECYCLE_LOCK_DIR" 2>/dev/null; then
			mv "$claim" "$LIFECYCLE_LOCK_DIR/owner" || {
				rmdir "$LIFECYCLE_LOCK_DIR" 2>/dev/null || true
				rm -f "$claim"
				return 1
			}
			LIFECYCLE_LOCK_HELD=1
			return 0
		fi
		[ -d "$LIFECYCLE_LOCK_DIR" ] && [ ! -L "$LIFECYCLE_LOCK_DIR" ] || {
			rm -f "$claim"
			return 1
		}
		if [ ! -e "$LIFECYCLE_LOCK_DIR/owner" ]; then
			empty_wait=0
			while [ "$empty_wait" -lt 2 ] && [ ! -e "$LIFECYCLE_LOCK_DIR/owner" ]; do
				sleep 1
				empty_wait=$((empty_wait + 1))
			done
			if [ ! -e "$LIFECYCLE_LOCK_DIR/owner" ]; then
				if lifecycle_delegate_blocks_recovery; then
					rm -f "$claim"
					return 75
				fi
				if rmdir "$LIFECYCLE_LOCK_DIR" 2>/dev/null; then
					attempts=$((attempts + 1))
					continue
				fi
				rm -f "$claim"
				return 75
			fi
		fi
		read_lifecycle_owner || {
			rm -f "$claim"
			return 75
		}
		current_start=$(process_start_time "$LIFECYCLE_OWNER_PID" 2>/dev/null || true)
		if [ -n "$current_start" ] && [ "$current_start" = "$LIFECYCLE_OWNER_START" ]; then
			rm -f "$claim"
			return 75
		fi
		if lifecycle_delegate_blocks_recovery; then
			rm -f "$claim"
			return 75
		fi
		# Delegate inspection may have raced an ownership change.
		read_lifecycle_owner || {
			rm -f "$claim"
			return 75
		}
		current_start=$(process_start_time "$LIFECYCLE_OWNER_PID" 2>/dev/null || true)
		if [ -n "$current_start" ] && [ "$current_start" = "$LIFECYCLE_OWNER_START" ]; then
			rm -f "$claim"
			return 75
		fi
		observed=$(cat "$LIFECYCLE_LOCK_DIR/owner" 2>/dev/null) || {
			rm -f "$claim"
			return 75
		}
		[ "$observed" = "$LIFECYCLE_OWNER_LINE" ] || {
			rm -f "$claim"
			return 75
		}
		rm -f "$LIFECYCLE_LOCK_DIR/owner" && rmdir "$LIFECYCLE_LOCK_DIR" || {
			rm -f "$claim"
			return 75
		}
		attempts=$((attempts + 1))
	done
	rm -f "$claim"
	return 75
}

release_lifecycle_lock() {
	local observed
	[ "$LIFECYCLE_LOCK_HELD" = 1 ] || return 0
	if read_lifecycle_owner; then
		observed="$LIFECYCLE_OWNER_PID $LIFECYCLE_OWNER_START $LIFECYCLE_OWNER_TOKEN"
		if [ "$observed" = "$LOCK_PID $LOCK_START $LIFECYCLE_TOKEN" ]; then
			rm -f "$LIFECYCLE_LOCK_DIR/owner" 2>/dev/null || true
			rmdir "$LIFECYCLE_LOCK_DIR" 2>/dev/null || true
		fi
	fi
	LIFECYCLE_LOCK_HELD=0
	LIFECYCLE_TOKEN=
}

subscription_lock_fd_identity() {
	stat -Lc '%d:%i:%u:%a:%h' /proc/self/fd/8 2>/dev/null
}

subscription_lock_path_identity() {
	[ -f "$SUBSCRIPTION_LOCK_FILE" ] &&
		[ ! -L "$SUBSCRIPTION_LOCK_FILE" ] || return 1
	stat -c '%d:%i:%u:%a:%h' "$SUBSCRIPTION_LOCK_FILE" 2>/dev/null
}

monotonic_seconds() {
	local uptime remainder
	IFS=' ' read -r uptime remainder < /proc/uptime || return 1
	uptime=${uptime%%.*}
	uint_between "$uptime" 0 2147483647 || return 1
	printf '%s\n' "$uptime"
}

acquire_subscription_lock() {
	local parent wait_limit started now fd_identity path_identity
	command -v flock >/dev/null 2>&1 || {
		log "flock is unavailable for subscription snapshot binding" || true
		return 1
	}
	parent=$(dirname "$SUBSCRIPTION_LOCK_FILE") || return 1
	mkdir -p "$parent" || return 1
	[ -d "$parent" ] && [ ! -L "$parent" ] || {
		log "subscription lock parent is unsafe" || true
		return 1
	}
	if [ -e "$SUBSCRIPTION_LOCK_FILE" ] || [ -L "$SUBSCRIPTION_LOCK_FILE" ]; then
		[ -f "$SUBSCRIPTION_LOCK_FILE" ] && [ ! -L "$SUBSCRIPTION_LOCK_FILE" ] || {
			log "subscription lock path is unsafe" || true
			return 1
		}
	else
		: > "$SUBSCRIPTION_LOCK_FILE" || return 1
	fi
	chmod 0600 "$SUBSCRIPTION_LOCK_FILE" || return 1
	exec 8<> "$SUBSCRIPTION_LOCK_FILE" || return 1

	fd_identity=$(subscription_lock_fd_identity) || {
		exec 8>&-
		log "cannot inspect the opened subscription lock" || true
		return 1
	}
	case "$fd_identity" in *:600:1) ;; *)
		exec 8>&-
		log "opened subscription lock metadata is unsafe" || true
		return 1
		;;
	esac

	wait_limit=${SBF_SUBSCRIPTION_LOCK_WAIT_LIMIT:-$request_timeout}
	uint_between "$wait_limit" 1 "$request_timeout" || {
		exec 8>&-
		log "invalid subscription lock wait limit" || true
		return 1
	}
	started=$(monotonic_seconds) || {
		exec 8>&-
		log "cannot read the monotonic clock for subscription locking" || true
		return 1
	}
	while ! flock -n -x 8; do
		now=$(monotonic_seconds) || {
			exec 8>&-
			log "cannot read the monotonic clock for subscription locking" || true
			return 1
		}
		[ $((now - started)) -lt "$wait_limit" ] || {
			exec 8>&-
			log "timed out waiting for the subscription generation lock" || true
			return 1
		}
		sleep 1
	done

	path_identity=$(subscription_lock_path_identity) || {
		flock -u 8 >/dev/null 2>&1 || true
		exec 8>&-
		log "subscription lock changed while acquiring it" || true
		return 1
	}
	[ "$path_identity" = "$fd_identity" ] || {
		flock -u 8 >/dev/null 2>&1 || true
		exec 8>&-
		log "subscription lock identity changed while acquiring it" || true
		return 1
	}
	SUBSCRIPTION_LOCK_ID=$fd_identity
	SUBSCRIPTION_LOCK_HELD=1
	return 0
}

subscription_file_identity() {
	stat -c '%d:%i:%u:%a:%h:%s' "$1" 2>/dev/null
}

activate_subscription_barrier() {
	local parent lock_parent claim identity lock_identity lock_uid marker_uid marker_mode

	[ "$SUBSCRIPTION_LOCK_HELD" = 1 ] || return 1
	lock_identity=$(subscription_lock_path_identity) || return 1
	[ "$lock_identity" = "$SUBSCRIPTION_LOCK_ID" ] || {
		log "subscription lock identity changed before synchronization" || true
		return 1
	}
	parent=$(dirname "$SUBSCRIPTION_BARRIER_FILE") || return 1
	lock_parent=$(dirname "$SUBSCRIPTION_LOCK_FILE") || return 1
	[ "$parent" = "$lock_parent" ] || {
		log "subscription barrier must share the lock directory" || true
		return 1
	}
	[ -d "$parent" ] && [ ! -L "$parent" ] || return 1
	lock_uid=$(stat -c '%u' "$SUBSCRIPTION_LOCK_FILE" 2>/dev/null) || return 1
	if [ -e "$SUBSCRIPTION_BARRIER_FILE" ] ||
	   [ -L "$SUBSCRIPTION_BARRIER_FILE" ]; then
		[ -f "$SUBSCRIPTION_BARRIER_FILE" ] &&
			[ ! -L "$SUBSCRIPTION_BARRIER_FILE" ] || {
			log "subscription barrier path is unsafe" || true
			return 1
		}
		marker_mode=$(stat -c '%a:%h:%s' "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null) ||
			return 1
		marker_uid=$(stat -c '%u' "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null) ||
			return 1
		[ "$lock_uid" = "$marker_uid" ] &&
			[ "$marker_mode" = 600:1:3 ] &&
			[ "$(cat "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null)" = v1 ] || {
			log "subscription barrier path contains invalid stale state" || true
			return 1
		}
		rm -f "$SUBSCRIPTION_BARRIER_FILE" || return 1
	fi

	claim=$(mktemp "$parent/.subscription.barrier.XXXXXX") || return 1
	SUBSCRIPTION_BARRIER_CLAIM=$claim
	printf 'v1\n' > "$claim" || return 1
	chmod 0600 "$claim" || return 1
	mv "$claim" "$SUBSCRIPTION_BARRIER_FILE" || return 1
	SUBSCRIPTION_BARRIER_CLAIM=
	[ -f "$SUBSCRIPTION_BARRIER_FILE" ] &&
		[ ! -L "$SUBSCRIPTION_BARRIER_FILE" ] || {
			rm -f "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null || true
			return 1
		}
	identity=$(subscription_file_identity "$SUBSCRIPTION_BARRIER_FILE") || {
		rm -f "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null || true
		return 1
	}
	SUBSCRIPTION_BARRIER_ID=$identity
	SUBSCRIPTION_BARRIER_ACTIVE=1
	marker_uid=$(stat -c '%u' "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null) || {
		remove_subscription_barrier >/dev/null 2>&1 || true
		return 1
	}
	marker_mode=$(stat -c '%a:%h:%s' "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null) ||
		{ remove_subscription_barrier >/dev/null 2>&1 || true; return 1; }
	[ "$lock_uid" = "$marker_uid" ] && [ "$marker_mode" = 600:1:3 ] ||
		{ remove_subscription_barrier >/dev/null 2>&1 || true; return 1; }
	[ "$(cat "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null)" = v1 ] ||
		{ remove_subscription_barrier >/dev/null 2>&1 || true; return 1; }
	lock_identity=$(subscription_lock_path_identity) || {
		remove_subscription_barrier >/dev/null 2>&1 || true
		return 1
	}
	[ "$lock_identity" = "$SUBSCRIPTION_LOCK_ID" ] || {
		remove_subscription_barrier >/dev/null 2>&1 || true
		log "subscription lock identity changed during synchronization setup" || true
		return 1
	}
}

remove_subscription_barrier() {
	local identity

	[ "$SUBSCRIPTION_BARRIER_ACTIVE" = 1 ] || {
		[ -z "$SUBSCRIPTION_BARRIER_CLAIM" ] ||
			rm -f "$SUBSCRIPTION_BARRIER_CLAIM" 2>/dev/null || true
		SUBSCRIPTION_BARRIER_CLAIM=
		return 0
	}
	identity=$(subscription_file_identity "$SUBSCRIPTION_BARRIER_FILE" 2>/dev/null) ||
		return 1
	[ "$identity" = "$SUBSCRIPTION_BARRIER_ID" ] || return 1
	rm -f "$SUBSCRIPTION_BARRIER_FILE" || return 1
	SUBSCRIPTION_BARRIER_ACTIVE=0
	SUBSCRIPTION_BARRIER_ID=
}

release_subscription_lock() {
	local result=0
	[ "$SUBSCRIPTION_LOCK_HELD" = 1 ] || return 0
	remove_subscription_barrier || result=1
	flock -u 8 >/dev/null 2>&1 || true
	exec 8>&-
	SUBSCRIPTION_LOCK_HELD=0
	SUBSCRIPTION_LOCK_ID=
	return "$result"
}

cleanup() {
	local original_rc="${1:-0}" cleanup_failed=0
	release_subscription_lock || {
		plain_error "failed to remove the subscription synchronization barrier"
		cleanup_failed=1
	}
	# Check / Update 允许在服务关着的时候临时把转换器拉起来跑一次。既然是临时
	# 的就必须还原, 否则界面会停在 "Autostart: Off / Status: Running", 而用户
	# 从没打开过这个服务。
	if [ "${STARTED_CONVERTER:-0}" = 1 ]; then
		STARTED_CONVERTER=0
		if LF_LIFECYCLE_TOKEN="$LIFECYCLE_TOKEN" "$INIT" stop >/dev/null 2>&1; then
			log "stopped the converter that this run started" || true
		else
			plain_error "failed to stop the converter that this run started"
			cleanup_failed=1
		fi
	fi
	# Manual Check / Update intentionally generates a private runtime config even
	# while the service is disabled. Restore the scrubbed at-rest form whenever
	# no converter is running, including a start failure before temporary
	# ownership could be recorded.
	if [ "${RESTORE_AT_REST_CONFIG:-0}" = 1 ]; then
		RESTORE_AT_REST_CONFIG=0
		if SBF_INCLUDE_DISABLED_URLS=0 "$GEN" >/dev/null; then
			log "restored the scrubbed on-disk converter config after the manual run" || true
		else
			plain_error "failed to restore the at-rest converter config after the manual run"
			cleanup_failed=1
		fi
	fi
	release_lifecycle_lock
	[ -n "$OUTPUT_STAGE" ] && rm -f "$OUTPUT_STAGE"
	[ -n "$BACKUP_STAGE" ] && rm -f "$BACKUP_STAGE"
	[ -n "$CLAIM_FILE" ] && rm -f "$CLAIM_FILE"
	[ -n "$SUBSCRIPTION_BARRIER_CLAIM" ] &&
		rm -f "$SUBSCRIPTION_BARRIER_CLAIM" 2>/dev/null || true
	[ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
	release_lock
	[ "$original_rc" -ne 0 ] || [ "$cleanup_failed" = 0 ] || original_rc=1
	trap - 0
	exit "$original_rc"
}

trap 'cleanup $?' 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

prepare_log() {
	local directory
	directory=$(dirname "$LOG") || return 1
	mkdir -p "$directory" || return 1
	[ ! -L "$LOG" ] || return 1
	: >> "$LOG" || return 1
	chmod 0600 "$LOG" || return 1
	LOG_READY=1
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

checked_add_int32() {
	local left="$1" right="$2"
	uint_between "$left" 0 2147483647 || return 1
	uint_between "$right" 0 2147483647 || return 1
	[ "$left" -le $((2147483647 - right)) ] || return 1
	CHECKED_RESULT=$((left + right))
}

checked_multiply_int32() {
	local left="$1" right="$2"
	uint_between "$left" 0 2147483647 || return 1
	uint_between "$right" 0 2147483647 || return 1
	if [ "$left" -eq 0 ] || [ "$right" -eq 0 ]; then
		CHECKED_RESULT=0
		return 0
	fi
	[ "$left" -le $((2147483647 / right)) ] || return 1
	CHECKED_RESULT=$((left * right))
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

count_enabled_template() {
	local sid="$1" tpl_enabled
	config_get_bool tpl_enabled "$sid" enabled 1
	[ "$tpl_enabled" != 1 ] || {
		checked_add_int32 "$ENABLED_TEMPLATE_COUNT" 1 || return 1
		ENABLED_TEMPLATE_COUNT=$CHECKED_RESULT
	}
	return 0
}

count_subscription_url() {
	local value="$1"
	[ -n "$value" ] && valid_http_url "$value" || return 1
	checked_add_int32 "$SUBSCRIPTION_URL_COUNT" 1 || return 1
	SUBSCRIPTION_URL_COUNT=$CHECKED_RESULT
	[ "$SUBSCRIPTION_URL_COUNT" -le 8 ]
}

load_cfg() {
	local source_budget aggregate_timeout template_budget
	config_load "$CONFIG" || {
		log "failed to load UCI config $CONFIG"
		return 1
	}
	config_get port main port '9716'
	config_get_bool SERVICE_ENABLED main enabled 0
	config_get password main password '890716'
	config_get sub_timeout main subscription_timeout '60'
	config_get default_template main default_template 'momo_template'
	config_get cache_dir main cache_dir '/var/lib/liquid-formula/cache'
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
	valid_scalar "$cache_dir" || {
		log "cache_dir contains a control character"
		return 1
	}
	case "$cache_dir" in
		/*) ;;
		*)
			log "cache_dir must be absolute"
			return 1
			;;
	esac
	CACHE_NODE=${cache_dir%/}/node.json
	[ -n "$password" ] || {
		log "password must not be empty"
		return 1
	}
	SUBSCRIPTION_URL_COUNT=0
	config_list_foreach main subscription_url count_subscription_url || {
		log "subscription_url must contain one to eight HTTP or HTTPS URLs"
		return 1
	}
	if [ "$SUBSCRIPTION_URL_COUNT" -eq 0 ]; then
		if [ "$cmd" != generate ] || [ "$SERVICE_ENABLED" = 1 ]; then
			log "at least one subscription_url is required"
			return 1
		fi
	fi

	ENABLED_TEMPLATE_COUNT=0
	config_foreach count_enabled_template template || {
		log "failed to inspect templates"
		return 1
	}

	source_budget=$SUBSCRIPTION_URL_COUNT
	[ "$source_budget" -gt 0 ] || source_budget=1
	checked_multiply_int32 "$source_budget" "$sub_timeout" || {
		log "aggregate timeout exceeds signed 32-bit range"
		return 1
	}
	checked_add_int32 "$CHECKED_RESULT" 60 || {
		log "aggregate timeout exceeds signed 32-bit range"
		return 1
	}
	aggregate_timeout=$CHECKED_RESULT

	checked_multiply_int32 "$ENABLED_TEMPLATE_COUNT" "$sub_timeout" || {
		log "request timeout exceeds signed 32-bit range"
		return 1
	}
	template_budget=$CHECKED_RESULT
	checked_add_int32 "$aggregate_timeout" "$template_budget" || {
		log "request timeout exceeds signed 32-bit range"
		return 1
	}
	checked_add_int32 "$CHECKED_RESULT" 60 || {
		log "request timeout exceeds signed 32-bit range"
		return 1
	}
	request_timeout=$CHECKED_RESULT
	startup_wait=${SBF_STARTUP_WAIT_LIMIT:-$request_timeout}
	uint_between "$startup_wait" 1 2147483647 || {
		log "invalid converter startup wait limit"
		return 1
	}
	pass_q=$(urlencode "$password")
	template_q=$(urlencode "$default_template")
}

read_current_generation_id() {
	local bytes generation extra
	[ -e "$CURRENT_FILE" ] || [ -L "$CURRENT_FILE" ] || return 2
	[ -f "$CURRENT_FILE" ] && [ ! -L "$CURRENT_FILE" ] || return 1
	bytes=$(wc -c < "$CURRENT_FILE" 2>/dev/null)
	bytes=$(printf '%s' "$bytes" | tr -d '[:space:]')
	[ "$bytes" = 65 ] || return 1
	IFS= read -r generation extra < "$CURRENT_FILE" || return 1
	[ -z "${extra:-}" ] || return 1
	[ "${#generation}" -eq 64 ] || return 1
	case "$generation" in *[!0-9a-f]*) return 1 ;; esac
	printf '%s\n' "$generation"
}

hash_generated_config() {
	local digest
	[ -f "$GENERATED_CONFIG" ] && [ ! -L "$GENERATED_CONFIG" ] || return 1
	digest=$(sha256sum "$GENERATED_CONFIG" 2>/dev/null) || return 1
	digest=${digest%% *}
	[ "${#digest}" -eq 64 ] || return 1
	case "$digest" in *[!0-9a-f]*) return 1 ;; esac
	CONFIG_DIGEST=$digest
}

observe_generation_before_refresh() {
	local generation status
	hash_generated_config || {
		log "cannot hash generated converter config"
		return 1
	}
	generation=$(read_current_generation_id)
	status=$?
	case "$status" in
		0) GENERATION_BEFORE_REFRESH=$generation ;;
		2) GENERATION_BEFORE_REFRESH= ;;
		*)
			log "subscription current pointer is invalid before refresh"
			return 1
			;;
	esac
}

validate_current_generation() {
	local expected_digest="$1" generation first_generation status generation_dir
	local manifest_file status_file aggregate_file value actual actual_size aggregate_sha

	generation=$(read_current_generation_id) || {
		log "subscription current pointer is missing or invalid"
		return 1
	}
	first_generation=$generation
	generation_dir=$SUBSCRIPTION_STATE/generations/$generation
	[ -d "$generation_dir" ] && [ ! -L "$generation_dir" ] || {
		log "selected subscription generation directory is invalid"
		return 1
	}
	manifest_file=$generation_dir/manifest.json
	status_file=$generation_dir/status.json
	aggregate_file=$generation_dir/aggregate.json
	for value in "$manifest_file" "$status_file" "$aggregate_file"; do
		[ -f "$value" ] && [ ! -L "$value" ] || {
			log "selected subscription generation is incomplete"
			return 1
		}
	done

	value=$(jsonfilter -i "$manifest_file" -e '@.schema' 2>/dev/null) || return 1
	[ "$value" = 1 ] || return 1
	value=$(jsonfilter -i "$manifest_file" -e '@.generation' 2>/dev/null) || return 1
	[ "$value" = "$generation" ] || return 1
	value=$(jsonfilter -i "$manifest_file" -e '@.config_digest' 2>/dev/null) || return 1
	[ "$value" = "$expected_digest" ] || {
		log "selected subscription generation belongs to another config"
		return 1
	}

	value=$(jsonfilter -i "$manifest_file" -e '@.aggregate.sha256' 2>/dev/null) || return 1
	[ "${#value}" -eq 64 ] || return 1
	case "$value" in *[!0-9a-f]*) return 1 ;; esac
	aggregate_sha=$value
	actual=$(sha256sum "$aggregate_file" 2>/dev/null) || return 1
	actual=${actual%% *}
	[ "$actual" = "$value" ] || return 1
	value=$(jsonfilter -i "$manifest_file" -e '@.aggregate.bytes' 2>/dev/null) || return 1
	uint_between "$value" 1 2147483647 || return 1
	actual_size=$(wc -c < "$aggregate_file" 2>/dev/null)
	actual_size=$(printf '%s' "$actual_size" | tr -d '[:space:]')
	[ "$actual_size" = "$value" ] || return 1
	value=$(jsonfilter -i "$manifest_file" -e '@.aggregate.outbounds' 2>/dev/null) || return 1
	uint_between "$value" 1 8192 || return 1

	value=$(jsonfilter -i "$manifest_file" -e '@.status_sha256' 2>/dev/null) || return 1
	[ "${#value}" -eq 64 ] || return 1
	case "$value" in *[!0-9a-f]*) return 1 ;; esac
	actual=$(sha256sum "$status_file" 2>/dev/null) || return 1
	actual=${actual%% *}
	[ "$actual" = "$value" ] || return 1
	actual=$(jsonfilter -i "$status_file" -e '@.schema' 2>/dev/null) || return 1
	[ "$actual" = 1 ] || return 1
	actual=$(jsonfilter -i "$status_file" -e '@.generation' 2>/dev/null) || return 1
	[ "$actual" = "$generation" ] || return 1
	actual=$(jsonfilter -i "$status_file" -e '@.state' 2>/dev/null) || return 1
	case "$actual" in fresh|degraded) ;; *) return 1 ;; esac

	generation=$(read_current_generation_id) || return 1
	[ "$generation" = "$first_generation" ] || {
		log "subscription generation changed during validation"
		return 1
	}
	VALIDATED_GENERATION=$generation
	VALIDATED_AGGREGATE_SHA=$aggregate_sha
}

validate_pinned_generation() {
	validate_current_generation "$CONFIG_DIGEST" || return 1
	[ -n "$PINNED_GENERATION" ] &&
		[ "$VALIDATED_GENERATION" = "$PINNED_GENERATION" ] &&
		[ "$VALIDATED_AGGREGATE_SHA" = "$PINNED_AGGREGATE_SHA" ] || {
			log "subscription generation changed after the converter snapshot was pinned"
			return 1
		}
}

validate_pinned_converter_snapshot() {
	local digest size
	[ -n "$CACHE_NODE" ] && [ -f "$CACHE_NODE" ] && [ ! -L "$CACHE_NODE" ] || {
		log "converter node snapshot is missing or unsafe"
		return 1
	}
	size=$(wc -c < "$CACHE_NODE" 2>/dev/null)
	size=$(printf '%s' "$size" | tr -d '[:space:]')
	uint_between "$size" 1 33554432 || {
		log "converter node snapshot size is invalid"
		return 1
	}
	digest=$(sha256sum "$CACHE_NODE" 2>/dev/null) || return 1
	digest=${digest%% *}
	[ "$digest" = "$PINNED_AGGREGATE_SHA" ] || {
		log "converter node snapshot does not match the pinned subscription generation"
		return 1
	}
}

pin_converter_snapshot() {
	PINNED_GENERATION=$VALIDATED_GENERATION
	PINNED_AGGREGATE_SHA=$VALIDATED_AGGREGATE_SHA
	[ -n "$PINNED_GENERATION" ] && [ -n "$PINNED_AGGREGATE_SHA" ] || return 1
	acquire_subscription_lock || {
		log "cannot lock the subscription generation for output rendering" || true
		return 1
	}
	validate_pinned_generation &&
		validate_pinned_converter_snapshot || return 1
}

require_new_generation_after_refresh() {
	validate_current_generation "$CONFIG_DIGEST" || {
		log "refresh did not select a valid same-config subscription generation"
		return 1
	}
	[ -z "$GENERATION_BEFORE_REFRESH" ] ||
		[ "$VALIDATED_GENERATION" != "$GENERATION_BEFORE_REFRESH" ] || {
			log "refresh did not advance the subscription generation"
			return 1
		}
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

converter_running() {
	"$INIT" running >/dev/null 2>&1
}

ensure_converter_for_generation() {
	local i=0 now deadline
	health_ok && return 0
	if ! acquire_lifecycle_lock; then
		log "another service lifecycle operation is in progress; refusing to race it" || true
		return 1
	fi
	# The service may have changed while health was probed. Recheck only after
	# owning the same lock used by package-managed start/stop/restart operations.
	if health_ok; then
		release_lifecycle_lock
		return 0
	fi
	if converter_running; then
		release_lifecycle_lock
		log "converter is running but not healthy on port ${port}; waiting for it to become ready" || return 1
	else
		log "converter is not running/ready on port ${port}; starting it temporarily" || return 1
		if ! LF_LIFECYCLE_TOKEN="$LIFECYCLE_TOKEN" "$INIT" start manual >/dev/null 2>&1; then
			# procd may publish an instance and only then fail while closing the
			# transaction. The shared lifecycle lock proves this invocation owns
			# that partial start, so retain the lock and stop it during cleanup.
			if converter_running; then
				STARTED_CONVERTER=1
				log "converter start failed after publishing an instance; scheduling cleanup" || true
			else
				release_lifecycle_lock
				log "converter start failed" || true
			fi
			return 1
		fi
		# Ownership begins only after a successful start. A pre-existing running
		# service is never marked temporary and therefore is never stopped here.
		STARTED_CONVERTER=1
	fi
	now=$(date +%s)
	case "$now" in ''|*[!0-9]*) log "cannot read clock for converter startup wait" || true; return 1 ;; esac
	deadline=$((now + startup_wait))
	while [ "$i" -lt "$startup_wait" ]; do
		health_ok && return 0
		now=$(date +%s)
		case "$now" in ''|*[!0-9]*) log "cannot read clock for converter startup wait" || true; return 1 ;; esac
		[ "$now" -lt "$deadline" ] || break
		sleep 1
		i=$((i + 1))
	done
	log "converter did not become ready on port ${port} within ${startup_wait}s" || true
	return 1
}

fetch_config() {
	local output="$1"
	curl -fsS --connect-timeout 10 --max-time "$request_timeout" \
		"http://127.0.0.1:${port}/?password=${pass_q}&template=${template_q}" \
		-o "$output"
}

refresh_converter() {
	# 不用 curl -f: 那样会丢掉响应体和状态码, 失败时无从判断原因。
	# 这里显式取回 HTTP 状态码并把细节写进 update.log。
	local output="$WORK_DIR/refresh.json" errfile="$WORK_DIR/refresh.err" code detail
	rm -f "$output" "$errfile"
	code=$(curl -sS --connect-timeout 10 --max-time "$request_timeout" \
		-o "$output" -w '%{http_code}' \
		"http://127.0.0.1:${port}/refresh?password=${pass_q}" 2>"$errfile") || {
		detail=$(head -c 200 "$errfile" 2>/dev/null | tr -d '\r\n')
		log "refresh request failed: ${detail:-curl exited non-zero}" || true
		return 1
	}
	case "$code" in
		2??) return 0 ;;
	esac
	detail=$(head -c 200 "$output" 2>/dev/null | tr -d '\r\n')
	case "$code" in
		401)
			log "refresh rejected with HTTP 401: the converter password does not match the UCI password; run Save & Apply to regenerate /etc/liquid-formula/config.yaml" || true
			;;
		*)
			log "refresh rejected with HTTP ${code}: ${detail:-no response body}" || true
			;;
	esac
	return 1
}

synchronize_converter_snapshot() {
	local output="$WORK_DIR/barrier.json" errfile="$WORK_DIR/barrier.err"
	local code detail size digest

	activate_subscription_barrier || {
		log "cannot activate the converter snapshot synchronization barrier" || true
		return 1
	}
	rm -f "$output" "$errfile"
	code=$(curl -sS --connect-timeout 10 --max-time "$request_timeout" \
		-o "$output" -w '%{http_code}' \
		"http://127.0.0.1:${port}/?password=${pass_q}&template=%21liquid_formula_barrier&refresh=1" \
		2>"$errfile") || {
		detail=$(head -c 200 "$errfile" 2>/dev/null | tr -d '\r\n')
		log "snapshot synchronization request failed: ${detail:-curl exited non-zero}" || true
		return 1
	}
	# The frozen converter authenticates and runs query refresh through its
	# refreshManager before it validates the requested template. This printable,
	# permanently invalid ID therefore provides a side-effect-free manager fence:
	# the exact 400 response below is emitted only after that refresh returns.
	[ "$code" = 400 ] || {
		log "snapshot synchronization returned unexpected HTTP ${code}" || true
		return 1
	}
	size=$(wc -c < "$output" 2>/dev/null)
	size=$(printf '%s' "$size" | tr -d '[:space:]')
	[ "$size" = 77 ] || {
		log "snapshot synchronization acknowledgement size is invalid" || true
		return 1
	}
	digest=$(sha256sum "$output" 2>/dev/null) || return 1
	digest=${digest%% *}
	[ "$digest" = 40c11915012685b0c7bc8230a8499fc53d7fd1624227270f0a04ef5d68167aad ] || {
		log "snapshot synchronization acknowledgement content is invalid" || true
		return 1
	}
	validate_pinned_generation || return 1
	validate_pinned_converter_snapshot || return 1
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
	# 目录也必须拒绝。只挡符号链接的话, 目标已是同名目录时 mv 会把临时文件搬
	# 进那个目录里并返回成功 —— 于是 Update 报成功, 而期望的输出路径其实还是
	# 一个目录, 谁也拿不到生成的 JSON。
	[ ! -d "$output_config" ] || {
		log "refusing directory output path: $output_config"
		return 1
	}
	# 既不是普通文件也不是不存在(设备节点、FIFO 之类)同样不该写。
	if [ -e "$output_config" ] && [ ! -f "$output_config" ]; then
		log "output path is not a regular file: $output_config"
		return 1
	fi
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

	if [ -n "$FAULT_HOOK" ]; then
		"$FAULT_HOOK" before_final_output_rename || {
			log "fault injected before final output replacement" || true
			return 1
		}
	fi
	validate_pinned_generation || {
		log "subscription generation became invalid before output replacement" || true
		return 1
	}
	validate_pinned_converter_snapshot || return 1

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

# prepare_log 必须排在 acquire_lock 前面: 抢锁失败是最常见的失败原因,
# 而它只有 plain_error 一条出路。日志没就绪的话这些消息会彻底消失。
prepare_log || {
	plain_error "cannot prepare $LOG"
	exit 1
}
acquire_lock || exit $?
mkdir -p "$TMP_ROOT" || {
	plain_error "cannot create temporary root $TMP_ROOT"
	exit 1
}
WORK_DIR=$(mktemp -d "$TMP_ROOT/liquid-formula-update.XXXXXX") || {
	plain_error "cannot create updater working directory"
	exit 1
}
load_cfg || exit 1

case "$cmd" in
	generate)
		SBF_INCLUDE_DISABLED_URLS=0 "$GEN" >/dev/null || {
			log "converter config generation failed" || true
			exit 1
		}
		log "generated converter config" || exit 1
		;;
	refresh)
		health_ok || {
			log "cannot refresh because the converter is not running and healthy (check: curl -sS http://127.0.0.1:${port}/health)" || true
			exit 1
		}
		observe_generation_before_refresh || exit 1
		refresh_converter || {
			log "refresh failed" || true
			exit 1
		}
		require_new_generation_after_refresh || exit 1
		pin_converter_snapshot || exit 1
		synchronize_converter_snapshot || exit 1
		release_subscription_lock || {
			log "failed to release the subscription snapshot binding" || true
			exit 1
		}
		log "refresh ok" || exit 1
		;;
	check|apply)
		SBF_INCLUDE_DISABLED_URLS=1 "$GEN" >/dev/null || {
			log "converter config generation failed" || true
			exit 1
		}
		[ "$SERVICE_ENABLED" = 1 ] || RESTORE_AT_REST_CONFIG=1
		observe_generation_before_refresh || exit 1
		ensure_converter_for_generation || {
			log "cannot reach converter; enable/start it and check the subscription URL" || true
			exit 1
		}
		refresh_converter || {
			log "refresh failed" || true
			exit 1
		}
		require_new_generation_after_refresh || exit 1
		pin_converter_snapshot || exit 1
		synchronize_converter_snapshot || exit 1
		GENERATED="$WORK_DIR/generated.json"
		fetch_config "$GENERATED" || {
			log "failed to fetch generated config" || true
			exit 1
		}
		validate_generated "$GENERATED" || exit 1
		validate_pinned_generation || {
			log "subscription generation became invalid after output download" || true
			exit 1
		}
		validate_pinned_converter_snapshot || exit 1
		if [ "$cmd" = check ]; then
			log "check ok" || exit 1
		else
			install_output "$GENERATED" || exit 1
		fi
		release_subscription_lock || {
			log "failed to release the subscription snapshot binding" || true
			exit 1
		}
		;;
esac
