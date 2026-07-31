#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

GEN="$REPO_ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula/generate-config.sh"
VALIDATE="$REPO_ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula/validate-template.sh"
UCI_DEFAULTS="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/config/liquid_formula"
YAML_EXAMPLE="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/liquid-formula/config.yaml.example"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-generate-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_FUNCTIONS="$TEST_TMP/functions.sh"
MOCK_BIN="$TEST_TMP/bin"
RUNTIME="$TEST_TMP/runtime"
mkdir -p "$MOCK_BIN" "$RUNTIME"
SYSTEM_CAT=$(command -v cat)
SYSTEM_CMP=$(command -v cmp)
export SYSTEM_CAT SYSTEM_CMP

cat > "$MOCK_FUNCTIONS" <<'EOF'
config_load() {
	[ "${MOCK_CONFIG_LOAD_FAIL:-0}" != 1 ]
}

config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}"
	local key="UCI_${section}_${option}" is_set __cg_value
	eval "is_set=\${$key+x}"
	if [ "$is_set" = x ]; then
		eval "__cg_value=\${$key}"
	else
		__cg_value=$default
	fi
	eval "$destination=\$__cg_value"
}

config_get_bool() {
	local destination="$1" section="$2" option="$3" default="${4-0}" __cgb_value
	config_get __cgb_value "$section" "$option" "$default"
	case "$__cgb_value" in
		1|true|yes|on|enabled) __cgb_value=1 ;;
		*) __cgb_value=0 ;;
	esac
	eval "$destination=\$__cgb_value"
}

config_foreach() {
	local callback="$1" type="$2" section count=0
	[ "$type" = template ] || return 0
	if [ -n "${MOCK_FOREACH_COUNT_FILE:-}" ]; then
		[ -f "$MOCK_FOREACH_COUNT_FILE" ] && read -r count < "$MOCK_FOREACH_COUNT_FILE"
		count=$((count + 1))
		printf '%s\n' "$count" > "$MOCK_FOREACH_COUNT_FILE"
		[ "${MOCK_FOREACH_FAIL_ON:-0}" != "$count" ] || return 74
	fi
	for section in ${UCI_TEMPLATE_IDS:-}; do
		"$callback" "$section" || return $?
	done
	if [ -n "${MOCK_ENABLED_TEMPLATE_COUNT_OVERRIDE:-}" ]; then
		ENABLED_TEMPLATE_COUNT=$MOCK_ENABLED_TEMPLATE_COUNT_OVERRIDE
	fi
}

config_list_foreach() {
	local section="$1" option="$2" callback="$3" list_file value count=0
	if [ -n "${MOCK_LIST_FOREACH_COUNT_FILE:-}" ]; then
		[ -f "$MOCK_LIST_FOREACH_COUNT_FILE" ] && read -r count < "$MOCK_LIST_FOREACH_COUNT_FILE"
		count=$((count + 1))
		printf '%s\n' "$count" > "$MOCK_LIST_FOREACH_COUNT_FILE"
	fi
	eval "list_file=\${UCI_${section}_${option}_LIST_FILE-}"
	[ -n "$list_file" ] && [ -f "$list_file" ] || return 0
	while IFS= read -r value || [ -n "$value" ]; do
		"$callback" "$value" || return $?
	done < "$list_file"
}
EOF

cat > "$MOCK_BIN/cat" <<'EOF'
#!/bin/sh
count=0
if [ -n "${MOCK_CAT_COUNT_FILE:-}" ]; then
	[ -f "$MOCK_CAT_COUNT_FILE" ] && read -r count < "$MOCK_CAT_COUNT_FILE"
	count=$((count + 1))
	printf '%s\n' "$count" > "$MOCK_CAT_COUNT_FILE"
	[ "${MOCK_CAT_FAIL_ON:-0}" != "$count" ] || exit 74
fi
exec "$SYSTEM_CAT" "$@"
EOF

cat > "$MOCK_BIN/cmp" <<'EOF'
#!/bin/sh
[ "${MOCK_CMP_FAIL:-0}" != 1 ] || exit 2
exec "$SYSTEM_CMP" "$@"
EOF

cat > "$MOCK_BIN/jsonfilter" <<'EOF'
#!/bin/sh
file=
while [ "$#" -gt 0 ]; do
	case "$1" in
		-i) file=$2; shift 2 ;;
		-e) shift 2 ;;
		*) shift ;;
	esac
done
[ -n "$file" ] && grep -q '"outbounds"' "$file" && ! grep -q 'INVALID' "$file"
EOF

FAULT_HOOK="$MOCK_BIN/fault-hook"
cat > "$FAULT_HOOK" <<'EOF'
#!/bin/sh
set -u

stage=${1:-}
staging_count=0
staging_mode=missing
for staging_file in "$(dirname "$SBF_CONFIG_OUT")"/."${SBF_CONFIG_OUT##*/}".*; do
	[ -e "$staging_file" ] || continue
	staging_count=$((staging_count + 1))
	staging_mode=$(stat -c %a "$staging_file" 2>/dev/null || printf unreadable)
done
printf '%s|%s|%s\n' "$stage" "$staging_count" "$staging_mode" >> "$MOCK_FAULT_CALLS"
[ "$stage" != "${MOCK_FAIL_FAULT_STAGE:-}" ]
EOF
chmod 0755 "$MOCK_BIN/cat" "$MOCK_BIN/cmp" "$MOCK_BIN/jsonfilter" "$FAULT_HOOK"

