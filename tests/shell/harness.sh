#!/bin/sh

TEST_ASSERTIONS=0
TEST_FAILURES=0

record_ok() {
	TEST_ASSERTIONS=$((TEST_ASSERTIONS + 1))
	printf 'ok %s - %s\n' "$TEST_ASSERTIONS" "$1"
}

record_failure() {
	TEST_ASSERTIONS=$((TEST_ASSERTIONS + 1))
	TEST_FAILURES=$((TEST_FAILURES + 1))
	printf 'not ok %s - %s\n' "$TEST_ASSERTIONS" "$1" >&2
}

assert_file_exists() {
	if [ -f "$1" ]; then
		record_ok "$2"
	else
		record_failure "$2 (missing: $1)"
	fi
}

assert_file_not_exists() {
	if [ ! -e "$1" ]; then
		record_ok "$2"
	else
		record_failure "$2 (unexpected: $1)"
	fi
}

assert_file_content() {
	expected=$1
	file=$2
	description=$3

	if [ ! -f "$file" ]; then
		record_failure "$description (missing: $file)"
		return
	fi

	actual=$(cat "$file")
	if [ "$actual" = "$expected" ]; then
		record_ok "$description"
	else
		record_failure "$description (expected '$expected', got '$actual')"
	fi
}

assert_file_sha256() {
	expected_hash=$1
	file=$2
	description=$3

	if [ ! -f "$file" ]; then
		record_failure "$description (missing: $file)"
		return
	fi

	actual_hash=$(sha256sum "$file") || {
		record_failure "$description (could not hash: $file)"
		return
	}
	actual_hash=${actual_hash%% *}
	if [ "$actual_hash" = "$expected_hash" ]; then
		record_ok "$description"
	else
		record_failure "$description (expected $expected_hash, got $actual_hash)"
	fi
}

assert_file_line_count() {
	expected_count=$1
	file=$2
	description=$3

	if [ ! -f "$file" ]; then
		record_failure "$description (missing: $file)"
		return
	fi

	actual_count=$(wc -l < "$file")
	actual_count=$(printf '%s' "$actual_count" | tr -d '[:space:]')
	if [ "$actual_count" = "$expected_count" ]; then
		record_ok "$description"
	else
		record_failure "$description (expected $expected_count, got $actual_count)"
	fi
}

assert_files_equal() {
	expected_file=$1
	actual_file=$2
	description=$3

	if [ -f "$expected_file" ] && [ -f "$actual_file" ] && cmp -s "$expected_file" "$actual_file"; then
		record_ok "$description"
	else
		record_failure "$description"
		if [ -f "$expected_file" ] && [ -f "$actual_file" ]; then
			diff -u "$expected_file" "$actual_file" >&2 || true
		fi
	fi
}

assert_empty() {
	value=$1
	description=$2

	if [ -z "$value" ]; then
		record_ok "$description"
	else
		record_failure "$description (found: $value)"
	fi
}

assert_equal() {
	expected=$1
	actual=$2
	description=$3

	if [ "$actual" = "$expected" ]; then
		record_ok "$description"
	else
		record_failure "$description (expected '$expected', got '$actual')"
	fi
}

assert_not_equal() {
	unexpected=$1
	actual=$2
	description=$3

	if [ "$actual" != "$unexpected" ]; then
		record_ok "$description"
	else
		record_failure "$description (unexpected '$actual')"
	fi
}

assert_command_success() {
	description=$1
	shift

	if "$@"; then
		record_ok "$description"
	else
		rc=$?
		record_failure "$description (exit $rc)"
	fi
}

assert_command_failure() {
	description=$1
	shift

	if "$@"; then
		record_failure "$description (unexpected success)"
	else
		record_ok "$description"
	fi
}

assert_contains() {
	file=$1
	pattern=$2
	description=$3

	if [ -f "$file" ] && grep -Eq -- "$pattern" "$file"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

assert_not_contains() {
	file=$1
	pattern=$2
	description=$3

	if [ -f "$file" ] && ! grep -Eq -- "$pattern" "$file"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

make_top_level() {
	awk '
		/^[[:space:]]*define[[:space:]]+/ { depth++; next }
		depth > 0 && /^[[:space:]]*endef[[:space:]]*$/ { depth--; next }
		depth == 0 { print }
	' "$1"
}

make_named_block() {
	awk -v target="$2" '
		$0 == "define " target { found = 1; inside = 1; next }
		inside && $0 == "endef" { inside = 0; exit }
		inside { print }
		END { if (!found) exit 1 }
	' "$1"
}

assert_make_top_level_contains() {
	file=$1
	pattern=$2
	description=$3

	if [ -f "$file" ] && make_top_level "$file" | grep -Eq "$pattern"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

assert_make_top_level_not_contains() {
	file=$1
	pattern=$2
	description=$3

	if [ -f "$file" ] && ! make_top_level "$file" | grep -Eq "$pattern"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

assert_make_top_level_order() {
	file=$1
	first_pattern=$2
	second_pattern=$3
	description=$4

	if [ ! -f "$file" ]; then
		record_failure "$description (missing: $file)"
		return
	fi

	top_level_content=$(make_top_level "$file")
	first_line=$(printf '%s\n' "$top_level_content" | grep -nEm1 "$first_pattern" || true)
	second_line=$(printf '%s\n' "$top_level_content" | grep -nEm1 "$second_pattern" || true)
	first_line=${first_line%%:*}
	second_line=${second_line%%:*}
	if [ -n "$first_line" ] && [ -n "$second_line" ] && [ "$first_line" -lt "$second_line" ]; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

assert_make_block_contains() {
	file=$1
	block=$2
	pattern=$3
	description=$4

	if [ -f "$file" ] && make_named_block "$file" "$block" | grep -Eq "$pattern"; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

finish_tests() {
	if [ "$TEST_FAILURES" -ne 0 ]; then
		printf '%s assertions, %s failures\n' "$TEST_ASSERTIONS" "$TEST_FAILURES" >&2
		return 1
	fi

	printf '%s assertions, 0 failures\n' "$TEST_ASSERTIONS"
}
