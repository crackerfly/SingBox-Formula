#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

MIGRATION_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula"
DPI_MIGRATION_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula-dpi"
PACKAGE_MAKEFILE="$REPO_ROOT/openwrt-feed/liquid-formula/Makefile"
LUCI_MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/Makefile"
TEMPLATE_PATH='/www/liquid-formula/templates/momo-template.json'

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-migration-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_ROOT="$TEST_TMP/root"
UCI_STATE="$TEST_TMP/uci.state"
UCI_CALLS="$TEST_TMP/uci.calls"
MIGRATION_EVENTS="$TEST_TMP/migration.events"
MIGRATION_UNDER_TEST="$TEST_TMP/99-liquid-formula"
GENERATED_CONFIG="$MOCK_ROOT/etc/liquid-formula/config.yaml"
mkdir -p "$MOCK_BIN" "$MOCK_ROOT/etc/init.d" "$(dirname "$GENERATED_CONFIG")" \
	"$MOCK_ROOT/usr/share/liquid-formula"
SYSTEM_MV=$(command -v mv)
export SYSTEM_MV

cat > "$MOCK_BIN/uci" <<'EOF'
#!/bin/sh
set -u

delimiter=' '
while [ "$#" -gt 0 ]; do
	case "$1" in
		-q) shift ;;
		-d)
			delimiter=${2-}
			shift 2
			;;
		*) break ;;
	esac
done
command=${1:-}
[ "$#" -gt 0 ] && shift

case "$command" in
	get)
		key=${1:-}
		awk -F '\t' -v wanted="$key" -v delimiter="$delimiter" '
			$1 == wanted {
				scalar = 1
				scalar_value = substr($0, index($0, "\t") + 1)
			}
			index($1, wanted ".__list.") == 1 {
				list_count++
				list_value = substr($0, index($0, "\t") + 1)
				joined = joined (list_count > 1 ? delimiter : "") list_value
			}
			END {
				if (scalar) print scalar_value
				else if (list_count) print joined
				else exit 1
			}
		' "$MOCK_UCI_STATE"
		;;
	show)
		key=${1:-}
		awk -F '\t' -v wanted="$key" -v delimiter="$delimiter" '
			function quote(value) {
				gsub(/\047/, "\047\\\047\047", value)
				return "\047" value "\047"
			}
			$1 == wanted {
				scalar = 1
				scalar_value = substr($0, index($0, "\t") + 1)
			}
			index($1, wanted ".__list.") == 1 {
				list_count++
				list_value = substr($0, index($0, "\t") + 1)
				joined = joined (list_count > 1 ? delimiter : "") quote(list_value)
			}
			END {
				if (scalar) printf "%s=%s\n", wanted, quote(scalar_value)
				else if (list_count) printf "%s=%s\n", wanted, joined
				else exit 1
			}
		' "$MOCK_UCI_STATE"
		;;
	export)
		# This fixture stores one package; no argument therefore models
		# `uci export` of all packages by exporting liquid_formula.
		package=${1:-liquid_formula}
		awk -F '\t' -v wanted="$package" '
			function quote(value) {
				gsub(/\047/, "\047\\\047\047", value)
				return "\047" value "\047"
			}
			{
				row_count++
				row_key[row_count] = $1
				row_value[row_count] = substr($0, index($0, "\t") + 1)
			}
			END {
				for (i = 1; i <= row_count; i++) {
					key = row_key[i]
					remainder = key
					sub("^" wanted "\\.", "", remainder)
					if (key !~ ("^" wanted "\\.") || remainder ~ /\./)
						continue
					section_count++
					section_key[section_count] = key
					section_name[section_count] = remainder
					section_type[section_count] = row_value[i]
				}
				if (!section_count)
					exit 1
				printf "package %s\n\n", wanted
				for (i = 1; i <= section_count; i++) {
					prefix = section_key[i] "."
					printf "config %s %s\n", section_type[i], quote(section_name[i])
					for (j = 1; j <= row_count; j++) {
						if (index(row_key[j], prefix) != 1)
							continue
						option = substr(row_key[j], length(prefix) + 1)
						if (option ~ /\.__list\.[0-9]+$/) {
							sub(/\.__list\.[0-9]+$/, "", option)
							printf "\tlist %s %s\n", option, quote(row_value[j])
						} else if (option !~ /\./) {
							printf "\toption %s %s\n", option, quote(row_value[j])
						}
					}
					print ""
				}
			}
		' "$MOCK_UCI_STATE"
		;;
	set)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		tmp="$MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".__list.") != 1 { print }
		' "$MOCK_UCI_STATE" > "$tmp"
		printf '%s\t%s\n' "$key" "$value" >> "$tmp"
		mv "$tmp" "$MOCK_UCI_STATE"
		printf 'set|%s|%s\n' "$key" "$value" >> "$MOCK_UCI_CALLS"
		;;
	add_list)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		tmp="$MOCK_UCI_STATE.tmp.$$"
		scalar_present=0
		scalar_value=
		if scalar_value=$(awk -F '\t' -v wanted="$key" '
			$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 }
			END { exit(found ? 0 : 1) }
		' "$MOCK_UCI_STATE"); then
			scalar_present=1
		fi
		awk -F '\t' -v wanted="$key" '$1 != wanted { print }' "$MOCK_UCI_STATE" > "$tmp"
		index=$(awk -F '\t' -v prefix="$key.__list." '
			index($1, prefix) == 1 {
				number = substr($1, length(prefix) + 1) + 0
				if (number > maximum) maximum = number
			}
			END { print maximum + 0 }
		' "$tmp")
		if [ "$scalar_present" = 1 ]; then
			index=$((index + 1))
			printf '%s.__list.%s\t%s\n' "$key" "$index" "$scalar_value" >> "$tmp"
		fi
		index=$((index + 1))
		printf '%s.__list.%s\t%s\n' "$key" "$index" "$value" >> "$tmp"
		mv "$tmp" "$MOCK_UCI_STATE"
		printf 'add_list|%s|%s\n' "$key" "$value" >> "$MOCK_UCI_CALLS"
		;;
	delete)
		key=${1:-}
		tmp="$MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".") != 1 { print }
		' "$MOCK_UCI_STATE" > "$tmp"
		mv "$tmp" "$MOCK_UCI_STATE"
		printf 'delete|%s\n' "$key" >> "$MOCK_UCI_CALLS"
		;;
	commit)
		printf 'commit|%s\n' "${1:-}" >> "$MOCK_UCI_CALLS"
		printf 'uci-commit|%s\n' "${1:-}" >> "$MOCK_MIGRATION_EVENTS"
		;;
	*)
		exit 2
		;;