export SBF_FUNCTIONS_SH="$MOCK_FUNCTIONS"
export SBF_CONFIG_OUT="$RUNTIME/config.yaml"
export SBF_TMP_ROOT="$RUNTIME/tmp"
export SBF_GEN_LOCK="$RUNTIME/generate.lock"
export PATH="$MOCK_BIN:$PATH"

reset_config() {
	rm -rf "$RUNTIME"
	mkdir -p "$RUNTIME/tmp"
	export UCI_main_enabled=0
	export UCI_main_boot_delay=90
	export UCI_main_port=9716
	export UCI_main_password=890716
	unset UCI_main_subscription_url
	export UCI_main_subscription_url_LIST_FILE="$RUNTIME/subscription-url.list"
	export UCI_main_subscription_timeout=60
	export UCI_main_refresh_interval=360
	export UCI_main_user_agent='sing-box 1.11.0'
	export UCI_main_default_template=momo_template
	export UCI_main_cache_dir="$RUNTIME/cache"
	export UCI_main_log_file="$RUNTIME/server.log"
	export UCI_main_output_config=/etc/momo/profiles/config.json
	export UCI_main_template_base_url=http://127.0.0.1/liquid-formula/templates
	export UCI_TEMPLATE_IDS=momo_template
	export UCI_momo_template_enabled=1
	export UCI_momo_template_name='Momo Template'
	export UCI_momo_template_file=momo-template.json
	export UCI_momo_template_no_node='➜ Direct'
	export MOCK_CONFIG_LOAD_FAIL=0
	export MOCK_CMP_FAIL=0
	export MOCK_CAT_FAIL_ON=0
	export MOCK_FOREACH_FAIL_ON=0
	unset MOCK_ENABLED_TEMPLATE_COUNT_OVERRIDE
	unset MOCK_FAIL_FAULT_STAGE
	unset SBF_TEST_FAULT_HOOK
	export MOCK_CAT_COUNT_FILE="$RUNTIME/cat.count"
	export MOCK_FOREACH_COUNT_FILE="$RUNTIME/foreach.count"
	export MOCK_LIST_FOREACH_COUNT_FILE="$RUNTIME/list-foreach.count"
	export MOCK_FAULT_CALLS="$RUNTIME/fault.calls"
	: > "$MOCK_FAULT_CALLS"
	: > "$UCI_main_subscription_url_LIST_FILE"
}

run_generator() {
	"$GEN" >"$TEST_TMP/generator.stdout" 2>"$TEST_TMP/generator.stderr"
	GEN_RC=$?
}

expect_generator_success() {
	description=$1
	run_generator
	if [ "$GEN_RC" -eq 0 ]; then
		record_ok "$description"
	else
		record_failure "$description (exit $GEN_RC: $(cat "$TEST_TMP/generator.stderr"))"
	fi
}

expect_generator_failure() {
	description=$1
	run_generator
	if [ "$GEN_RC" -ne 0 ]; then
		record_ok "$description"
	else
		record_failure "$description (unexpected success)"
	fi
}

expect_list_generator_failure() {
	description=$1
	rm -f "$MOCK_LIST_FOREACH_COUNT_FILE"
	run_generator
	if [ "$GEN_RC" -ne 0 ] && [ -s "$MOCK_LIST_FOREACH_COUNT_FILE" ]; then
		record_ok "$description"
	elif [ "$GEN_RC" -eq 0 ]; then
		record_failure "$description (unexpected success)"
	else
		record_failure "$description (failed before reading the subscription_url list: $(cat "$TEST_TMP/generator.stderr"))"
	fi
}

