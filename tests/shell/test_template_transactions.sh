#!/bin/sh
set -u

REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$REPO_ROOT/tests/shell/harness.sh"
RPC="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula"
UI="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js"

assert_contains "$RPC" 'template.lock' 'template writes use a separate atomic lock'
assert_contains "$RPC" 'mktemp "\$TPL_DIR/\.template.new' 'template staging shares the destination filesystem for atomic rename'
assert_contains "$RPC" '1048576' 'RPC enforces the 1 MiB template limit'
assert_contains "$RPC" 'valid_id' 'template IDs are strictly validated before path access'
assert_contains "$RPC" 'valid_file' 'template filenames are strictly validated before path access'
assert_contains "$RPC" 'template id and file are immutable' 'editing cannot change persistent identity'
assert_contains "$RPC" 'cannot disable current default_template' 'default template cannot be disabled'
assert_contains "$RPC" 'cannot delete current default_template' 'default template cannot be deleted'
# 快照与恢复都收敛到单个 section。整份 export/import 会把另一个会话并发提交的
# 端口、密码一起回退, 而模板锁根本拦不住 Overview 的保存。
assert_contains "$RPC" 'uci -q show "\$section"' 'transaction snapshots only its own template section'
assert_contains "$RPC" 'template_section_restore "\$id"' 'transaction restores only its own template section'
assert_not_contains "$RPC" '^[[:space:]]*uci export' 'transaction never exports the whole config'
assert_contains "$RPC" 'template_hex_encode' 'snapshot stores raw UCI values without parsing shell-style quotes'
assert_contains "$RPC" 'rollback was incomplete and recovery artifacts were retained' 'incomplete rollback retains its recovery material'
assert_contains "$RPC" 'cannot back up template' 'delete refuses to mutate UCI after a failed file backup'
assert_contains "$RPC" 'template_reply_error rollback' 'transaction reports rollback failures by phase'
assert_contains "$RPC" 'phase.*complete' 'successful transaction reports the complete phase'
assert_contains "$UI" 'TextEncoder.*1048576|1048576' 'browser enforces the UTF-8 1 MiB limit'
assert_contains "$UI" 'readOnly = true' 'editing locks template ID and filename'
assert_contains "$UI" "res.phase !== 'complete'" 'browser fails closed on incomplete transactions'

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
MOCK_BIN="$TMP/bin"
MOCK_STORE="$TMP/uci"
MOCK_TPL="$TMP/templates"
MOCK_STATE="$TMP/state"
MOCK_FUNCTIONS="$TMP/functions.sh"
MOCK_GENERATOR="$TMP/generator"
MOCK_VALIDATOR="$TMP/validator"
MOCK_INIT="$TMP/init"
MOCK_PROCESS_START="$TMP/process-start"
mkdir -p "$MOCK_BIN" "$MOCK_STORE" "$MOCK_TPL" "$MOCK_STATE"

cat > "$MOCK_FUNCTIONS" <<'EOF'
config_load() {
	return 0
}

config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}" value
	if [ -f "$MOCK_UCI_STORE/liquid_formula.$section.$option" ]; then
		IFS= read -r value < "$MOCK_UCI_STORE/liquid_formula.$section.$option" || value=
	else
		value=$default
	fi
	eval "$destination=\$value"
}

config_get_bool() {
	config_get "$@"
}

config_list_foreach() {
	local section="$1" option="$2" callback="$3" path value
	for path in "$MOCK_UCI_STORE/liquid_formula.$section.$option".__list.*; do
		[ -f "$path" ] || continue
		IFS= read -r value < "$path" || value=
		"$callback" "$value" || return $?
	done
}