esac
EOF
chmod 0755 "$MOCK_BIN/uci"

cat > "$MOCK_BIN/mv" <<'EOF'
#!/bin/sh
set -u

previous=
last=
for argument in "$@"; do
	previous=$last
	last=$argument
done

if [ -n "${MOCK_LEGACY_MARKER_PATH:-}" ] && [ "$last" = "$MOCK_LEGACY_MARKER_PATH" ]; then
	mode=$(stat -c %a "$previous" 2>/dev/null || printf missing)
	digest=$(sha256sum "$previous" 2>/dev/null || printf missing)
	digest=${digest%% *}
	printf 'marker-rename|%s|%s|%s|%s\n' \
		"$(dirname "$previous")" "$mode" "$digest" "$last" >> "$MOCK_MIGRATION_EVENTS"
	if [ "${MOCK_MARKER_MV_FAIL:-0}" = 1 ]; then
		exit 74
	fi
fi

exec "$SYSTEM_MV" "$@"
EOF
chmod 0755 "$MOCK_BIN/mv"

cat > "$MOCK_ROOT/etc/init.d/liquid-formula" <<EOF
#!/bin/sh
printf 'init|%s\n' "\$*" >> "$TEST_TMP/runtime.calls"
exit 0
EOF
chmod 0755 "$MOCK_ROOT/etc/init.d/liquid-formula"

cat > "$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh" <<EOF
#!/bin/sh
printf 'generate\n' >> "$TEST_TMP/runtime.calls"
printf 'generated by migration\n' > "$GENERATED_CONFIG"
exit 0
EOF
chmod 0755 "$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh"

sed \
	-e "s|/etc/liquid-formula|$MOCK_ROOT/etc/liquid-formula|g" \
	-e "s|/var/lib/liquid-formula|$MOCK_ROOT/var/lib/liquid-formula|g" \
	-e "s|/var/log/liquid-formula|$MOCK_ROOT/var/log/liquid-formula|g" \
	-e "s|/www/liquid-formula|$MOCK_ROOT/www/liquid-formula|g" \
	-e "s|/etc/init.d/liquid-formula|$MOCK_ROOT/etc/init.d/liquid-formula|g" \
	-e "s|/usr/share/liquid-formula/generate-config.sh|$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh|g" \
	"$MIGRATION_SOURCE" > "$MIGRATION_UNDER_TEST"
chmod 0755 "$MIGRATION_UNDER_TEST"

PATH="$MOCK_BIN:$PATH"
MOCK_UCI_STATE="$UCI_STATE"
MOCK_UCI_CALLS="$UCI_CALLS"
MARKER_DIR="$MOCK_ROOT/var/lib/liquid-formula/subscriptions"
MARKER_PATH="$MARKER_DIR/legacy-first-url.sha256"
MOCK_LEGACY_MARKER_PATH="$MARKER_PATH"
MOCK_MIGRATION_EVENTS="$MIGRATION_EVENTS"
MOCK_MARKER_MV_FAIL=0
export PATH MOCK_UCI_STATE MOCK_UCI_CALLS MOCK_LEGACY_MARKER_PATH
export MOCK_MIGRATION_EVENTS MOCK_MARKER_MV_FAIL
UCI_LIST_DELIMITER=$(printf '\037')

