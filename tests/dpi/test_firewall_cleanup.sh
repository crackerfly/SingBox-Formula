#!/bin/sh
#
# 防火墙状态清理。
#
# FakeHTTP / FakeSIP 在 nft 不可用时会回退到 iptables, 在 mangle 表里建
# <NAME>_R/_S/_D 三条链并从 PREROUTING / POSTROUTING 跳进去。进程被 SIGKILL
# 或崩溃时它自己的拆除逻辑跑不到, 而清理函数原先只删 nft 表 —— 那些 mangle
# 钩子会永久残留, 下次启动还可能因为链已存在而失败。

set -u

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
COMMON="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/service-common.sh"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/lfdpi-firewall.XXXXXX")" || exit 1
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

checks=0
failures=0

ok() {
	checks=$((checks + 1))
	printf 'ok %s - %s\n' "$checks" "$1"
}

fail() {
	checks=$((checks + 1))
	failures=$((failures + 1))
	printf 'not ok %s - %s\n' "$checks" "$1"
}

expect_log() {
	if grep -Fq -e "$1" "$TMP/calls.log" 2>/dev/null; then
		ok "$2"
	else
		fail "$2"
	fi
}

refute_log() {
	if grep -Fq -e "$1" "$TMP/calls.log" 2>/dev/null; then
		fail "$2"
	else
		ok "$2"
	fi
}

mkdir -p "$TMP/bin"

# iptables 桩: 链存在, 且每条跳转规则只允许删一次。
cat > "$TMP/bin/iptables" <<'EOF'
#!/bin/sh
printf 'iptables %s\n' "$*" >> "$MOCK_LOG"
case "$*" in
	*"-n -L FAKEHTTP_S"*) exit 0 ;;
	*"-n -L FAKESIP_S"*) exit 0 ;;
	*"-D PREROUTING"*)
		[ ! -f "$MOCK_STATE/pre" ] || exit 1
		: > "$MOCK_STATE/pre"
		exit 0
		;;
	*"-D POSTROUTING"*)
		[ ! -f "$MOCK_STATE/post" ] || exit 1
		: > "$MOCK_STATE/post"
		exit 0
		;;
esac
exit 0
EOF

# ip6tables 桩: 链不存在, 整段应当被跳过。
cat > "$TMP/bin/ip6tables" <<'EOF'
#!/bin/sh
printf 'ip6tables %s\n' "$*" >> "$MOCK_LOG"
exit 1
EOF

cat > "$TMP/bin/nft" <<'EOF'
#!/bin/sh
printf 'nft %s\n' "$*" >> "$MOCK_LOG"
exit 0
EOF

chmod 0755 "$TMP/bin/iptables" "$TMP/bin/ip6tables" "$TMP/bin/nft"

MOCK_LOG="$TMP/calls.log"
MOCK_STATE="$TMP/state"
export MOCK_LOG MOCK_STATE

run_cleanup() {
	rm -rf "$MOCK_STATE" "$MOCK_LOG"
	mkdir -p "$MOCK_STATE"
	PATH="$TMP/bin:$PATH" sh -c '
		. "$1" 2>/dev/null || true
		tf_cleanup_firewall_state "$2"
	' _ "$COMMON" "$1"
}

run_cleanup fakehttp
cleanup_status=$?

if [ "$cleanup_status" = 0 ]; then
	ok "cleaning up fakehttp reports success"
else
	fail "cleaning up fakehttp reports success"
fi

expect_log 'nft delete table ip fakehttp' 'the nft IPv4 table is still removed'
expect_log 'nft delete table ip6 fakehttp' 'the nft IPv6 table is still removed'

# 链上还有引用时 -X 会失败, 所以顺序必须是 flush -> 摘跳转 -> 删链。
expect_log '-F FAKEHTTP_R' 'the iptables rule chain is flushed'
expect_log '-D PREROUTING -j FAKEHTTP_S' 'the PREROUTING jump is removed'
expect_log '-D POSTROUTING -j FAKEHTTP_D' 'the POSTROUTING jump is removed'
expect_log '-X FAKEHTTP_S' 'the source chain is deleted'
expect_log '-X FAKEHTTP_D' 'the destination chain is deleted'

order_ok=1
flush_line=$(grep -n -- '-F FAKEHTTP_S' "$MOCK_LOG" | head -n 1 | cut -d: -f1)
jump_line=$(grep -n -- '-D PREROUTING -j FAKEHTTP_S' "$MOCK_LOG" | head -n 1 | cut -d: -f1)
delete_line=$(grep -n -- '-X FAKEHTTP_S' "$MOCK_LOG" | head -n 1 | cut -d: -f1)
[ -n "$flush_line" ] && [ -n "$jump_line" ] && [ -n "$delete_line" ] || order_ok=0
[ "$order_ok" = 0 ] || [ "$flush_line" -lt "$jump_line" ] || order_ok=0
[ "$order_ok" = 0 ] || [ "$jump_line" -lt "$delete_line" ] || order_ok=0
if [ "$order_ok" = 1 ]; then
	ok 'teardown runs flush, then jump removal, then chain deletion'
else
	fail 'teardown runs flush, then jump removal, then chain deletion'
fi

# 反复异常退出会插入多条同样的跳转规则, 所以要一直删到失败为止。
repeat_count=$(grep -c -- '-D PREROUTING -j FAKEHTTP_S' "$MOCK_LOG")
if [ "$repeat_count" -ge 2 ]; then
	ok 'jump removal repeats until iptables reports nothing left'
else
	fail 'jump removal repeats until iptables reports nothing left'
fi

# 链不存在的地址族不该被继续操作。
refute_log 'ip6tables -w -t mangle -F' 'a family without the chains is skipped entirely'

run_cleanup fakesip
expect_log '-X FAKESIP_D' 'the same teardown covers fakesip'

# 未知服务名必须拒绝, 免得把别的东西删掉。
if run_cleanup evil-service >/dev/null 2>&1; then
	fail 'an unknown service name is rejected'
else
	ok 'an unknown service name is rejected'
fi

printf '%s checks, %s failures\n' "$checks" "$failures"
[ "$failures" = 0 ]