config_foreach() {
	local callback="$1" type="$2" path key section
	[ "$type" = template ] || return 0
	for path in "$MOCK_UCI_STORE"/liquid_formula.*; do
		[ -f "$path" ] || continue
		key=${path##*/}
		section=${key#liquid_formula.}
		case "$section" in *.*|main) continue ;; esac
		[ "$(cat "$path")" = template ] || continue
		"$callback" "$section" || return $?
	done
}
EOF

cat > "$MOCK_BIN/uci" <<'EOF'
#!/bin/sh
[ "${1-}" != -q ] || shift
command=${1-}
[ "$#" -eq 0 ] || shift
case "$command" in
	get)
		[ -f "$MOCK_UCI_STORE/$1" ] || exit 1
		cat "$MOCK_UCI_STORE/$1"
		;;
	show)
		section=$1
		found=0
		for path in "$MOCK_UCI_STORE/$section" "$MOCK_UCI_STORE/$section".*; do
			[ -f "$path" ] || continue
			found=1
			key=${path##*/}
			case "$key" in *.__list.*) key=${key%%.__list.*} ;; esac
			value=$(cat "$path")
			escaped=$(printf '%s' "$value" | sed "s/'/'\\\\''/g")
			printf "%s='%s'\n" "$key" "$escaped"
		done
		[ "$found" = 1 ]
		;;
	set)
		assignment=$1
		key=${assignment%%=*}
		value=${assignment#*=}
		if [ -n "${MOCK_UCI_FAIL_TYPE_SECTION:-}" ] &&
			[ "$key" = "liquid_formula.$MOCK_UCI_FAIL_TYPE_SECTION" ]; then
			exit 74
		fi
		if [ "${MOCK_UCI_FAIL_RESTORE:-0}" = 1 ] &&
			[ -e "$MOCK_GENERATOR_FAILED" ] &&
			[ "$key" = liquid_formula.old.name ]; then
			exit 74
		fi
		rm -f "$MOCK_UCI_STORE/$key".__list.*
		printf '%s\n' "$value" > "$MOCK_UCI_STORE/$key"
		;;
	add_list)
		assignment=$1
		key=${assignment%%=*}
		value=${assignment#*=}
		rm -f "$MOCK_UCI_STORE/$key"
		index=1
		while [ -e "$MOCK_UCI_STORE/$key.__list.$index" ]; do index=$((index + 1)); done
		printf '%s\n' "$value" > "$MOCK_UCI_STORE/$key.__list.$index"
		;;
	delete)
		rm -f "$MOCK_UCI_STORE/$1" "$MOCK_UCI_STORE/$1".*
		;;
	commit|revert)
		exit 0
		;;
	*) exit 2 ;;
esac
EOF

cat > "$MOCK_BIN/jsonfilter" <<'EOF'
#!/bin/sh
[ "$1" = -e ] || exit 2
python3 -c '
import json, sys
value = json.load(sys.stdin).get(sys.argv[1][2:], "")
if value is True:
    print("true")
elif value is False:
    print("false")
else:
    print(value)
' "$2"
EOF

SYSTEM_CP=$(command -v cp)
SYSTEM_RM=$(command -v rm)
export SYSTEM_CP SYSTEM_RM
cat > "$MOCK_BIN/cp" <<'EOF'
#!/bin/sh
last=
for argument do last=$argument; done
case "$last" in
	"$MOCK_STATE"/.template.delete.*)
		[ "${MOCK_CP_FAIL_DELETE_BACKUP:-0}" != 1 ] || exit 74
		;;
esac
exec "$SYSTEM_CP" "$@"
EOF

cat > "$MOCK_BIN/rm" <<'EOF'
#!/bin/sh
last=
for argument do last=$argument; done
if [ "${MOCK_RM_FAIL_NEW_ROLLBACK:-0}" = 1 ] &&
	[ -e "$MOCK_GENERATOR_FAILED" ] &&
	[ "$last" = "$MOCK_TPL/new.json" ]; then
	exit 74
fi
exec "$SYSTEM_RM" "$@"
EOF

cat > "$MOCK_GENERATOR" <<'EOF'
#!/bin/sh
count=0
[ ! -f "$MOCK_GENERATOR_COUNT" ] || IFS= read -r count < "$MOCK_GENERATOR_COUNT"
count=$((count + 1))
printf '%s\n' "$count" > "$MOCK_GENERATOR_COUNT"
if [ "${MOCK_GENERATOR_FAIL_ON:-0}" = "$count" ]; then
	: > "$MOCK_GENERATOR_FAILED"
	exit 74
