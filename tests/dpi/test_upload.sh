#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
CGI="$ROOT/openwrt-feed/luci-app-liquid-formula/root/www/cgi-bin/liquid-formula-upload"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

[ -x "$CGI" ] || {
	echo "missing executable upload CGI" >&2
	exit 1
}

mkdir -p "$TMP/staging" "$TMP/assets" "$TMP/payloads"

out="$TMP/oversize.out"
CONTENT_LENGTH=600000 \
QUERY_STRING='kind=logo&name=large.png' \
LFAPP_STAGING_DIR="$TMP/staging" \
LFAPP_ASSET_DIR="$TMP/assets" \
LFAPP_PAYLOAD_DIR="$TMP/payloads" \
LFAPP_CGI_UPLOAD="$TMP/never-called" \
	"$CGI" </dev/null >"$out"
grep -qi 'too large' "$out"

out="$TMP/reserved.out"
CONTENT_LENGTH=128 \
QUERY_STRING='kind=logo&name=default-logo.svg' \
LFAPP_STAGING_DIR="$TMP/staging" \
LFAPP_ASSET_DIR="$TMP/assets" \
LFAPP_PAYLOAD_DIR="$TMP/payloads" \
LFAPP_CGI_UPLOAD="$TMP/never-called" \
	"$CGI" </dev/null >"$out"
grep -qi 'reserved' "$out"

# A local process must not be able to redirect root's upload work through a
# pre-created final-component symlink.
cat >"$TMP/symlink-helper" <<'EOF'
#!/bin/sh
touch "$LFAPP_TEST_MARKER"
exit 1
EOF
chmod 755 "$TMP/symlink-helper"
rm -rf "$TMP/staging"
mkdir "$TMP/staging-target"
chmod 777 "$TMP/staging-target"
ln -s "$TMP/staging-target" "$TMP/staging"
out="$TMP/symlink.out"
CONTENT_LENGTH=128 \
QUERY_STRING='kind=logo&name=brand.png' \
LFAPP_STAGING_DIR="$TMP/staging" \
LFAPP_ASSET_DIR="$TMP/assets" \
LFAPP_PAYLOAD_DIR="$TMP/payloads" \
LFAPP_CGI_UPLOAD="$TMP/symlink-helper" \
LFAPP_TEST_MARKER="$TMP/helper-called" \
	"$CGI" </dev/null >"$out"
grep -qi 'unsafe upload directory' "$out"
[ ! -e "$TMP/helper-called" ]
[ "$(stat -c '%a' "$TMP/staging-target")" = '777' ]
rm "$TMP/staging"
mkdir "$TMP/staging"

# Minimal PNG signature + IHDR width/height (1 x 1); CRC/content is irrelevant
# to the upload gate, which intentionally validates structure rather than decoding.
printf '\211PNG\r\n\032\n\000\000\000\rIHDR\000\000\000\001\000\000\000\001' >"$TMP/one.png"

cat >"$TMP/fake-cgi-upload" <<'EOF'
#!/bin/sh
cp "$TF_TEST_FIXTURE" "$LFAPP_STAGING_DIR/pending-logo"
printf 'Status: 200 OK\r\nContent-Type: text/plain\r\n\r\n{}\n'
EOF
chmod 755 "$TMP/fake-cgi-upload"

out="$TMP/success.out"
CONTENT_LENGTH=128 \
QUERY_STRING='kind=logo&name=brand.png' \
LFAPP_STAGING_DIR="$TMP/staging" \
LFAPP_ASSET_DIR="$TMP/assets" \
LFAPP_PAYLOAD_DIR="$TMP/payloads" \
LFAPP_CGI_UPLOAD="$TMP/fake-cgi-upload" \
TF_TEST_FIXTURE="$TMP/one.png" \
	"$CGI" </dev/null >"$out"
grep -q '"path":"/etc/liquid-formula/assets/brand.png"' "$out"
cmp "$TMP/one.png" "$TMP/assets/brand.png"

echo "upload tests: ok"