cat > "$UCI_STATE" <<'EOF'
liquid_formula.main	global
liquid_formula.main.enabled	1
liquid_formula.main.port	9000
liquid_formula.main.password	custom-password
liquid_formula.main.default_template	openwrt
liquid_formula.main.output_config	/etc/sing-box/config.json
liquid_formula.openwrt	template
liquid_formula.openwrt.enabled	0
liquid_formula.openwrt.name	Custom Legacy Template
liquid_formula.openwrt.file	custom-openwrt.json
liquid_formula.openwrt.no_node	Custom Direct
EOF
printf 'liquid_formula.main.subscription_url\t\n' >> "$UCI_STATE"
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
: > "$TEST_TMP/runtime.calls"
printf 'user-edited generated config\n' > "$GENERATED_CONFIG"

state_value() {
	_key=$1
	awk -F '\t' -v wanted="$_key" '$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 } END { if (!found) exit 1 }' "$UCI_STATE"
}

assert_state() {
	_key=$1
	_expected=$2
	_description=$3
	_actual='<missing>'
	_actual=$(state_value "$_key" 2>/dev/null) || _actual='<missing>'
	if [ "$_actual" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected '$_expected', got '$_actual')"
	fi
}

assert_state_exists() {
	_key=$1
	_description=$2
	if state_value "$_key" >/dev/null 2>&1; then
		record_ok "$_description"
	else
		record_failure "$_description (missing: $_key)"
	fi
}

assert_state_missing() {
	_key=$1
	_description=$2
	if state_value "$_key" >/dev/null 2>&1; then
		record_failure "$_description (unexpected: $_key)"
	else
		record_ok "$_description"
	fi
}

assert_uci_list_values() {
	_key=$1
	_expected=$2
	_description=$3
	_actual='<missing>'
	if _actual=$(uci -q -d "$UCI_LIST_DELIMITER" get "$_key") &&
		[ "$_actual" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected '$_expected', got '$_actual')"
	fi
}

assert_uci_missing() {
	_key=$1
	_description=$2
	if uci -q get "$_key" >/dev/null 2>&1; then
		record_failure "$_description (unexpected: $_key)"
	else
		record_ok "$_description"
	fi
}

