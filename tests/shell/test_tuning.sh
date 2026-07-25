#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
LUCI="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula"
APPLY="$LUCI/root/usr/share/liquid-formula/apply-tuning.sh"
RPC="$LUCI/root/usr/libexec/rpcd/liquid_formula"
VIEW="$LUCI/root/www/luci-static/resources/view/liquid-formula/customlogo.js"
MENU="$LUCI/root/usr/share/luci/menu.d/luci-app-liquid-formula.json"
ACL="$LUCI/root/usr/share/rpcd/acl.d/luci-app-liquid-formula.json"
DEFAULTS="$LUCI/root/etc/uci-defaults/99-luci-app-liquid-formula"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-tuning.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

. "$SCRIPT_DIR/harness.sh"

assert_file_exists "$APPLY" "tuning apply helper exists"
assert_file_exists "$LUCI/root/etc/config/tuning" "tuning config ships with defaults"

# --- menu ---------------------------------------------------------------------

assert_contains "$MENU" '"Tuning Utility"' "the first tab is named Tuning Utility"
assert_not_contains "$MENU" '"Custom Logo"' "the old tab title is gone from the menu"
# 页面内的 Custom Logo 功能本身必须保留。
assert_contains "$VIEW" "_\\('Enable Custom Logo'\\)" "the Custom Logo feature itself is untouched"
assert_contains "$VIEW" "form.Map\\('customlogo'" "the page still edits the customlogo config"

# --- harness ------------------------------------------------------------------

BIN="$TEST_TMP/bin"
mkdir -p "$BIN" "$TEST_TMP/etc/sysctl.d" "$TEST_TMP/init.d"

