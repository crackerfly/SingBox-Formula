#!/bin/sh
# Validate a singbox-subscribe-convert JSON template by replacing template placeholders
# with harmless dummy values and checking that the result is valid JSON.

TEMPLATE="$1"
[ -n "$TEMPLATE" ] && [ -f "$TEMPLATE" ] || { echo "template file not found" >&2; exit 1; }
TMP="/tmp/sbsc-template-check.$$"
# Replace converter placeholders with JSON-valid dummy values.
sed -r \
	-e 's/\{\{[[:space:]]*Nodes[[:space:]]*\}\}/{"tag":"__dummy_node__","type":"direct"}/g' \
	-e 's/\{\{[[:space:]]*"[^"]*"[[:space:]]*\|[[:space:]]*NotesName[[:space:]]*\}\}/"➜ Direct"/g' \
	"$TEMPLATE" > "$TMP" || exit 1
if command -v jsonfilter >/dev/null 2>&1; then
	jsonfilter -i "$TMP" -e '@.outbounds' >/dev/null 2>&1 || { rm -f "$TMP"; echo "template JSON check failed" >&2; exit 1; }
fi
rm -f "$TMP"
exit 0