fi
exit 0
EOF

cat > "$MOCK_VALIDATOR" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$MOCK_INIT" <<'EOF'
#!/bin/sh
[ "$1" = running ] && exit 1
exit 0
EOF
cat > "$MOCK_PROCESS_START" <<'EOF'
#!/bin/sh
printf '%s\n' "$1"
EOF
chmod 0755 "$MOCK_BIN/uci" "$MOCK_BIN/jsonfilter" "$MOCK_BIN/cp" "$MOCK_BIN/rm" \
	"$MOCK_GENERATOR" "$MOCK_VALIDATOR" "$MOCK_INIT" "$MOCK_PROCESS_START"

export PATH="$MOCK_BIN:$PATH"
export MOCK_UCI_STORE="$MOCK_STORE"
export MOCK_STATE MOCK_TPL
export MOCK_GENERATOR_COUNT="$TMP/generator.count"
export MOCK_GENERATOR_FAILED="$TMP/generator.failed"
export SBF_FUNCTIONS_SH="$MOCK_FUNCTIONS"
export SBF_TEMPLATE_DIR="$MOCK_TPL"
export SBF_STATE_DIR="$MOCK_STATE"
export SBF_GENERATOR="$MOCK_GENERATOR"
export SBF_TEMPLATE_VALIDATOR="$MOCK_VALIDATOR"
export SBF_INIT_SCRIPT="$MOCK_INIT"
export SBF_PROCESS_START_HELPER="$MOCK_PROCESS_START"

seed_old_template() {
	rm -rf "$MOCK_STORE" "$MOCK_TPL" "$MOCK_STATE"
	mkdir -p "$MOCK_STORE" "$MOCK_TPL" "$MOCK_STATE"
	printf 'template\n' > "$MOCK_STORE/liquid_formula.old"
	printf '1\n' > "$MOCK_STORE/liquid_formula.old.enabled"
	printf "Old router's template\n" > "$MOCK_STORE/liquid_formula.old.name"
	printf 'old.json\n' > "$MOCK_STORE/liquid_formula.old.file"
	printf 'Direct\n' > "$MOCK_STORE/liquid_formula.old.no_node"
	printf 'first item\n' > "$MOCK_STORE/liquid_formula.old.extra_values.__list.1"
	printf 'second item with spaces\n' > "$MOCK_STORE/liquid_formula.old.extra_values.__list.2"
	printf 'other\n' > "$MOCK_STORE/liquid_formula.main.default_template"
	printf 'old-content\n' > "$MOCK_TPL/old.json"
	rm -f "$MOCK_GENERATOR_COUNT" "$MOCK_GENERATOR_FAILED"
	unset MOCK_UCI_FAIL_TYPE_SECTION MOCK_UCI_FAIL_RESTORE MOCK_CP_FAIL_DELETE_BACKUP MOCK_RM_FAIL_NEW_ROLLBACK 2>/dev/null || true
	export MOCK_GENERATOR_FAIL_ON=0
}

run_template_rpc() {
	method=$1
	payload=$2
	printf '%s' "$payload" |
		"$RPC" call "$method" > "$TMP/rpc.out" 2> "$TMP/rpc.err"
	RPC_RC=$?
}

# uci show shell-quotes apostrophes as '\''; parsing and trimming those quotes
# corrupts the restored value. A failed transaction must recover the exact raw UCI value.
seed_old_template
export MOCK_GENERATOR_FAIL_ON=1
run_template_rpc write_template '{"id":"old","name":"New name","file":"old.json","no_node":"Direct","enabled":true,"content":"new-content"}'
if [ "$RPC_RC" -ne 0 ]; then record_ok "write failure enters rollback"; else record_failure "write failure enters rollback"; fi
assert_file_content "Old router's template" "$MOCK_STORE/liquid_formula.old.name" "rollback preserves a UCI value containing a single quote"
assert_file_content old-content "$MOCK_TPL/old.json" "write rollback restores the previous template file"
assert_file_not_exists "$MOCK_STORE/liquid_formula.old.extra_values" "list rollback does not collapse an extension list into a scalar"
assert_file_content 'first item' "$MOCK_STORE/liquid_formula.old.extra_values.__list.1" "list rollback preserves the first item"
assert_file_content 'second item with spaces' "$MOCK_STORE/liquid_formula.old.extra_values.__list.2" "list rollback preserves item order and embedded spaces"
assert_contains "$TMP/rpc.out" 'failed and was rolled back' "complete rollback is reported accurately"

