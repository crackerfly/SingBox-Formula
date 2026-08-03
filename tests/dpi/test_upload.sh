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

# --- upload lock recovery ----------------------------------------------------
# uhttpd SIGKILLs a CGI child once script_timeout expires, so the EXIT trap
# never runs and the lock directory survives. Before 1.8.8 that made every
# later upload fail with 409 until the next reboot.

run_upload() {
	CONTENT_LENGTH=128 \
	QUERY_STRING='kind=logo&name=brand.png' \
	LFAPP_STAGING_DIR="$TMP/staging" \
	LFAPP_ASSET_DIR="$TMP/assets" \
	LFAPP_PAYLOAD_DIR="$TMP/payloads" \
	LFAPP_CGI_UPLOAD="$TMP/fake-cgi-upload" \
	TF_TEST_FIXTURE="$TMP/one.png" \
		"$CGI" </dev/null >"$1"
}

# A successful run must leave nothing behind.
[ ! -e "$TMP/staging/.lock" ]

# An owner record naming a process that no longer exists is recoverable.
mkdir "$TMP/staging/.lock"
printf '999999 4294967295\n' >"$TMP/staging/.lock/owner"
out="$TMP/stale-owner.out"
run_upload "$out"
grep -q '"ok":true' "$out"
[ ! -e "$TMP/staging/.lock" ]

# A lock directory with no owner record at all — either a pre-1.8.8 leftover or
# a writer killed between mkdir and publication — is also recoverable.
mkdir "$TMP/staging/.lock"
out="$TMP/no-owner.out"
run_upload "$out"
grep -q '"ok":true' "$out"
[ ! -e "$TMP/staging/.lock" ]

# A truncated or otherwise unparsable owner record must not wedge the lock.
mkdir "$TMP/staging/.lock"
printf 'not-a-pid\n' >"$TMP/staging/.lock/owner"
out="$TMP/bad-owner.out"
run_upload "$out"
grep -q '"ok":true' "$out"
[ ! -e "$TMP/staging/.lock" ]

# A live owner still blocks, and the blocked run must not delete the holder's
# claim on its way out.
sleep 30 &
holder=$!
holder_start=$(awk '{ sub(/^.*\) /, ""); split($0, f, " "); print f[20] }' "/proc/$holder/stat")
mkdir "$TMP/staging/.lock"
printf '%s %s\n' "$holder" "$holder_start" >"$TMP/staging/.lock/owner"
out="$TMP/live-owner.out"
run_upload "$out"
grep -qi 'another upload is in progress' "$out"
[ -f "$TMP/staging/.lock/owner" ]
kill "$holder" 2>/dev/null || true
wait "$holder" 2>/dev/null || true

# Once that holder is gone the very next upload recovers the lock.
out="$TMP/after-holder.out"
run_upload "$out"
grep -q '"ok":true' "$out"
[ ! -e "$TMP/staging/.lock" ]

echo "upload tests: ok"
