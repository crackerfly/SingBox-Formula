#!/bin/sh

source_file_matches_manifest_hash() {
	integrity_file=$1
	integrity_expected_hash=$2
	integrity_relative_path=$3
	integrity_allowlist=$4

	integrity_actual_hash=$(sha256sum "$integrity_file") || return 2
	integrity_actual_hash=${integrity_actual_hash%% *}
	[ "$integrity_actual_hash" = "$integrity_expected_hash" ] && return 0

	grep -Fqx "$integrity_relative_path" "$integrity_allowlist" || return 1
	integrity_last_byte=$(tail -c 1 "$integrity_file" | od -An -tu1 | tr -d '[:space:]') || return 2
	[ "$integrity_last_byte" = 10 ] || return 1

	integrity_without_lf_hash=$(head -c -1 "$integrity_file" | sha256sum) || return 2
	integrity_without_lf_hash=${integrity_without_lf_hash%% *}
	[ "$integrity_without_lf_hash" = "$integrity_expected_hash" ]
}

source_file_ensure_web_single_lf_variant() {
	fixture_file=$1
	fixture_expected_hash=$2
	fixture_relative_path=$3
	fixture_allowlist=$4

	grep -Fqx "$fixture_relative_path" "$fixture_allowlist" || return 1
	fixture_actual_hash=$(sha256sum "$fixture_file") || return 2
	fixture_actual_hash=${fixture_actual_hash%% *}
	if [ "$fixture_actual_hash" = "$fixture_expected_hash" ]; then
		printf '\n' >> "$fixture_file" || return 2
		return 0
	fi

	source_file_matches_manifest_hash \
		"$fixture_file" \
		"$fixture_expected_hash" \
		"$fixture_relative_path" \
		"$fixture_allowlist"
}