assert_export_main_lines() {
	_option=$1
	_expected=$2
	_description=$3
	_actual=$(uci -q export liquid_formula | awk -v wanted="$_option" '
		$1 == "config" { in_main = ($3 == "\047main\047"); next }
		in_main && ($1 == "option" || $1 == "list") && $2 == wanted {
			line = $0
			sub(/^[[:space:]]+/, "", line)
			print line
		}
	')
	if [ "$_actual" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected '$_expected', got '$_actual')"
	fi
}

assert_export_subscription_lines() {
	_expected=$1
	_description=$2
	assert_export_main_lines subscription_url "$_expected" "$_description"
}

assert_subscription_migration_calls() {
	_expected_add_value=$1
	_description=$2
	_actual=$(awk -F '|' '
		$1 == "delete" && $2 == "liquid_formula.main.subscription_url" { print }
		$1 == "add_list" && $2 == "liquid_formula.main.subscription_url" { print }
	' "$UCI_CALLS")
	_expected="delete|liquid_formula.main.subscription_url
add_list|liquid_formula.main.subscription_url|$_expected_add_value"
	if [ "$_actual" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected delete then one add_list, got '$_actual')"
	fi
}

assert_empty_subscription_migration_calls() {
	_description=$1
	_actual=$(awk -F '|' '
		$1 == "delete" && $2 == "liquid_formula.main.subscription_url" { print }
		$1 == "add_list" && $2 == "liquid_formula.main.subscription_url" { print }
	' "$UCI_CALLS")
	if [ "$_actual" = 'delete|liquid_formula.main.subscription_url' ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected one delete and no add_list, got '$_actual')"
	fi
}

assert_no_legacy_marker() {
	_description=$1
	assert_file_not_exists "$MARKER_PATH" "$_description"
}

assert_exact_legacy_marker() {
	_expected_content=$1
	_expected_file_sha=$2
	_description=$3
	assert_file_content "$_expected_content" "$MARKER_PATH" "$_description contains the exact scalar URL digest"
	assert_file_sha256 "$_expected_file_sha" "$MARKER_PATH" "$_description contains exactly one digest plus newline"
	assert_equal 600 "$(stat -c %a "$MARKER_PATH" 2>/dev/null)" "$_description has mode 0600"
	assert_equal 700 "$(stat -c %a "$MARKER_DIR" 2>/dev/null)" "$_description uses a private state directory"
}

# Characterize the libuci edge that makes delete-before-add_list mandatory:
# adding a list item to a scalar converts and retains that scalar as item one.
printf 'liquid_formula.main.fixture_scalar\tfirst value\n' >> "$UCI_STATE"
uci add_list 'liquid_formula.main.fixture_scalar=second value'
assert_equal 'first value|second value' \
	"$(uci -q -d '|' get liquid_formula.main.fixture_scalar)" \
	'the UCI mock add_list operation retains a nonempty scalar as item one'
assert_equal "liquid_formula.main.fixture_scalar='first value'|'second value'" \
	"$(uci -q -d '|' show liquid_formula.main.fixture_scalar)" \
	'the UCI mock show command joins converted list items on one line'
assert_export_main_lines fixture_scalar "list fixture_scalar 'first value'
list fixture_scalar 'second value'" 'the UCI mock export distinguishes the converted value as a list'
uci delete liquid_formula.main.fixture_scalar

printf 'liquid_formula.main.fixture_scalar\t\n' >> "$UCI_STATE"
uci add_list 'liquid_formula.main.fixture_scalar=after-empty'
assert_equal '|after-empty' \
	"$(uci -q -d '|' get liquid_formula.main.fixture_scalar)" \
	'the UCI mock add_list operation retains an empty scalar before the appended item'
assert_export_main_lines fixture_scalar "list fixture_scalar ''
list fixture_scalar 'after-empty'" 'the UCI mock export retains both list items after empty-scalar conversion'
uci delete liquid_formula.main.fixture_scalar
: > "$UCI_CALLS"

"$MIGRATION_UNDER_TEST"

assert_file_content 'user-edited generated config' "$GENERATED_CONFIG" \
	'upgrade migration preserves an existing generated conffile'
assert_not_contains "$TEST_TMP/runtime.calls" '^generate$' \
	'upgrade migration does not regenerate an existing config.yaml'

assert_state liquid_formula.main.port 9000 'migration preserves an explicitly selected legacy port'
assert_state liquid_formula.main.output_config /etc/sing-box/config.json 'migration preserves an explicitly selected legacy output path'
assert_state liquid_formula.main.password custom-password 'migration preserves an explicit password'
assert_uci_missing liquid_formula.main.subscription_url 'migration removes a legacy empty subscription scalar as a zero-item list'
assert_export_subscription_lines '' 'a migrated empty scalar exports no subscription_url option or list'
assert_empty_subscription_migration_calls 'empty scalar migration performs one delete and never adds an empty list item'
assert_state liquid_formula.main.default_template openwrt 'migration preserves the explicit default template choice'
assert_state liquid_formula.openwrt.file custom-openwrt.json 'migration does not delete or rewrite a custom legacy template'
assert_state liquid_formula.main.boot_delay 90 'migration fills a missing boot delay'
assert_state liquid_formula.main.subscription_timeout 60 'migration fills a missing subscription timeout'
assert_state liquid_formula.main.template_base_url http://127.0.0.1/liquid-formula/templates 'migration fills a missing template base URL'
assert_state liquid_formula.momo_template.file momo-template.json 'migration adds the missing packaged template without removing user sections'
assert_state liquid_formula.momo_template.enabled 1 'migration enables the packaged momo template'
assert_state liquid_formula.localdns_template.file localdns-template.json 'migration adds the missing local DNS template'
assert_state liquid_formula.localdns_template.enabled 1 'migration enables the packaged local DNS template'
assert_no_legacy_marker 'an empty legacy scalar creates no adoption marker'

# Normalize the fixture to the required zero-item representation before the
# idempotence run, even when the pre-feature product left its scalar behind.
awk -F '\t' 'index($1, "liquid_formula.main.subscription_url") != 1 { print }' "$UCI_STATE" > "$UCI_STATE.next"
mv "$UCI_STATE.next" "$UCI_STATE"
: > "$UCI_CALLS"
"$MIGRATION_UNDER_TEST"
assert_empty "$(cat "$UCI_CALLS")" 'a second migration run performs no UCI writes or commit'
assert_uci_missing liquid_formula.main.subscription_url 'a second migration run keeps the zero-item subscription list omitted'
assert_no_legacy_marker 'an absent subscription value creates no adoption marker'

# A pre-1.8.4 scalar must become exactly the first (and only) list item.  The
# URL deliberately includes the characters providers commonly put in tokens;
# migration must not shell-evaluate, decode, or truncate it.
cat > "$UCI_STATE" <<'EOF'
liquid_formula.main	global
liquid_formula.main.subscription_url	https://provider.example/sub?token=O%27Brien&region=東京
EOF
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
"$MIGRATION_UNDER_TEST"
assert_uci_list_values liquid_formula.main.subscription_url 'https://provider.example/sub?token=O%27Brien&region=東京' 'migration preserves the complete legacy URL as the only list item'
assert_export_subscription_lines "list subscription_url 'https://provider.example/sub?token=O%27Brien&region=東京'" 'the migrated nonempty scalar exports as exactly one list item'
assert_subscription_migration_calls 'https://provider.example/sub?token=O%27Brien&region=東京' 'nonempty migration deletes the scalar before one add_list call'
assert_exact_legacy_marker \
	'bf65ef88a81c819a57cb5c2e05ed8d152436b3d43bc0f6f01f52cbbe07a1c85e' \
	'175b1bab2ad1934314bf3b506b6c887159a61138e1d4b90e6d393398ca66c46f' \
	'the scalar migration marker'
assert_equal "uci-commit|liquid_formula
marker-rename|$MARKER_DIR|600|175b1bab2ad1934314bf3b506b6c887159a61138e1d4b90e6d393398ca66c46f|$MARKER_PATH" \
	"$(cat "$MIGRATION_EVENTS")" \
	'the marker is staged privately and atomically renamed only after the UCI commit'
marker_identity=$(stat -c '%i:%Y' "$MARKER_PATH" 2>/dev/null || printf missing)
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
"$MIGRATION_UNDER_TEST"
assert_empty "$(cat "$UCI_CALLS")" 'the converted subscription list makes a second migration run zero-write'
assert_uci_list_values liquid_formula.main.subscription_url 'https://provider.example/sub?token=O%27Brien&region=東京' 'the second run cannot duplicate the migrated list item'
assert_equal "$marker_identity" "$(stat -c '%i:%Y' "$MARKER_PATH" 2>/dev/null || printf missing)" 'an idempotent list migration does not replace the existing marker'
assert_empty "$(cat "$MIGRATION_EVENTS")" 'an idempotent list migration performs no marker write'

# Marker publication is deliberately after the irreversible UCI commit.  If
# the atomic marker rename fails, the migration reports failure but keeps the
# exact one-item list rather than attempting an unsafe rollback to a scalar.
rm -rf "$MARKER_DIR"
cat > "$UCI_STATE" <<'EOF'
liquid_formula.main	global
liquid_formula.main.subscription_url	https://failure.example/sub?token=still-committed
EOF
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
MOCK_MARKER_MV_FAIL=1
export MOCK_MARKER_MV_FAIL
if "$MIGRATION_UNDER_TEST" >"$TEST_TMP/marker-failure.stdout" 2>"$TEST_TMP/marker-failure.stderr"; then
	record_failure 'a legacy marker rename failure is reported'
else
	record_ok 'a legacy marker rename failure is reported'
fi
assert_uci_list_values liquid_formula.main.subscription_url \
	'https://failure.example/sub?token=still-committed' \
	'a marker write failure keeps the already-committed one-item URL list'
assert_export_subscription_lines \
	"list subscription_url 'https://failure.example/sub?token=still-committed'" \
	'a marker write failure does not revert the committed list type'
assert_equal '1' "$(awk -F '|' '$1 == "commit" && $2 == "liquid_formula" { count++ } END { print count + 0 }' "$UCI_CALLS")" \
	'a marker write failure occurs after exactly one durable UCI commit'
assert_equal "uci-commit|liquid_formula
marker-rename|$MARKER_DIR|600|e5d9f11c5c380e6600b39501b3d8f3d3c06c82f89767696a306224c7fb6704fa|$MARKER_PATH" \
	"$(cat "$MIGRATION_EVENTS")" \
	'the failed marker rename was attempted after the UCI commit with complete mode-0600 bytes'
assert_no_legacy_marker 'a failed marker rename exposes no partial adoption marker'
MOCK_MARKER_MV_FAIL=0
export MOCK_MARKER_MV_FAIL

# Once a list exists it is authoritative.  A migration rerun must neither add
# a legacy scalar nor rewrite an ordered list that another upgrade already made.
rm -rf "$MARKER_DIR"
awk -F '\t' 'index($1, "liquid_formula.main.subscription_url") != 1 { print }' "$UCI_STATE" > "$UCI_STATE.next"
mv "$UCI_STATE.next" "$UCI_STATE"
printf 'liquid_formula.main.subscription_url.__list.1\thttps://first.example/sub?token=one&city=東京\n' >> "$UCI_STATE"
printf "liquid_formula.main.subscription_url.__list.2\t  https://second.example/O'Brien?encoded=%%27&label=two words  \n" >> "$UCI_STATE"
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
assert_equal "https://first.example/sub?token=one&city=東京   https://second.example/O'Brien?encoded=%27&label=two words  " \
	"$(uci -q get liquid_formula.main.subscription_url)" \
	'the UCI mock get command retains whitespace inside list values'
assert_equal "liquid_formula.main.subscription_url='https://first.example/sub?token=one&city=東京' '  https://second.example/O'\\''Brien?encoded=%27&label=two words  '" \
	"$(uci -q show liquid_formula.main.subscription_url)" \
	'the UCI mock show command quotes each whitespace-bearing list item separately'
assert_equal "https://first.example/sub?token=one&city=東京|  https://second.example/O'Brien?encoded=%27&label=two words  " \
	"$(uci -q -d '|' get liquid_formula.main.subscription_url)" \
	'the UCI mock custom delimiter does not trim list item bytes'
"$MIGRATION_UNDER_TEST"
assert_uci_list_values liquid_formula.main.subscription_url \
	"https://first.example/sub?token=one&city=東京${UCI_LIST_DELIMITER}  https://second.example/O'Brien?encoded=%27&label=two words  " \
	'migration never rewrites an already-migrated ordered URL list'
assert_export_subscription_lines "list subscription_url 'https://first.example/sub?token=one&city=東京'
list subscription_url '  https://second.example/O'\\''Brien?encoded=%27&label=two words  '" 'an existing ordered list keeps whitespace and list syntax in export'
assert_empty "$(cat "$UCI_CALLS")" 'an existing subscription list skips scalar migration and all UCI writes'
assert_no_legacy_marker 'an existing multi-item list creates no legacy marker'
assert_empty "$(cat "$MIGRATION_EVENTS")" 'an existing multi-item list performs no marker write'

# A one-item list has the same get/show text as a scalar.  Keeping it zero-write
# proves migration used export type information instead of delimiter heuristics.
awk -F '\t' 'index($1, "liquid_formula.main.subscription_url") != 1 { print }' "$UCI_STATE" > "$UCI_STATE.next"
mv "$UCI_STATE.next" "$UCI_STATE"
printf 'liquid_formula.main.subscription_url.__list.1\t  https://single.example/sub?token=one&city=京都  \n' >> "$UCI_STATE"
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
"$MIGRATION_UNDER_TEST"
assert_uci_list_values liquid_formula.main.subscription_url '  https://single.example/sub?token=one&city=京都  ' 'migration preserves every byte of the sole whitespace-bearing list item'
assert_export_subscription_lines "list subscription_url '  https://single.example/sub?token=one&city=京都  '" 'a one-item existing list retains quoted list syntax and whitespace in export'
assert_empty "$(cat "$UCI_CALLS")" 'a one-item existing list remains zero-write and therefore requires export type detection'
assert_no_legacy_marker 'an existing one-item list creates no legacy marker'
assert_empty "$(cat "$MIGRATION_EVENTS")" 'an existing one-item list performs no marker write'

# A package that never had the scalar option must remain a zero-item list and
# must not fabricate the digest of an empty string as a legacy adoption marker.
cat > "$UCI_STATE" <<'EOF'
liquid_formula.main	global
EOF
rm -rf "$MARKER_DIR"
: > "$UCI_CALLS"
: > "$MIGRATION_EVENTS"
rm -f "$GENERATED_CONFIG"
: > "$TEST_TMP/runtime.calls"
"$MIGRATION_UNDER_TEST"
assert_uci_missing liquid_formula.main.subscription_url 'a fresh install leaves the URL list empty instead of creating an empty scalar'
assert_export_subscription_lines '' 'a fresh install exports no subscription_url entry'
assert_no_legacy_marker 'a fresh install creates no legacy marker'
assert_empty "$(cat "$MIGRATION_EVENTS" | awk -F '|' '$1 == "marker-rename" { print }')" 'a fresh install never attempts marker publication'
assert_file_content 'generated by migration' "$GENERATED_CONFIG" \
	'a fresh install generates its initial config.yaml'
assert_contains "$TEST_TMP/runtime.calls" '^generate$' \
	'a fresh install invokes the config generator exactly when config.yaml is absent'

# Exercise the DPI uci-defaults migration as a real script. A fake default
# route is present specifically to prove fresh installs no longer copy a
# guessed /proc device into UCI.
DPI_MOCK_BIN="$TEST_TMP/dpi-bin"
DPI_MOCK_ROOT="$TEST_TMP/dpi-root"
DPI_UCI_STATE="$TEST_TMP/dpi-uci.state"
DPI_UCI_CALLS="$TEST_TMP/dpi-uci.calls"
DPI_ROUTE_FILE="$TEST_TMP/proc-net-route"
DPI_MIGRATION_UNDER_TEST="$TEST_TMP/99-liquid-formula-dpi"
mkdir -p "$DPI_MOCK_BIN" "$DPI_MOCK_ROOT/etc/init.d"

cat >"$DPI_MOCK_BIN/uci" <<'EOF'
#!/bin/sh
set -u

[ "${1:-}" != -q ] || shift
command=${1:-}
[ "$#" -eq 0 ] || shift

state_value() {
	awk -F '\t' -v wanted="$1" '
		$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 }
		END { exit(found ? 0 : 1) }
	' "$DPI_MOCK_UCI_STATE"
}

case "$command" in
	get)
		key=${1:-}
		if state_value "$key"; then
			exit 0
		fi
		awk -F '\t' -v prefix="$key.__list." '
			index($1, prefix) == 1 {
				value = substr($0, index($0, "\t") + 1)
				printf "%s%s", count++ ? " " : "", value
			}
			END {
				if (count) print ""
				else exit 1
			}
		' "$DPI_MOCK_UCI_STATE"
		;;
	show)
		package=${1:-}
		awk -F '\t' -v prefix="$package." '
			index($1, prefix) == 1 && $1 ~ /\.__type$/ {
				key = $1
				sub(/\.__type$/, "", key)
				print key "=" substr($0, index($0, "\t") + 1)
			}
		' "$DPI_MOCK_UCI_STATE"
		;;
	set)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		tmp="$DPI_MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".__list.") != 1 { print }
		' "$DPI_MOCK_UCI_STATE" >"$tmp"
		printf '%s\t%s\n' "$key" "$value" >>"$tmp"
		mv "$tmp" "$DPI_MOCK_UCI_STATE"
		printf 'set|%s|%s\n' "$key" "$value" >>"$DPI_MOCK_UCI_CALLS"
		;;
	add_list)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		index=$(awk -F '\t' -v prefix="$key.__list." '
			index($1, prefix) == 1 {
				n = substr($1, length(prefix) + 1) + 0
				if (n > maximum) maximum = n
			}
			END { print maximum + 1 }
		' "$DPI_MOCK_UCI_STATE")
		printf '%s.__list.%s\t%s\n' "$key" "$index" "$value" >>"$DPI_MOCK_UCI_STATE"
		printf 'add_list|%s|%s\n' "$key" "$value" >>"$DPI_MOCK_UCI_CALLS"
		;;
	delete)
		key=${1:-}
		tmp="$DPI_MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".__list.") != 1 { print }
		' "$DPI_MOCK_UCI_STATE" >"$tmp"
		mv "$tmp" "$DPI_MOCK_UCI_STATE"
		printf 'delete|%s\n' "$key" >>"$DPI_MOCK_UCI_CALLS"
		;;
	commit)
		printf 'commit|%s\n' "${1:-}" >>"$DPI_MOCK_UCI_CALLS"
		;;
	*)
		exit 2
		;;
