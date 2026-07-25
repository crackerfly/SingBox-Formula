#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
action=$repo_root/.github/actions/build-package/action.yml
workflow=$repo_root/.github/workflows/build.yml
publisher=$repo_root/.github/scripts/publish-release.sh
pkg_version=$(sed -n 's/^PKG_VERSION:=\(.*\)$/\1/p' \
	"$repo_root/openwrt-feed/liquid-formula/Makefile")
test -n "$pkg_version"
release_tag="v$pkg_version"
test_tmp=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-release-test.XXXXXX")
trap 'rm -rf "$test_tmp"' EXIT HUP INT TERM

fail()
{
	echo "not ok - $*" >&2
	exit 1
}

pass()
{
	echo "ok - $*"
}

assert_contains()
{
	file=$1
	pattern=$2
	message=$3
	grep -Fq -- "$pattern" "$file" || fail "$message"
	pass "$message"
}

# Execute the exact package-selection checks embedded in the composite action.
# This catches malformed grep patterns rather than merely checking that a grep
# command is present in YAML.
select_commands=$(awk '
	/make defconfig/ { after_defconfig=1; next }
	after_defconfig && /^[[:space:]]*grep / {
		sub(/^[[:space:]]*/, "")
		print
		count++
		if (count == 2) exit
	}
' "$action")

if [ "$(printf '%s\n' "$select_commands" | grep -c '^grep ')" -ne 2 ]; then
	fail "composite action must contain two executable package-selection checks"
fi

mkdir -p "$test_tmp/sdk"
printf '%s\n' \
	'CONFIG_PACKAGE_liquid-formula=m' \
	'CONFIG_PACKAGE_luci-app-liquid-formula=m' > "$test_tmp/sdk/.config"
if (
	cd "$test_tmp/sdk"
	MAIN_PACKAGE=liquid-formula
	LUCI_PACKAGE=luci-app-liquid-formula
	export MAIN_PACKAGE LUCI_PACKAGE
	eval "$select_commands"
) >/dev/null 2>&1; then
	pass "package-selection checks execute successfully for both modules"
else
	fail "package-selection checks execute successfully for both modules"
fi

printf '%s\n' 'CONFIG_PACKAGE_liquid-formula=m' > "$test_tmp/sdk/.config"
if (
	cd "$test_tmp/sdk"
	MAIN_PACKAGE=liquid-formula
	LUCI_PACKAGE=luci-app-liquid-formula
	export MAIN_PACKAGE LUCI_PACKAGE
	eval "$select_commands"
) >/dev/null 2>&1; then
	fail "package-selection checks reject a missing LuCI module"
else
	pass "package-selection checks reject a missing LuCI module"
fi

assert_contains "$workflow" \
	'group: release-${{ github.workflow }}-${{ needs.test.outputs.pkg_version }}' \
	"release concurrency is keyed by package version across refs"
assert_contains "$workflow" 'queue: max' \
	"concurrent web uploads and releases queue instead of replacing pending runs"

mkdir -p "$test_tmp/bin" "$test_tmp/assets"
printf 'package\n' > "$test_tmp/assets/liquid-formula_${pkg_version}_test.ipk"
printf 'notes\n' > "$test_tmp/NOTES.md"

cat > "$test_tmp/bin/gh" <<'MOCK'
#!/bin/sh
set -eu

if [ "$1" = api ]; then
	endpoint=$2
	case "$endpoint" in
		*/git/ref/tags/*)
			case "$MOCK_SCENARIO" in
				no_tag|orphan_release)
					echo 'gh: Reference does not exist (HTTP 404)' >&2
					exit 1
					;;
				ref_error)
					echo 'gh: transport failure (HTTP 503)' >&2
					exit 1
					;;
				*)
					exit 0
					;;
			esac
			;;
		*/commits/*)
			printf '%s\n' "$MOCK_TAG_SHA"
			;;
		*/releases/tags/*)
			case "$MOCK_SCENARIO" in
				existing|mismatch|orphan_release)
					exit 0
					;;
				*)
					echo 'gh: Release not found (HTTP 404)' >&2
					exit 1
					;;
			esac
			;;
		*)
			echo "unexpected gh api endpoint: $endpoint" >&2
			exit 2
			;;
	esac
elif [ "$1" = release ]; then
	shift
	printf '%s\n' "$*" >> "$MOCK_GH_LOG"
else
	echo "unexpected gh command: $*" >&2
	exit 2
fi
MOCK
chmod 0755 "$test_tmp/bin/gh"

expected_sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
other_sha=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

run_publisher()
{
	scenario=$1
	tag_sha=$2
	: > "$test_tmp/gh.log"
	PATH="$test_tmp/bin:$PATH" \
	GITHUB_REPOSITORY=example/liquid-formula \
	MOCK_SCENARIO="$scenario" \
	MOCK_TAG_SHA="$tag_sha" \
	MOCK_GH_LOG="$test_tmp/gh.log" \
	sh "$publisher" "$release_tag" "$expected_sha" \
		"$test_tmp/assets" "$test_tmp/NOTES.md"
}

if run_publisher existing "$expected_sha" >/dev/null 2>&1; then
	assert_contains "$test_tmp/gh.log" "upload $release_tag" \
		"an existing release is updated only after its tag matches the build SHA"
else
	fail "matching existing release should be updated"
fi

if run_publisher mismatch "$other_sha" >/dev/null 2>&1; then
	fail "mismatched existing tag must stop publication"
else
	if [ -s "$test_tmp/gh.log" ]; then
		fail "mismatched existing tag must not mutate a release"
	fi
	pass "mismatched existing tag stops publication before mutation"
fi

if run_publisher tag_only "$expected_sha" >/dev/null 2>&1; then
	assert_contains "$test_tmp/gh.log" \
		"create $release_tag $test_tmp/assets/liquid-formula_${pkg_version}_test.ipk --target $expected_sha" \
		"a matching pre-existing tag can receive a new release"
else
	fail "matching pre-existing tag should receive a release"
fi

if run_publisher no_tag "$expected_sha" >/dev/null 2>&1; then
	assert_contains "$test_tmp/gh.log" "--target $expected_sha" \
		"a new tag is created at the exact build SHA"
else
	fail "absent tag should be created at the build SHA"
fi

if run_publisher ref_error "$expected_sha" >/dev/null 2>&1; then
	fail "non-404 tag lookup errors must stop publication"
else
	if [ -s "$test_tmp/gh.log" ]; then
		fail "tag lookup errors must not mutate a release"
	fi
	pass "non-404 tag lookup errors fail closed"
fi

if run_publisher orphan_release "$expected_sha" >/dev/null 2>&1; then
	fail "a release with a missing tag must not be updated"
else
	if [ -s "$test_tmp/gh.log" ]; then
		fail "an orphaned release must not be mutated"
	fi
	pass "an orphaned release is rejected"
fi

echo "release workflow tests: ok"
