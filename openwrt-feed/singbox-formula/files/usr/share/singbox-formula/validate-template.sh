#!/bin/sh
# Validate a singbox-formula template after replacing converter placeholders.

umask 077

TEMPLATE=${1-}
TMP_ROOT=${SBF_TMP_ROOT:-${TMPDIR:-/tmp}}
TMP=

cleanup() {
	[ -n "$TMP" ] && rm -f "$TMP"
}

trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

[ -n "$TEMPLATE" ] && [ -f "$TEMPLATE" ] && [ ! -L "$TEMPLATE" ] || {
	printf 'template file not found or unsafe\n' >&2
	exit 1
}
command -v jsonfilter >/dev/null 2>&1 || {
	printf 'jsonfilter is required\n' >&2
	exit 1
}
mkdir -p "$TMP_ROOT" || exit 1
TMP=$(mktemp "$TMP_ROOT/sbsc-template-check.XXXXXX") || exit 1

sed -r \
	-e 's/\{\{[[:space:]]*Nodes[[:space:]]*\}\}/{"tag":"__dummy_node__","type":"direct"}/g' \
	-e 's/\{\{[[:space:]]*"[^"]*"[[:space:]]*\|[[:space:]]*NotesName[[:space:]]*\}\}/"➜ Direct"/g' \
	"$TEMPLATE" > "$TMP" || exit 1

jsonfilter -i "$TMP" -e '@.outbounds' >/dev/null 2>&1 || {
	printf 'template JSON check failed\n' >&2
	exit 1
}

exit 0