esac
EOF
chmod 0755 "$DPI_MOCK_BIN/uci"

printf 'Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n' >"$DPI_ROUTE_FILE"
printf 'stale0\t00000000\t00000000\t0003\t0\t0\t0\t00000000\n' >>"$DPI_ROUTE_FILE"
sed \
	-e "s|/etc/taoistfuchen|$DPI_MOCK_ROOT/etc/taoistfuchen|g" \
	-e "s|/etc/liquid-formula|$DPI_MOCK_ROOT/etc/liquid-formula|g" \
	-e "s|/etc/init.d|$DPI_MOCK_ROOT/etc/init.d|g" \
	-e "s|/proc/net/route|$DPI_ROUTE_FILE|g" \
	"$DPI_MIGRATION_SOURCE" >"$DPI_MIGRATION_UNDER_TEST"
chmod 0755 "$DPI_MIGRATION_UNDER_TEST"

DPI_MOCK_UCI_STATE="$DPI_UCI_STATE"
DPI_MOCK_UCI_CALLS="$DPI_UCI_CALLS"
export DPI_MOCK_UCI_STATE DPI_MOCK_UCI_CALLS

dpi_state_value() {
	awk -F '\t' -v wanted="$1" '
		$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 }
		END { exit(found ? 0 : 1) }
	' "$DPI_UCI_STATE"
}

