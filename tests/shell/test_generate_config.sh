#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

GEN="$REPO_ROOT/openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh"
VALIDATE="$REPO_ROOT/openwrt-feed/singbox-formula/files/usr/share/singbox-formula/validate-template.sh"
UCI_DEFAULTS="$REPO_ROOT/openwrt-feed/singbox-formula/files/etc/config/singbox_formula"
YAML_EXAMPLE="$REPO_ROOT/openwrt-feed/singbox-formula/files/etc/singbox-formula/config.yaml.example"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-generate-test.XXXXXX") || exit 1
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
chmod 0755 "$MOCK_BIN/cat" "$MOCK_BIN/cmp" "$MOCK_BIN/jsonfilter"

export SBF_FUNCTIONS_SH="$MOCK_FUNCTIONS"
export SBF_CONFIG_OUT="$RUNTIME/config.yaml"
export SBF_TMP_ROOT="$RUNTIME/tmp"
export PATH="$MOCK_BIN:$PATH"

reset_config() {
	rm -rf "$RUNTIME"
	mkdir -p "$RUNTIME/tmp"
	export UCI_main_enabled=0
	export UCI_main_boot_delay=90
	export UCI_main_port=9716
	export UCI_main_password=890716
	export UCI_main_subscription_url=
	export UCI_main_subscription_timeout=60
	export UCI_main_refresh_interval=360
	export UCI_main_singbox_flag=1
	export UCI_main_default_template=momo_template
	export UCI_main_cache_dir="$RUNTIME/cache"
	export UCI_main_log_file="$RUNTIME/server.log"
	export UCI_main_output_config=/etc/momo/profiles/config.json
	export UCI_main_template_base_url=http://127.0.0.1/singbox-formula/templates
	export UCI_TEMPLATE_IDS=momo_template
	export UCI_momo_template_enabled=1
	export UCI_momo_template_name='Momo Template'
	export UCI_momo_template_file=momo-template.json
	export UCI_momo_template_no_node='➜ Direct'
	export MOCK_CONFIG_LOAD_FAIL=0
	export MOCK_CMP_FAIL=0
	export MOCK_CAT_FAIL_ON=0
	export MOCK_FOREACH_FAIL_ON=0
	export MOCK_CAT_COUNT_FILE="$RUNTIME/cat.count"
	export MOCK_FOREACH_COUNT_FILE="$RUNTIME/foreach.count"
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

reset_config
expect_generator_success "generates a valid disabled configuration"
assert_file_exists "$SBF_CONFIG_OUT" "creates config.yaml"
assert_equal 600 "$(stat -c %a "$SBF_CONFIG_OUT" 2>/dev/null)" "writes config.yaml with mode 0600"
assert_fixed "$SBF_CONFIG_OUT" "  password: '890716'" "preserves the intentional default password"
assert_fixed "$SBF_CONFIG_OUT" "  url: ''" "quotes an empty subscription URL as an empty YAML string"
assert_fixed "$SBF_CONFIG_OUT" "  write_timeout: 120" "sets write timeout to subscription timeout plus 60"
assert_contains "$UCI_DEFAULTS" "option[[:space:]]+password[[:space:]]+'890716'" "keeps the intentional UCI default password"
assert_contains "$UCI_DEFAULTS" "option[[:space:]]+subscription_timeout[[:space:]]+'60'" "ships the bounded subscription timeout default"
assert_contains "$YAML_EXAMPLE" 'write_timeout:[[:space:]]+120' "keeps the example HTTP timeout above the subscription timeout"

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
export UCI_main_port=1 UCI_main_subscription_timeout=5 UCI_main_refresh_interval=1 UCI_main_boot_delay=0
expect_generator_success "accepts every lower numeric boundary"
assert_fixed "$SBF_CONFIG_OUT" "write_timeout: 65" "derives the lower HTTP timeout budget"

reset_config
export UCI_main_port=65535 UCI_main_subscription_timeout=600 UCI_main_refresh_interval=10080 UCI_main_boot_delay=600
expect_generator_success "accepts every upper numeric boundary"
assert_fixed "$SBF_CONFIG_OUT" "write_timeout: 660" "derives the upper HTTP timeout budget"

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
export UCI_main_enabled=1 UCI_main_subscription_url=
expect_generator_failure "rejects an empty subscription URL while enabled"

reset_config
export UCI_main_enabled=1 UCI_main_subscription_url='https://provider.example/sub?token=complete-secret'
expect_generator_success "accepts an HTTPS subscription URL while enabled"
assert_fixed "$SBF_CONFIG_OUT" "https://provider.example/sub?token=complete-secret&flag=singbox" "preserves the complete subscription URL and appends the format flag"

reset_config
export UCI_main_enabled=1 UCI_main_subscription_url='http://provider.example/sub'
expect_generator_success "accepts an HTTP subscription URL"

for invalid_url in 'ftp://provider.example/sub' 'file:///tmp/sub' 'provider.example/sub' 'https://'; do
	reset_config
	export UCI_main_enabled=1 UCI_main_subscription_url="$invalid_url"
	expect_generator_failure "rejects invalid subscription URL: $invalid_url"
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
export UCI_main_template_base_url='http://localhost:8080/singbox-formula/templates'
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
	'/var/lib/singbox-formula/output/profile.json'; do
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