# Creating the section type is part of the transaction. If that first UCI write fails,
# later option writes must not turn a malformed section into an apparent success.
seed_old_template
export MOCK_UCI_FAIL_TYPE_SECTION=new
run_template_rpc write_template '{"id":"new","name":"New","file":"new.json","no_node":"Direct","enabled":true,"content":"new-content"}'
if [ "$RPC_RC" -ne 0 ]; then record_ok "new section type failure aborts the transaction"; else record_failure "new section type failure aborts the transaction"; fi
assert_file_not_exists "$MOCK_STORE/liquid_formula.new" "failed section creation leaves no UCI section"
assert_file_not_exists "$MOCK_TPL/new.json" "failed section creation removes the installed template file"

# When a new template had no old file, successful removal is part of rollback.
# A failed rm must retain the UCI absence snapshot and report incomplete recovery.
seed_old_template
export MOCK_GENERATOR_FAIL_ON=1 MOCK_RM_FAIL_NEW_ROLLBACK=1
run_template_rpc write_template '{"id":"new","name":"New","file":"new.json","no_node":"Direct","enabled":true,"content":"new-content"}'
assert_contains "$TMP/rpc.out" 'rollback was incomplete and recovery artifacts were retained' "failed removal of a new template makes rollback incomplete"
assert_file_exists "$MOCK_TPL/new.json" "failed rollback removal leaves the unrecovered new file visible"
set -- "$MOCK_STATE"/.template.uci.*
if [ -f "$1" ]; then record_ok "failed new-file removal retains the absence snapshot"; else record_failure "failed new-file removal retains the absence snapshot"; fi

# A delete without a readable backup is not rollback-capable and therefore must stop
# before touching UCI or invoking the generator.
seed_old_template
export MOCK_CP_FAIL_DELETE_BACKUP=1
run_template_rpc delete_template '{"id":"old"}'
if [ "$RPC_RC" -ne 0 ]; then record_ok "delete refuses to continue after backup failure"; else record_failure "delete refuses to continue after backup failure"; fi
assert_contains "$TMP/rpc.out" '"phase":"snapshot"' "delete backup failure is identified as a snapshot failure"
assert_file_exists "$MOCK_STORE/liquid_formula.old" "delete backup failure leaves UCI untouched"
assert_file_content old-content "$MOCK_TPL/old.json" "delete backup failure leaves the template untouched"
assert_file_not_exists "$MOCK_GENERATOR_COUNT" "delete backup failure never invokes the generator"

# If UCI restoration itself fails, the response must not claim rollback and the only
# recovery material must remain available to an administrator.
seed_old_template
export MOCK_GENERATOR_FAIL_ON=1 MOCK_UCI_FAIL_RESTORE=1
run_template_rpc write_template '{"id":"old","name":"New name","file":"old.json","no_node":"Direct","enabled":true,"content":"new-content"}'
assert_contains "$TMP/rpc.out" 'rollback was incomplete and recovery artifacts were retained' "incomplete rollback is reported explicitly"
set -- "$MOCK_STATE"/.template.uci.*
if [ -f "$1" ]; then record_ok "incomplete rollback retains its UCI snapshot"; else record_failure "incomplete rollback retains its UCI snapshot"; fi
set -- "$MOCK_STATE"/.template.backup.*
if [ -f "$1" ]; then record_ok "incomplete rollback retains its file backup"; else record_failure "incomplete rollback retains its file backup"; fi

finish_tests