dpi_list_value() {
	"$DPI_MOCK_BIN/uci" -q get "$1" 2>/dev/null || true
}

cat >"$DPI_UCI_STATE" <<'EOF'
fakehttp.main.__type	fakehttp
fakehttp.payload1.__type	payload
fakesip.main.__type	fakesip
EOF
: >"$DPI_UCI_CALLS"
PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST"
assert_equal auto "$(dpi_state_value fakehttp.main.interface_mode)" \
	'fresh FakeHTTP installation defaults to official WAN auto mode'
assert_equal auto "$(dpi_state_value fakesip.main.interface_mode)" \
	'fresh FakeSIP installation defaults to official WAN auto mode'
assert_equal '' "$(dpi_list_value fakehttp.main.interface)" \
	'fresh FakeHTTP installation does not persist a guessed route device'
assert_equal '' "$(dpi_list_value fakesip.main.interface)" \
	'fresh FakeSIP installation does not persist a guessed route device'
assert_equal 8970 "$(dpi_state_value fakehttp.main.queue_num)" \
	'fresh auto migration retains FakeHTTP NFQUEUE 8970'
assert_equal 8971 "$(dpi_state_value fakesip.main.queue_num)" \
	'fresh auto migration retains FakeSIP NFQUEUE 8971'