cat > "$BIN/uci" <<'EOF'
#!/bin/sh
store="$MOCK_UCI_STORE"
touch "$store"
[ "${1:-}" = "-q" ] && shift
case "${1:-}" in
	get)
		value=$(grep "^$2=" "$store" | tail -1 | cut -d= -f2-)
		[ -n "$value" ] || exit 1
		printf '%s\n' "$value"
		;;
	set)
		key=${2%%=*}; value=${2#*=}
		grep -v "^$key=" "$store" > "$store.new" 2>/dev/null
		mv "$store.new" "$store"
		printf '%s=%s\n' "$key" "$value" >> "$store"
		;;
esac
exit 0
EOF

cat > "$BIN/sysctl" <<'EOF'
#!/bin/sh
[ "${1:-}" = "-w" ] || exit 1
key=${2%%=*}; value=${2#*=}
case " ${MOCK_SYSCTL_REJECT:-} " in
	*" $key "*) exit 1 ;;
esac
printf '%s=%s\n' "$key" "$value" >> "$MOCK_APPLIED"
exit 0
EOF

cat > "$TEST_TMP/init.d/irqbalance" <<'EOF'
#!/bin/sh
printf 'irqbalance %s\n' "${1:-}" >> "$MOCK_IRQ_LOG"
exit 0
EOF

chmod 0755 "$BIN/uci" "$BIN/sysctl" "$TEST_TMP/init.d/irqbalance"

MOCK_UCI_STORE="$TEST_TMP/uci.store"
MOCK_APPLIED="$TEST_TMP/applied.log"
MOCK_IRQ_LOG="$TEST_TMP/irq.log"
export MOCK_UCI_STORE MOCK_APPLIED MOCK_IRQ_LOG

run_apply() {
	rm -f "$MOCK_APPLIED" "$MOCK_IRQ_LOG"
	PATH="$BIN:$PATH" \
	LFAPP_SYSCTL_DROPIN="$TEST_TMP/etc/sysctl.d/99-liquid-formula.conf" \
	LFAPP_SYSCTL_CONF="$TEST_TMP/etc/sysctl.conf" \
	LFAPP_INIT_DIR="$TEST_TMP/init.d" \
		sh "$APPLY" >"$TEST_TMP/apply.out" 2>"$TEST_TMP/apply.err"
}

write_config() {
	cat > "$MOCK_UCI_STORE" <<EOF
tuning.main.enabled=${1:-1}
tuning.main.tcp_fastopen=${2:-3}
tuning.main.default_qdisc=${3:-cake}
tuning.main.congestion_control=${4:-bbr}
tuning.main.tcp_max_syn_backlog=${5:-512}
tuning.main.irqbalance=${6:-1}
EOF
}

# 模拟用户手工跑过那段脚本之后的 sysctl.conf。
write_legacy_sysctl_conf() {
	cat > "$TEST_TMP/etc/sysctl.conf" <<'EOF'
# a setting this package does not own
net.ipv4.ip_forward = 1
kernel.panic = 3

# Custom Network Optimization
net.ipv4.tcp_fastopen = 3
net.core.default_qdisc = cake
net.ipv4.tcp_congestion_control = bbr
net.ipv4.tcp_max_syn_backlog = 512
EOF
}

# --- applying -----------------------------------------------------------------

write_config
write_legacy_sysctl_conf
run_apply
assert_equal 0 "$?" "apply succeeds when every key is accepted"
assert_contains "$TEST_TMP/apply.out" '^ok$' "apply reports success"

DROPIN="$TEST_TMP/etc/sysctl.d/99-liquid-formula.conf"
assert_file_exists "$DROPIN" "apply writes the sysctl drop-in"
assert_contains "$APPLY" 'mv "\$dropin_tmp" "\$DROPIN"' "the drop-in is installed atomically instead of truncated in place"
assert_contains "$DROPIN" 'net.ipv4.tcp_fastopen = 3' "drop-in carries tcp_fastopen"
assert_contains "$DROPIN" 'net.core.default_qdisc = cake' "drop-in carries the qdisc"
assert_contains "$DROPIN" 'net.ipv4.tcp_congestion_control = bbr' "drop-in carries the congestion control"
assert_contains "$DROPIN" 'net.ipv4.tcp_max_syn_backlog = 512' "drop-in carries the SYN backlog"
assert_contains "$MOCK_APPLIED" 'net.core.default_qdisc=cake' "apply pushes values into the running kernel"

# /etc/sysctl.conf 在 /etc/sysctl.d/ 之后加载, 残留的同名键会压过 drop-in。
assert_not_contains "$TEST_TMP/etc/sysctl.conf" 'tcp_congestion_control' "managed keys are removed from sysctl.conf"
assert_not_contains "$TEST_TMP/etc/sysctl.conf" 'Custom Network Optimization' "the hand-written block header is removed too"
# 但绝不能顺手删掉不属于本包的设置。
assert_contains "$TEST_TMP/etc/sysctl.conf" 'net.ipv4.ip_forward = 1' "unrelated sysctl.conf entries are preserved"
assert_contains "$TEST_TMP/etc/sysctl.conf" 'kernel.panic = 3' "unrelated sysctl.conf entries stay intact"
assert_file_exists "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "the original sysctl.conf is backed up"

assert_contains "$MOCK_IRQ_LOG" 'irqbalance restart' "irqbalance is restarted when enabled"
assert_contains "$MOCK_UCI_STORE" 'irqbalance.irqbalance.enabled=1' "irqbalance own config is switched on"

# --- idempotence --------------------------------------------------------------

before=$(wc -l < "$DROPIN")
run_apply
run_apply
after=$(wc -l < "$DROPIN")
assert_equal "$before" "$after" "repeated applies never grow the drop-in"

backup_first=$(cat "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak")
run_apply
assert_equal "$backup_first" "$(cat "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak")" \
	"the first backup is not overwritten by later applies"

# --- a missing kernel module --------------------------------------------------
# cake without kmod-sched-cake must not stop the other three from applying.

write_config
# 变量赋值前缀作用在函数上时会留在当前 shell(POSIX 行为), 所以显式收尾。
MOCK_SYSCTL_REJECT='net.core.default_qdisc'
export MOCK_SYSCTL_REJECT
run_apply
apply_rc=$?
unset MOCK_SYSCTL_REJECT
assert_equal 2 "$apply_rc" "a rejected key reports partial application"
assert_contains "$TEST_TMP/apply.out" 'partial net.core.default_qdisc' "the rejected key is named"
assert_contains "$MOCK_APPLIED" 'net.ipv4.tcp_congestion_control=bbr' "other keys still apply when one is rejected"

# --- disabling ----------------------------------------------------------------

write_config 0 3 cake bbr 512 0
run_apply
assert_equal 0 "$?" "disabling succeeds"
assert_contains "$TEST_TMP/apply.out" '^disabled$' "apply reports the disabled state"
assert_file_not_exists "$DROPIN" "disabling removes the drop-in so it stops applying at boot"
assert_contains "$MOCK_IRQ_LOG" 'irqbalance disable' "irqbalance is disabled with it"
# 关闭管理应当把当初摘走的键还回去, 否则用户原本的持久化设置就永久丢了。
assert_contains "$TEST_TMP/etc/sysctl.conf" 'net.ipv4.tcp_congestion_control' "disabling restores the previously managed keys"
assert_contains "$TEST_TMP/etc/sysctl.conf" 'net.ipv4.ip_forward = 1' "restoring does not disturb unrelated entries"

# 反复禁用不应把同一批键追加两次。
run_apply
restored_count=$(grep -c 'net.ipv4.tcp_congestion_control' "$TEST_TMP/etc/sysctl.conf")
assert_equal 1 "$restored_count" "repeated disables do not duplicate the restored keys"

# --- input validation ---------------------------------------------------------
# 这些值会进入 sysctl.conf 语法和 sysctl -w 的参数。

write_config 1 3 'cake; rm -rf /' bbr 512 0
run_apply
assert_equal 1 "$?" "a shell metacharacter in the qdisc is rejected"

write_config 1 99 cake bbr 512 0
run_apply
assert_equal 1 "$?" "an out-of-range tcp_fastopen is rejected"

write_config 1 3 cake bbr notanumber 0
run_apply
assert_equal 1 "$?" "a non-numeric SYN backlog is rejected"

write_config 1 3 cake '../../etc/passwd' 512 0
run_apply
assert_equal 1 "$?" "a path traversal in the congestion control is rejected"

write_config 1 3 cake bbr 8 0
run_apply
assert_equal 1 "$?" "an implausibly small SYN backlog is rejected"

# --- rpcd surface -------------------------------------------------------------

assert_contains "$RPC" 'tuning_status' "rpcd publishes the tuning status method"
assert_contains "$RPC" 'tuning_apply' "rpcd publishes the tuning apply method"
assert_contains "$RPC" 'sysctl_conf_conflict' "status reports whether sysctl.conf overrides the drop-in"
assert_contains "$RPC" 'cake_module' "status reports whether the cake module is present"
assert_contains "$RPC" 'bbr_module' "status reports whether the bbr module is present"

assert_contains "$ACL" '"tuning"' "ACL grants access to the tuning config"
assert_contains "$ACL" '"irqbalance"' "ACL grants access to the irqbalance config"

# --- view wiring --------------------------------------------------------------

assert_contains "$VIEW" 'tuning_status' "the page reads live kernel state"
assert_contains "$VIEW" 'tuning_apply' "the page applies through the helper"
assert_contains "$VIEW" 'handleSaveApply' "saving also pushes the values into the kernel"
assert_contains "$VIEW" 'kmod-sched-cake' "the page explains the cake module requirement"
assert_contains "$VIEW" 'kmod-tcp-bbr' "the page explains the bbr module requirement"

# 状态同步: 装包时默认值取自系统实际值, 而不是写死的推荐值。
assert_contains "$DEFAULTS" 'seed_from_proc' "install seeds tuning defaults from the running kernel"
assert_contains "$DEFAULTS" '/proc/sys/net/ipv4/tcp_congestion_control' "the congestion control default mirrors the live value"

finish_tests