assert_fixed() {
	file=$1
	needle=$2
	description=$3
	if [ -f "$file" ] && grep -Fq -- "$needle" "$file"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

assert_uci_template_enabled() {
	section=$1
	description=$2
	if awk -v wanted="config template '$section'" '
		$0 == wanted { in_section = 1; next }
		in_section && /^[[:space:]]*config[[:space:]]/ { exit }
		in_section && /^[[:space:]]*option[[:space:]]+enabled[[:space:]]+\0471\047[[:space:]]*$/ { enabled = 1 }
		END { exit(enabled ? 0 : 1) }
	' "$UCI_DEFAULTS"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

set_dynamic() {
	variable=$1
	value=$2
	eval "export $variable=\$value"
}

assert_invalid_preserves_output() {
	variable=$1
	value=$2
	description=$3
	reset_config
	printf 'last-known-good\n' > "$SBF_CONFIG_OUT"
	set_dynamic "$variable" "$value"
	expect_generator_failure "$description"
	assert_file_content 'last-known-good' "$SBF_CONFIG_OUT" "$description preserves the old config"
}

set_subscription_urls() {
	: > "$UCI_main_subscription_url_LIST_FILE"
	for _subscription_url in "$@"; do
		printf '%s\n' "$_subscription_url" >> "$UCI_main_subscription_url_LIST_FILE"
	done
}

assert_gateway_urls_in_order() {
	_file=$1
	_description=$2
	shift 2
	_actual="$TEST_TMP/gateway-urls.actual"
	_expected="$TEST_TMP/gateway-urls.expected"
	: > "$_actual"
	: > "$_expected"
	if [ "$#" -eq 0 ]; then
		printf '%s\n' '  urls: []' > "$_expected"
	else
		printf '%s\n' '  urls:' > "$_expected"
		printf '%s\n' "$@" >> "$_expected"
	fi
	if [ ! -f "$_file" ]; then
		record_failure "$_description (missing: $_file)"
		return
	fi
	awk '
		$0 == "liquid_formula_gateway:" {
			gateway_count++
			in_gateway = 1
			in_urls = 0
			next
		}
		in_gateway && /^[^[:space:]]/ {
			in_gateway = 0
			in_urls = 0
		}
		!in_gateway { next }
		$0 == "  urls:" || $0 == "  urls: []" {
			urls_count++
			if (urls_count == 1) {
				print
				in_urls = ($0 == "  urls:")
			}
			next
		}
		in_urls && /^    - / {
			if (urls_count == 1)
				print
			next
		}
		in_urls {
			in_urls = 0
		}
		END {
			if (gateway_count != 1 || urls_count != 1)
				exit 1
		}
	' "$_file" > "$_actual"
	_extract_rc=$?
	if [ "$_extract_rc" -eq 0 ] && cmp -s "$_expected" "$_actual"; then
		record_ok "$_description"
	else
		record_failure "$_description"
		if [ -f "$_actual" ]; then
			diff -u "$_expected" "$_actual" >&2 || true
		fi
	fi
}

assert_gateway_block() {
	_file=$1
	_expected=$2
	_description=$3
	_actual="$TEST_TMP/gateway-block.actual"
	if [ ! -f "$_file" ]; then
		record_failure "$_description (missing: $_file)"
		return
	fi
	awk '
		$0 == "liquid_formula_gateway:" {
			count++
			if (count == 1) {
				in_gateway = 1
				print
			}
			next
		}
		in_gateway && /^[^[:space:]]/ {
			in_gateway = 0
		}
		in_gateway && count == 1 {
			print
		}
		END {
			if (count != 1)
				exit 1
		}
	' "$_file" > "$_actual"
	_extract_rc=$?
	if [ "$_extract_rc" -eq 0 ] && [ "$(cat "$_actual")" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description"
		printf '%s\n' "$_expected" > "$TEST_TMP/gateway-block.expected"
		diff -u "$TEST_TMP/gateway-block.expected" "$_actual" >&2 || true
	fi
}

configure_enabled_templates() {
	_count=$1
	UCI_TEMPLATE_IDS=momo_template
	_index=2
	while [ "$_index" -le "$_count" ]; do
		_id="template_$_index"
		UCI_TEMPLATE_IDS="$UCI_TEMPLATE_IDS $_id"
		set_dynamic "UCI_${_id}_enabled" 1
		set_dynamic "UCI_${_id}_name" "Template $_index"
		set_dynamic "UCI_${_id}_file" "template-$_index.json"
		set_dynamic "UCI_${_id}_no_node" Direct
		_index=$((_index + 1))
	done
	export UCI_TEMPLATE_IDS
}

set_numbered_subscription_urls() {
	_count=$1
	: > "$UCI_main_subscription_url_LIST_FILE"
	_index=1
	while [ "$_index" -le "$_count" ]; do
		printf 'https://source-%s.example/sub?occurrence=%s\n' "$_index" "$_index" >> "$UCI_main_subscription_url_LIST_FILE"
		_index=$((_index + 1))
	done
}

assert_no_subscription_url_default() {
	if grep -Eq '^[[:space:]]*(option|list)[[:space:]]+subscription_url([[:space:]]|$)' "$UCI_DEFAULTS"; then
		record_failure "new installs omit subscription_url when no URLs are configured"
	else
		record_ok "new installs omit subscription_url when no URLs are configured"
	fi
}

reset_config
expect_generator_success "generates a valid disabled configuration"
assert_file_exists "$SBF_CONFIG_OUT" "creates config.yaml"
assert_equal 600 "$(stat -c %a "$SBF_CONFIG_OUT" 2>/dev/null)" "writes config.yaml with mode 0600"
assert_fixed "$SBF_CONFIG_OUT" "  password: '890716'" "preserves the intentional default password"
assert_fixed "$SBF_CONFIG_OUT" "  url: 'http://127.0.0.1:9717/v1/aggregate'" "routes the converter through the exact loopback aggregate endpoint while disabled"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" "emits an explicit empty gateway URL list while disabled"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 120" "uses A=max(S,1)*T+60 for the disabled converter subscription"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 240" "uses R=A+E*T+60 for the disabled server write timeout"
assert_gateway_block "$SBF_CONFIG_OUT" "liquid_formula_gateway:
  listen_address: '127.0.0.1'
  listen_port: 9717
  source_timeout: 60
  aggregate_timeout: 120
  user_agent: 'sing-box 1.11.0'
  urls: []" "emits the complete disabled gateway block with no unknown keys"
assert_contains "$UCI_DEFAULTS" "option[[:space:]]+password[[:space:]]+'890716'" "keeps the intentional UCI default password"
assert_contains "$UCI_DEFAULTS" "option[[:space:]]+subscription_timeout[[:space:]]+'60'" "ships the bounded subscription timeout default"
assert_no_subscription_url_default
assert_contains "$YAML_EXAMPLE" 'write_timeout:[[:space:]]+240' "keeps the example server timeout aligned with the default R budget"
assert_fixed "$YAML_EXAMPLE" "  url: 'http://127.0.0.1:9717/v1/aggregate'" "routes the example converter subscription through the exact aggregate endpoint"
assert_fixed "$YAML_EXAMPLE" "  timeout: 120" "keeps the example converter timeout aligned with the default A budget"
assert_gateway_block "$YAML_EXAMPLE" "liquid_formula_gateway:
  listen_address: '127.0.0.1'
  listen_port: 9717
  source_timeout: 60
  aggregate_timeout: 120
  user_agent: 'sing-box 1.11.0'
  urls: []" "ships a complete disabled gateway block in the example"
assert_contains "$UCI_DEFAULTS" "option[[:space:]]+default_template[[:space:]]+'momo_template'" "keeps momo as the new-install default template"
assert_uci_template_enabled momo_template "enables momo in the new-install template sections"
assert_uci_template_enabled localdns_template "enables local DNS in the new-install template sections"

touch -d '@1000000000' "$SBF_CONFIG_OUT"
before_identity=$(stat -c '%i:%Y' "$SBF_CONFIG_OUT")
expect_generator_success "regenerates identical configuration successfully"
after_identity=$(stat -c '%i:%Y' "$SBF_CONFIG_OUT")
assert_equal "$before_identity" "$after_identity" "identical generation preserves inode and mtime"

old_inode=$(stat -c %i "$SBF_CONFIG_OUT")
export UCI_main_password="router's password"
expect_generator_success "atomically replaces changed configuration"
new_inode=$(stat -c %i "$SBF_CONFIG_OUT")
assert_not_equal "$old_inode" "$new_inode" "changed generation installs a new inode"
assert_fixed "$SBF_CONFIG_OUT" "password: 'router''s password'" "escapes YAML single quotes"
leftovers=$(find "$RUNTIME" -maxdepth 1 -name '.config.yaml.*' -print)
assert_empty "$leftovers" "leaves no config staging file"

reset_config
expect_generator_success "creates a complete config before a rename fault"
fault_baseline_identity=$(stat -c '%i:%Y' "$SBF_CONFIG_OUT")
fault_baseline_sha=$(sha256sum "$SBF_CONFIG_OUT")
export UCI_main_password=changed-before-rename
export SBF_TEST_FAULT_HOOK="$FAULT_HOOK"
export MOCK_FAIL_FAULT_STAGE=before_config_rename
expect_generator_failure "propagates the before_config_rename fault"
assert_equal "$fault_baseline_identity" "$(stat -c '%i:%Y' "$SBF_CONFIG_OUT")" "the config rename fault preserves the previous inode and mtime"
assert_equal "$fault_baseline_sha" "$(sha256sum "$SBF_CONFIG_OUT")" "the config rename fault preserves the previous complete bytes"
assert_equal 'before_config_rename|1|600' "$(cat "$MOCK_FAULT_CALLS")" "the sole allowed config hook sees one private mode-0600 staging file"
fault_leftovers=$(find "$RUNTIME" -maxdepth 1 -name '.config.yaml.*' -print)
assert_empty "$fault_leftovers" "the config rename fault cleans its staging file"
unset SBF_TEST_FAULT_HOOK MOCK_FAIL_FAULT_STAGE

reset_config
export UCI_main_port=1 UCI_main_subscription_timeout=5 UCI_main_refresh_interval=1 UCI_main_boot_delay=0
expect_generator_success "accepts every lower numeric boundary"
assert_fixed "$SBF_CONFIG_OUT" "  url: 'http://127.0.0.1:2/v1/aggregate'" "derives gateway port 2 from converter port 1"
assert_fixed "$SBF_CONFIG_OUT" "  listen_port: 2" "keeps the lower-bound gateway block port in agreement"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 65" "derives the lower A budget exactly"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 130" "derives the lower R budget exactly"

reset_config
export UCI_main_port=65535 UCI_main_subscription_timeout=600 UCI_main_refresh_interval=10080 UCI_main_boot_delay=600
expect_generator_success "accepts every upper numeric boundary"
assert_fixed "$SBF_CONFIG_OUT" "  url: 'http://127.0.0.1:65534/v1/aggregate'" "maps converter port 65535 to gateway port 65534"
assert_fixed "$SBF_CONFIG_OUT" "  listen_port: 65534" "keeps the upper-bound gateway block port in agreement"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 660" "derives A without the obsolete cap at the upper timeout boundary"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 1320" "derives R without the obsolete cap at the upper timeout boundary"

# 刷新是串行的, 所以每多一个启用的模板就要多一份 subscription_timeout。
reset_config
export UCI_TEMPLATE_IDS='momo_template second_template'
export UCI_second_template_enabled=1 UCI_second_template_name='Second' UCI_second_template_file=second.json
expect_generator_success "accepts a second enabled template"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 300" "R grows by exactly T for each enabled template"

reset_config
export UCI_TEMPLATE_IDS='momo_template second_template'
export UCI_second_template_enabled=0 UCI_second_template_name='Second' UCI_second_template_file=second.json
expect_generator_success "accepts a disabled second template"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 240" "a disabled template costs no R budget"

reset_config
export UCI_main_subscription_timeout=600
configure_enabled_templates 65
expect_generator_success "does not cap the enabled template count"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 39720" "R includes all 65 enabled templates without a 3600-second cap"

reset_config
export UCI_main_subscription_timeout=5 MOCK_ENABLED_TEMPLATE_COUNT_OVERRIDE=429496704
expect_generator_success "accepts the largest simulated template count whose R fits signed int32"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 2147483645" "computes the signed-int32 R boundary exactly"

printf 'last-known-good\n' > "$SBF_CONFIG_OUT"
export MOCK_ENABLED_TEMPLATE_COUNT_OVERRIDE=429496705
expect_generator_failure "rejects an R budget above signed int32 before shell arithmetic can wrap"
assert_file_content 'last-known-good' "$SBF_CONFIG_OUT" "R overflow preserves the previous complete config"

# config.yaml 有 7 个生成入口, 它们之间原本没有共同的锁。两个并发生成会各自
# 读一份 UCI, 后写出的未必是读到新值的那个 —— config.yaml 就落后于 UCI。
GEN_LOCK_FILE="$TEST_TMP/generate.lock"
assert_contains "$GEN" 'flock' "the generator serialises itself instead of trusting every caller"
assert_contains "$GEN" 'LFGEN_LOCKED' "the re-exec cannot recurse"

reset_config
export SBF_GEN_LOCK="$GEN_LOCK_FILE" SBF_GEN_LOCK_WAIT=1
FLOCK_PROBE="$TEST_TMP/flock.probe"
: > "$FLOCK_PROBE"
if command -v flock >/dev/null 2>&1 && flock -w 0 -E 99 "$FLOCK_PROBE" true >/dev/null 2>&1; then
	# 锁被别人占着时必须超时退出, 而不是无限期阻塞或者并发写出去。
	flock "$GEN_LOCK_FILE" -c 'sleep 4' &
	holder=$!
	sleep 1
	if "$GEN" >"$TEST_TMP/locked.out" 2>&1; then
		record_failure "a concurrent generation is refused instead of racing"
	else
		locked_rc=$?
		if [ "$locked_rc" = 75 ]; then
			record_ok "a concurrent generation is refused instead of racing"
		else
			record_failure "a concurrent generation is refused instead of racing (exit $locked_rc)"
		fi
	fi
	assert_contains "$TEST_TMP/locked.out" 'another generation is still running' "the refusal explains itself"
	wait "$holder" 2>/dev/null || true
	expect_generator_success "generation succeeds again once the lock is free"

	# /dev/null is a process-global inode, not a private feature-probe target. An
	# unrelated lock holder there must not make the generator bypass its real lock.
	DEVNULL_READY="$TEST_TMP/devnull.ready"
	rm -f "$DEVNULL_READY"
	flock /dev/null sh -c ': > "$1"; sleep 2' sh "$DEVNULL_READY" &
	devnull_holder=$!
	n=0
	while [ ! -e "$DEVNULL_READY" ] && [ "$n" -lt 100 ]; do sleep 0.01; n=$((n + 1)); done
	expect_generator_success "an unrelated /dev/null lock does not disable generation locking"
	assert_not_contains "$TEST_TMP/generator.stderr" 'flock unavailable' "the private flock probe is not confused by /dev/null contention"
	wait "$devnull_holder" 2>/dev/null || true
else
	record_ok "a concurrent generation is refused instead of racing"
	record_ok "the refusal explains itself"
	record_ok "generation succeeds again once the lock is free"
	record_ok "an unrelated /dev/null lock does not disable generation locking"
	record_ok "the private flock probe is not confused by /dev/null contention"
fi
export SBF_GEN_LOCK="$RUNTIME/generate.lock"
unset SBF_GEN_LOCK_WAIT

assert_invalid_preserves_output UCI_main_port 0 "rejects port zero"
assert_invalid_preserves_output UCI_main_port 65536 "rejects port above 65535"
assert_invalid_preserves_output UCI_main_port 09716 "rejects a leading-zero port"
assert_invalid_preserves_output UCI_main_port '9716: bad' "rejects numeric YAML injection"
assert_invalid_preserves_output UCI_main_subscription_timeout 4 "rejects subscription timeout below 5"
assert_invalid_preserves_output UCI_main_subscription_timeout 601 "rejects subscription timeout above 600"
assert_invalid_preserves_output UCI_main_refresh_interval 0 "rejects refresh interval zero"
assert_invalid_preserves_output UCI_main_refresh_interval 10081 "rejects refresh interval above 10080"
assert_invalid_preserves_output UCI_main_boot_delay -1 "rejects negative boot delay"
assert_invalid_preserves_output UCI_main_boot_delay 601 "rejects boot delay above 600"

reset_config
export UCI_main_enabled=1 UCI_main_subscription_url='https://legacy.example/must-not-satisfy-enabled'
set_subscription_urls
expect_list_generator_failure "rejects a zero-item subscription URL list while enabled even when a legacy scalar exists"

# A list is the public configuration interface.  Keep a valid scalar in the
# harness only to prove that the generator consumes the list rather than
# accidentally falling back to a legacy scalar during upgrades.
reset_config
export UCI_main_enabled=1 UCI_main_subscription_url='https://legacy.example/must-not-win'
set_subscription_urls "https://first.example/sub?token=alpha&region=東京"
expect_generator_success "accepts one ordered subscription URL"
assert_fixed "$SBF_CONFIG_OUT" "  url: 'http://127.0.0.1:9717/v1/aggregate'" "routes the converter to the adjacent loopback aggregate endpoint"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" \
	"the ordered list wins over a simultaneous legacy scalar canary" \
	"    - 'https://first.example/sub?token=alpha&region=東京'"
assert_not_contains "$SBF_CONFIG_OUT" 'legacy\.example' "the legacy scalar canary never reaches generated YAML"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 120" "one URL receives the exact A budget"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 240" "one URL and one enabled template receive the exact R budget"

for count_budget in \
	'2 180 300' \
	'3 240 360' \
	'4 300 420' \
	'5 360 480' \
	'6 420 540' \
	'7 480 600'; do
	set -- $count_budget
	reset_config
	export UCI_main_enabled=1
	set_numbered_subscription_urls "$1"
	expect_generator_success "accepts an ordered $1-item subscription URL list"
	assert_fixed "$SBF_CONFIG_OUT" "  timeout: $2" "$1 URL occurrences receive the exact A budget"
	assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: $3" "$1 URL occurrences receive the exact R budget"
done

reset_config
export UCI_main_enabled=1 UCI_main_user_agent="Agent's exact value"
set_subscription_urls \
	"https://first.example/sub?token=alpha&region=東京" \
	"https://second.example/O'Brien?encoded=%27"
expect_generator_success "emits a complete gateway configuration for two ordered URLs"
assert_gateway_block "$SBF_CONFIG_OUT" "liquid_formula_gateway:
  listen_address: '127.0.0.1'
  listen_port: 9717
  source_timeout: 60
  aggregate_timeout: 180
  user_agent: 'Agent''s exact value'
  urls:
    - 'https://first.example/sub?token=alpha&region=東京'
    - 'https://second.example/O''Brien?encoded=%27'" "the enabled gateway block has every required key and preserves URL bytes"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 180" "the converter timeout agrees with the two-source aggregate timeout"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 300" "the server write timeout agrees with the two-source R budget"

reset_config
export UCI_main_enabled=1
set_subscription_urls \
	'https://duplicate.example/sub?token=same' \
	'https://duplicate.example/sub?token=same' \
	'https://third.example/sub'
expect_generator_success "accepts duplicate URL occurrences without collapsing configuration identity"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" \
	"preserves exact duplicate URLs and their occurrence order" \
	"    - 'https://duplicate.example/sub?token=same'" \
	"    - 'https://duplicate.example/sub?token=same'" \
	"    - 'https://third.example/sub'"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 240" "duplicate URL occurrences each contribute T to A"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 360" "duplicate URL occurrences each contribute T through A to R"

reset_config
export UCI_main_enabled=1
set_subscription_urls \
	"https://first.example/sub?token=alpha&region=東京" \
	"https://second.example/O'Brien?encoded=%27&name=O%27Brien" \
	"http://third.example/sub?x=3&label=三" \
	"https://fourth.example/sub?x=4" \
	"https://fifth.example/sub?x=5" \
	"https://sixth.example/sub?x=6" \
	"https://seventh.example/sub?x=7" \
	"https://eighth.example/sub?x=8"
expect_generator_success "accepts the maximum eight ordered subscription URLs"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" \
	"emits exactly eight single-quoted gateway URLs in UCI order" \
	"    - 'https://first.example/sub?token=alpha&region=東京'" \
	"    - 'https://second.example/O''Brien?encoded=%27&name=O%27Brien'" \
	"    - 'http://third.example/sub?x=3&label=三'" \
	"    - 'https://fourth.example/sub?x=4'" \
	"    - 'https://fifth.example/sub?x=5'" \
	"    - 'https://sixth.example/sub?x=6'" \
	"    - 'https://seventh.example/sub?x=7'" \
	"    - 'https://eighth.example/sub?x=8'"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 540" "eight URL occurrences receive the exact A budget"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 660" "eight URL occurrences receive the exact R budget"

reset_config
export UCI_main_enabled=1
set_subscription_urls \
	'https://one.example/sub' 'https://two.example/sub' 'https://three.example/sub' \
	'https://four.example/sub' 'https://five.example/sub' 'https://six.example/sub' \
	'https://seven.example/sub' 'https://eight.example/sub' 'https://nine.example/sub'
expect_list_generator_failure "rejects a ninth subscription URL"

reset_config
export UCI_main_enabled=1
set_subscription_urls 'https://valid.example/sub' ''
expect_list_generator_failure "rejects an empty entry in a nonempty subscription URL list"

for invalid_list_url in \
	'ftp://provider.example/sub' \
	'http://?query' \
	'https:///path' \
	'http://:80/sub' \
	'http://user@:80/sub' \
	'https://exa%6Dple.com/sub' \
	'https://user[bad]@provider.example/sub' \
	'https://provider|invalid.example/sub' \
	'https://[x:y]/sub' \
	'https://[::::]/sub' \
	'https://[2001::db8::1]/sub' \
	'https://provider.example/raw space' \
	'https://provider.example/sub ' \
	'https://provider.example/%zz' \
	"https://provider.example/sub$(printf '\001')" \
	"https://provider.example/sub$(printf '\177')"; do
	reset_config
	export UCI_main_enabled=1
	set_subscription_urls "$invalid_list_url"
	expect_list_generator_failure "rejects an invalid list subscription URL while enabled"
done

for valid_list_url in \
	'HTTPS://provider.example/sub' \
	'https://user:pass@provider.example/sub' \
	'https://[2001:db8::1]/sub' \
	'https://[::ffff:192.0.2.1]/sub' \
	'https://[fe80::1%25eth0]/sub' \
	'https://provider.example:0/sub' \
	'https://provider.example:65536/sub' \
	'https://provider.example/sub#fragment' \
	'https://provider.example/sub?opaque=%zz' \
	'https://provider.example/escaped%20space'; do
	reset_config
	export UCI_main_enabled=1
	set_subscription_urls "$valid_list_url"
	expect_generator_success "accepts backend-valid subscription URL: $valid_list_url"
	assert_fixed "$SBF_CONFIG_OUT" "    - '$valid_list_url'" \
		"preserves backend-valid subscription URL bytes: $valid_list_url"
done

reset_config
export UCI_main_enabled=0 UCI_main_subscription_url='https://legacy.example/must-not-leak'
set_subscription_urls
expect_generator_success "allows an empty subscription URL list while disabled"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" "disabled zero-item input stays an empty gateway URL list"
assert_not_contains "$SBF_CONFIG_OUT" 'legacy\.example' "a disabled zero-item list ignores the legacy scalar completely"

reset_config
export UCI_main_enabled=0
set_subscription_urls \
	'https://saved-one.example/sub?token=must-stay-private' \
	'https://saved-two.example/sub?token=must-stay-private'
expect_generator_success "validates saved subscription URLs while disabled"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" "disabled mode with saved URLs still gives the gateway an empty source list"
assert_not_contains "$SBF_CONFIG_OUT" 'saved-(one|two)\.example' "disabled mode does not expose saved subscription URLs in generated YAML"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 120" "disabled saved URLs use effective S=0 for the A budget"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 240" "disabled saved URLs use effective S=0 for the R budget"

# Check / Update may temporarily start a disabled service, but only an explicit
# per-process override may expose its saved URLs to that root-only runtime
# config. The normal disabled generation above must remain scrubbed at rest.
export SBF_INCLUDE_DISABLED_URLS=1
expect_generator_success "manual generation can use saved URLs while the service remains disabled"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" \
	"manual disabled generation preserves the complete ordered URL list" \
	"    - 'https://saved-one.example/sub?token=must-stay-private'" \
	"    - 'https://saved-two.example/sub?token=must-stay-private'"
assert_fixed "$SBF_CONFIG_OUT" "  timeout: 180" "manual disabled generation budgets both saved URLs"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 300" "manual disabled generation budgets both URLs and the enabled template"
unset SBF_INCLUDE_DISABLED_URLS

reset_config
export UCI_main_enabled=0 SBF_INCLUDE_DISABLED_URLS=1
set_subscription_urls
expect_list_generator_failure "manual disabled generation still requires at least one subscription URL"
unset SBF_INCLUDE_DISABLED_URLS

reset_config
export UCI_main_enabled=0
set_subscription_urls ''
expect_list_generator_failure "rejects one empty-string URL list item while disabled"

reset_config
export UCI_main_enabled=0
set_subscription_urls 'file:///tmp/not-a-subscription'
expect_list_generator_failure "rejects a supplied invalid list URL while disabled"

reset_config
export UCI_main_enabled=1 UCI_main_port=65535
set_subscription_urls 'https://provider.example/sub'
expect_generator_success "uses a non-overflow adjacent gateway port at the converter upper boundary"
assert_fixed "$SBF_CONFIG_OUT" "  url: 'http://127.0.0.1:65534/v1/aggregate'" "uses port 65534 and the exact aggregate path when converter port is 65535"

reset_config
export UCI_main_enabled=1
set_subscription_urls 'https://provider.example/sub?token=complete-secret'
expect_generator_success "accepts an HTTPS subscription URL while enabled"
assert_fixed "$SBF_CONFIG_OUT" "  url: 'http://127.0.0.1:9717/v1/aggregate'" "keeps the real HTTPS URL behind the exact loopback aggregate endpoint"
assert_gateway_urls_in_order "$SBF_CONFIG_OUT" \
	"passes the HTTPS list item through untouched in the gateway block" \
	"    - 'https://provider.example/sub?token=complete-secret'"

reset_config
export UCI_main_enabled=1
set_subscription_urls 'http://provider.example/sub'
expect_generator_success "accepts an HTTP subscription URL"

reset_config
export UCI_main_user_agent='v2rayN/7.0.0'
expect_generator_success "accepts a custom subscription user agent"
assert_fixed "$SBF_CONFIG_OUT" "  user_agent: 'v2rayN/7.0.0'" "emits the configured subscription user agent"

reset_config
export UCI_main_user_agent=
expect_generator_success "accepts an empty subscription user agent"
assert_fixed "$SBF_CONFIG_OUT" "  user_agent: ''" "quotes an empty user agent so the converter falls back to its default"

reset_config
export UCI_main_user_agent="sing-box 1.11.0 with an apostrophe ' inside"
expect_generator_success "accepts a user agent containing a single quote"
assert_fixed "$SBF_CONFIG_OUT" "user_agent: 'sing-box 1.11.0 with an apostrophe '' inside'" "escapes single quotes in the user agent"

assert_invalid_preserves_output UCI_main_user_agent 'sing-box 中文' "rejects a non ASCII user agent"
assert_invalid_preserves_output UCI_main_user_agent "$(awk 'BEGIN{ s=""; while (length(s) < 201) s = s "a"; print s }')" "rejects an over long user agent"

for invalid_url in 'ftp://provider.example/sub' 'file:///tmp/sub' 'provider.example/sub' 'https://'; do
	reset_config
	export UCI_main_enabled=1
	set_subscription_urls "$invalid_url"
	expect_list_generator_failure "rejects invalid subscription URL: $invalid_url"
done

for invalid_base in \
	'https://127.0.0.1/templates' \
	'http://router.example/templates' \
	'http://localhost.evil/templates' \
	'http://127.0.0.1@evil.example/templates'; do
	reset_config
	export UCI_main_template_base_url="$invalid_base"
	expect_generator_failure "rejects non-local template base URL: $invalid_base"
done

reset_config
export UCI_main_template_base_url='http://localhost:8080/liquid-formula/templates'
expect_generator_success "accepts a loopback template base URL with a local port"

reset_config
export UCI_momo_template_file='.custom.profile.json'
expect_generator_success "accepts every canonical JSON template filename"

reset_config
export UCI_momo_template_file='.json'
expect_generator_failure "rejects a template filename without a basename"

for allowed_output in \
	'/etc/momo/profiles/config.json' \
	'/etc/sing-box/generated/router.json' \
	'/var/lib/liquid-formula/output/profile.json'; do
	reset_config
	export UCI_main_output_config="$allowed_output"
	expect_generator_success "accepts output path: $allowed_output"
done

for invalid_output in \
	'relative.json' \
	'/tmp/profile.json' \
	'/etc/momo/profiles/../../shadow.json' \
	'/etc/momo/profiles/profile.txt'; do
	reset_config
	export UCI_main_output_config="$invalid_output"
	expect_generator_failure "rejects output path: $invalid_output"
done

reset_config
export UCI_TEMPLATE_IDS=other_template
export UCI_other_template_enabled=1 UCI_other_template_name=Other UCI_other_template_file=other.json UCI_other_template_no_node=Direct
expect_generator_failure "rejects a missing default template"

reset_config
export UCI_momo_template_enabled=0
expect_generator_failure "rejects a disabled default template"

reset_config
export UCI_momo_template_name= UCI_momo_template_no_node=
expect_generator_success "supports empty YAML scalar values"
assert_fixed "$SBF_CONFIG_OUT" "name: ''" "quotes an empty template name"
assert_fixed "$SBF_CONFIG_OUT" "no_node: ''" "quotes an empty no-node label"

reset_config
export UCI_main_password=
expect_generator_failure "rejects an empty authentication password"

reset_config
expect_generator_success "creates a last-known-good config before cmp failure"
before_cmp_identity=$(stat -c '%i:%Y' "$SBF_CONFIG_OUT")
before_cmp_content=$(sha256sum "$SBF_CONFIG_OUT")
export MOCK_CMP_FAIL=1
expect_generator_failure "treats cmp I/O failure as fatal"
assert_equal "$before_cmp_identity" "$(stat -c '%i:%Y' "$SBF_CONFIG_OUT")" "cmp failure preserves config inode and mtime"
assert_equal "$before_cmp_content" "$(sha256sum "$SBF_CONFIG_OUT")" "cmp failure preserves config content"
cmp_leftovers=$(find "$RUNTIME" -maxdepth 1 -name '.config.yaml.*' -print)
assert_empty "$cmp_leftovers" "cmp failure removes its staging file"

reset_config
expect_generator_success "creates a last-known-good config before output failure"
printf 'baseline=%s\n' "$(sha256sum "$SBF_CONFIG_OUT")" > "$TEST_TMP/emission-baseline"
rm -f "$MOCK_CAT_COUNT_FILE"
export UCI_main_password=changed-password MOCK_CAT_FAIL_ON=1
expect_generator_failure "propagates a config cat failure"
assert_equal "$(cat "$TEST_TMP/emission-baseline")" "baseline=$(sha256sum "$SBF_CONFIG_OUT")" "cat failure preserves the last-known-good config"

reset_config
expect_generator_success "creates a last-known-good config before foreach failure"
printf 'baseline=%s\n' "$(sha256sum "$SBF_CONFIG_OUT")" > "$TEST_TMP/foreach-baseline"
rm -f "$MOCK_FOREACH_COUNT_FILE"
export UCI_main_password=changed-password MOCK_FOREACH_FAIL_ON=2
expect_generator_failure "propagates a template emission config_foreach failure"
assert_equal "$(cat "$TEST_TMP/foreach-baseline")" "baseline=$(sha256sum "$SBF_CONFIG_OUT")" "config_foreach failure preserves the last-known-good config"

reset_config
printf 'last-known-good\n' > "$SBF_CONFIG_OUT"
export MOCK_CONFIG_LOAD_FAIL=1
expect_generator_failure "propagates config_load failure"
assert_file_content 'last-known-good' "$SBF_CONFIG_OUT" "config_load failure preserves the old config"

VALID_TEMPLATE="$TEST_TMP/valid-template.json"
INVALID_TEMPLATE="$TEST_TMP/invalid-template.json"
printf '{"outbounds":[{{ Nodes }}]}\n' > "$VALID_TEMPLATE"
printf '{"outbounds":[INVALID]}\n' > "$INVALID_TEMPLATE"
if SBF_TMP_ROOT="$RUNTIME/tmp" "$VALIDATE" "$VALID_TEMPLATE" >/dev/null 2>&1; then
	record_ok "validates a template through jsonfilter"
else
	record_failure "validates a template through jsonfilter"
fi
if SBF_TMP_ROOT="$RUNTIME/tmp" "$VALIDATE" "$INVALID_TEMPLATE" >/dev/null 2>&1; then
	record_failure "rejects malformed template JSON"
else
	record_ok "rejects malformed template JSON"
fi
validate_leftovers=$(find "$RUNTIME/tmp" -type f -name 'sbsc-template-check.*' -print 2>/dev/null)
assert_empty "$validate_leftovers" "template validation cleans unique staging files"

finish_tests