assert_equal 53 "$(dpi_state_value fakesip.main.ports)" \
	'fresh auto migration retains FakeSIP default excluded port 53'

# A pre-auto installation with saved manual lists is selected even if its old
# package never wrote interface_mode explicitly. Neither list may be rewritten
# or supplemented by the fake default route.
cat >"$DPI_UCI_STATE" <<'EOF'
fakehttp.main.__type	fakehttp
fakehttp.main.interface.__list.1	manual-http0
fakehttp.payload1.__type	payload
fakesip.main.__type	fakesip
fakesip.main.interface.__list.1	manual-sip0
EOF
: >"$DPI_UCI_CALLS"
PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST"
assert_equal selected "$(dpi_state_value fakehttp.main.interface_mode)" \
	'migration classifies an existing FakeHTTP manual list as selected'
assert_equal selected "$(dpi_state_value fakesip.main.interface_mode)" \
	'migration classifies an existing FakeSIP manual list as selected'
assert_equal manual-http0 "$(dpi_list_value fakehttp.main.interface)" \
	'migration preserves the existing FakeHTTP manual list exactly'
assert_equal manual-sip0 "$(dpi_list_value fakesip.main.interface)" \
	'migration preserves the existing FakeSIP manual list exactly'

# Existing empty options are still explicit UCI values. Additive migration may
# leave them for the normal validator/UI to report, but must not silently turn
# them into new package defaults during an upgrade.
{
	printf 'fakehttp.main.__type\tfakehttp\n'
	printf 'fakehttp.main.interface_mode\t\n'
	printf 'fakehttp.main.direction\t\n'
	printf 'fakehttp.payload1.__type\tpayload\n'
	printf 'fakesip.main.__type\tfakesip\n'
	printf 'fakesip.main.interface_mode\t\n'
	printf 'fakesip.main.ports\t\n'
} >"$DPI_UCI_STATE"
: >"$DPI_UCI_CALLS"
PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST"
assert_equal '' "$(dpi_state_value fakehttp.main.interface_mode)" \
	'migration preserves an explicitly empty FakeHTTP interface mode'
assert_equal '' "$(dpi_state_value fakehttp.main.direction)" \
	'migration preserves an explicitly empty FakeHTTP direction'
assert_equal '' "$(dpi_state_value fakesip.main.interface_mode)" \
	'migration preserves an explicitly empty FakeSIP interface mode'
assert_equal '' "$(dpi_state_value fakesip.main.ports)" \
	'migration preserves an explicitly empty FakeSIP port list'

assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/conffiles' \
	'^/www/liquid-formula/templates/momo-template\.json$' \
	'ships the editable packaged template as a conffile'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/conffiles' \
	'^/www/liquid-formula/templates/localdns-template\.json$' \
	'ships the editable local DNS template as a conffile'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/install' \
	'run-delayed\.sh.*run-delayed\.sh' \
	'installs the managed boot delay helper'

LUCI_POSTINST="$TEST_TMP/luci-postinst.sh"
RPCD_INIT="$TEST_TMP/rpcd"
RPCD_PLUGIN="$TEST_TMP/liquid_formula"

make_named_block "$LUCI_MAKEFILE" 'Package/luci-app-liquid-formula/postinst' | \
	sed \
		-e 's/\$\$/\$/g' \
		-e "s|/usr/libexec/rpcd/liquid_formula|$RPCD_PLUGIN|g" \
		-e "s|/etc/init.d/rpcd|$RPCD_INIT|g" \
		-e "s|/tmp/luci-|$TEST_TMP/luci-|g" \
	> "$LUCI_POSTINST"
chmod 0755 "$LUCI_POSTINST"

cat > "$RPCD_INIT" <<'EOF'
#!/bin/sh
exit "${MOCK_RPCD_RC:-0}"
EOF
chmod 0755 "$RPCD_INIT"
printf '#!/bin/sh\nexit 0\n' > "$RPCD_PLUGIN"
chmod 0644 "$RPCD_PLUGIN"

IPKG_INSTROOT=
MOCK_RPCD_RC=1
export IPKG_INSTROOT MOCK_RPCD_RC
if "$LUCI_POSTINST" >/dev/null 2>&1; then
	record_failure 'LuCI postinst propagates rpcd registration failure'
else
	record_ok 'LuCI postinst propagates rpcd registration failure'
fi

MOCK_RPCD_RC=0
export MOCK_RPCD_RC
if "$LUCI_POSTINST" >/dev/null 2>&1; then
	record_ok 'LuCI postinst succeeds after rpcd registration succeeds'
else
	record_failure 'LuCI postinst succeeds after rpcd registration succeeds'
fi
if [ -x "$RPCD_PLUGIN" ]; then
	record_ok 'LuCI postinst makes the real rpcd plugin executable'
else
	record_failure 'LuCI postinst makes the real rpcd plugin executable'
fi

POSTINST_CONTENT=$(make_named_block "$LUCI_MAKEFILE" 'Package/luci-app-liquid-formula/postinst')
if printf '%s\n' "$POSTINST_CONTENT" | grep -Fq '/etc/init.d/uhttpd'; then
	record_failure 'LuCI postinst does not restart uhttpd'
else
	record_ok 'LuCI postinst does not restart uhttpd'
fi
if printf '%s\n' "$POSTINST_CONTENT" | grep -Fq '/usr/libexec/rpcd/liquid-formula'; then
	record_failure 'LuCI postinst does not chmod a nonexistent rpcd alias'
else
	record_ok 'LuCI postinst does not chmod a nonexistent rpcd alias'
fi

finish_tests
