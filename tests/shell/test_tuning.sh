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
MAKEFILE_LUCI="$LUCI/Makefile"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-tuning.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

OPENWRT_RELEASE="$TEST_TMP/openwrt_release"
printf "DISTRIB_RELEASE='25.12.0'\n" > "$OPENWRT_RELEASE"

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
LFAPP_OPENWRT_RELEASE="$OPENWRT_RELEASE" \
LFAPP_TUNING_LOCK="$TEST_TMP/tuning.lock" \
		sh "$APPLY" >"$TEST_TMP/apply.out" 2>"$TEST_TMP/apply.err"
}

run_apply_at() {
	local dropin="$1" sysctl_conf="$2" action="${3:-apply}"
	rm -f "$MOCK_APPLIED" "$MOCK_IRQ_LOG"
	PATH="$BIN:$PATH" \
LFAPP_SYSCTL_DROPIN="$dropin" \
LFAPP_SYSCTL_CONF="$sysctl_conf" \
LFAPP_INIT_DIR="$TEST_TMP/init.d" \
LFAPP_OPENWRT_RELEASE="$OPENWRT_RELEASE" \
LFAPP_GREP_BIN="${MOCK_GREP_BIN:-grep}" \
LFAPP_FLOCK_BIN="${MOCK_FLOCK_BIN:-flock}" \
LFAPP_TUNING_LOCK="${MOCK_LOCK_FILE:-$TEST_TMP/tuning.lock}" \
LFAPP_TUNING_LOCK_WAIT="${MOCK_LOCK_WAIT:-30}" \
		sh "$APPLY" "$action" >"$TEST_TMP/apply-at.out" 2>"$TEST_TMP/apply-at.err"
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
assert_contains "$APPLY" 'mv -f "\$DROPIN_NEW" "\$DROPIN"' "the drop-in is installed atomically instead of truncated in place"
assert_contains "$APPLY" 'mv -f "\$SYSCTL_NEW" "\$SYSCTL_CONF"' "sysctl.conf is replaced atomically"
assert_not_contains "$APPLY" 'cat "\$tmp" > "\$SYSCTL_CONF"' "sysctl.conf is never truncated in place"
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

# 所有文件必须先准备成功再提交。目标目录不可创建时，原先的实现已经摘掉
# sysctl.conf 里的键；现在失败必须保持原文件和备份状态不变。
TX_APPLY="$TEST_TMP/tx-apply"
mkdir -p "$TX_APPLY"
cp "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "$TX_APPLY/sysctl.conf"
cp "$TX_APPLY/sysctl.conf" "$TX_APPLY/sysctl.before"
: > "$TX_APPLY/not-a-directory"
write_config
run_apply_at "$TX_APPLY/not-a-directory/99-liquid-formula.conf" "$TX_APPLY/sysctl.conf"
assert_equal 1 "$?" "apply reports a drop-in staging failure"
assert_files_equal "$TX_APPLY/sysctl.before" "$TX_APPLY/sysctl.conf" "failed apply leaves sysctl.conf untouched"
assert_file_not_exists "$TX_APPLY/sysctl.conf.liquid-formula.bak" "failed apply does not publish a backup for an uncommitted transaction"

cat > "$BIN/grep-error" <<'EOF'
#!/bin/sh
exit 2
EOF
chmod 0755 "$BIN/grep-error"
TX_GREP="$TEST_TMP/tx-grep"
mkdir -p "$TX_GREP/sysctl.d"
cp "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "$TX_GREP/sysctl.conf"
cp "$TX_GREP/sysctl.conf" "$TX_GREP/sysctl.before"
MOCK_GREP_BIN="$BIN/grep-error"
run_apply_at "$TX_GREP/sysctl.d/99-liquid-formula.conf" "$TX_GREP/sysctl.conf"
grep_apply_rc=$?
unset MOCK_GREP_BIN
assert_equal 1 "$grep_apply_rc" "a sysctl.conf inspection error fails apply closed"
assert_files_equal "$TX_GREP/sysctl.before" "$TX_GREP/sysctl.conf" "inspection failure leaves sysctl.conf untouched"
assert_file_not_exists "$TX_GREP/sysctl.d/99-liquid-formula.conf" "inspection failure rolls back the staged drop-in"

# 缺锁工具或锁竞争都必须在读写持久化状态前失败。
TX_NOLOCK="$TEST_TMP/tx-no-lock"
mkdir -p "$TX_NOLOCK/sysctl.d"
cp "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "$TX_NOLOCK/sysctl.conf"
cp "$TX_NOLOCK/sysctl.conf" "$TX_NOLOCK/sysctl.before"
MOCK_FLOCK_BIN="$TX_NOLOCK/missing-flock"
run_apply_at "$TX_NOLOCK/sysctl.d/99-liquid-formula.conf" "$TX_NOLOCK/sysctl.conf"
no_flock_rc=$?
unset MOCK_FLOCK_BIN
assert_not_equal 0 "$no_flock_rc" "a missing flock implementation fails closed"
assert_files_equal "$TX_NOLOCK/sysctl.before" "$TX_NOLOCK/sysctl.conf" "lock failure leaves sysctl.conf untouched"
assert_file_not_exists "$TX_NOLOCK/sysctl.d/99-liquid-formula.conf" "lock failure publishes no drop-in"

cat > "$BIN/uci-block" <<'EOF'
#!/bin/sh
touch "$MOCK_LOCK_ENTERED"
while [ ! -e "$MOCK_LOCK_RELEASE" ]; do sleep 1; done
exec "$MOCK_REAL_UCI" "$@"
EOF
chmod 0755 "$BIN/uci-block"
TX_LOCK="$TEST_TMP/tx-lock"
mkdir -p "$TX_LOCK/sysctl.d"
cp "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "$TX_LOCK/sysctl.conf"
MOCK_LOCK_ENTERED="$TX_LOCK/entered"
MOCK_LOCK_RELEASE="$TX_LOCK/release"
MOCK_REAL_UCI="$BIN/uci"
export MOCK_LOCK_ENTERED MOCK_LOCK_RELEASE MOCK_REAL_UCI
PATH="$BIN:$PATH" \
LFAPP_UCI_BIN="$BIN/uci-block" \
LFAPP_SYSCTL_DROPIN="$TX_LOCK/sysctl.d/99-liquid-formula.conf" \
LFAPP_SYSCTL_CONF="$TX_LOCK/sysctl.conf" \
LFAPP_INIT_DIR="$TEST_TMP/init.d" \
LFAPP_TUNING_LOCK="$TX_LOCK/transaction.lock" \
LFAPP_TUNING_LOCK_WAIT=5 \
	sh "$APPLY" >"$TX_LOCK/first.out" 2>"$TX_LOCK/first.err" &
first_pid=$!
lock_wait=0
while [ ! -e "$MOCK_LOCK_ENTERED" ] && [ "$lock_wait" -lt 10 ]; do
	sleep 1
	lock_wait=$((lock_wait + 1))
done
MOCK_LOCK_FILE="$TX_LOCK/transaction.lock"
MOCK_LOCK_WAIT=0
run_apply_at "$TX_LOCK/sysctl.d/99-liquid-formula.conf" "$TX_LOCK/sysctl.conf"
busy_rc=$?
unset MOCK_LOCK_FILE MOCK_LOCK_WAIT
assert_equal 75 "$busy_rc" "a concurrent tuning transaction fails explicitly as busy"
touch "$MOCK_LOCK_RELEASE"
wait "$first_pid"
assert_equal 0 "$?" "the lock owner completes after the contender is rejected"

# 原子 rename 不能以改变权限为代价。
TX_MODE="$TEST_TMP/tx-mode"
mkdir -p "$TX_MODE/sysctl.d"
cp "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "$TX_MODE/sysctl.conf"
chmod 0600 "$TX_MODE/sysctl.conf"
run_apply_at "$TX_MODE/sysctl.d/99-liquid-formula.conf" "$TX_MODE/sysctl.conf"
assert_equal 0 "$?" "atomic sysctl.conf apply succeeds"
assert_equal 600 "$(stat -c '%a' "$TX_MODE/sysctl.conf")" "atomic sysctl.conf replacement preserves its mode"

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
assert_file_not_exists "$TEST_TMP/etc/sysctl.conf.liquid-formula.bak" "a completed disable retires the backup"

# 反复禁用不应把同一批键追加两次。
run_apply
restored_count=$(grep -c 'net.ipv4.tcp_congestion_control' "$TEST_TMP/etc/sysctl.conf")
assert_equal 1 "$restored_count" "repeated disables do not duplicate the restored keys"

# 关闭/卸载要先成功准备恢复文件，才能撤掉 drop-in。sysctl.conf 被换成符号链接
# 时 helper 会拒绝修改；持久化调优必须原样保留，prerm 也必须中止卸载。
TX_DISABLE="$TEST_TMP/tx-disable"
mkdir -p "$TX_DISABLE/sysctl.d"
write_legacy_sysctl_conf
cp "$TEST_TMP/etc/sysctl.conf" "$TX_DISABLE/sysctl.conf"
write_config 1 3 cake bbr 512 0
run_apply_at "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/sysctl.conf"
assert_equal 0 "$?" "transaction fixture enables tuning"
mv "$TX_DISABLE/sysctl.conf" "$TX_DISABLE/sysctl.real"
ln -s "$TX_DISABLE/sysctl.real" "$TX_DISABLE/sysctl.conf"
write_config 0 3 cake bbr 512 0
run_apply_at "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/sysctl.conf"
assert_equal 1 "$?" "disable refuses an unsafe sysctl.conf restore"
assert_file_exists "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "failed disable preserves the active drop-in"
run_apply_at "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/sysctl.conf" restore
assert_equal 1 "$?" "uninstall restore reports the same preparation failure"
assert_file_exists "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "failed uninstall restore preserves the active drop-in"
rm "$TX_DISABLE/sysctl.conf"
mv "$TX_DISABLE/sysctl.real" "$TX_DISABLE/sysctl.conf"

# grep 的 1 是“没有匹配”，2 才是读取失败；读取失败绝不能被当成无需恢复。
MOCK_GREP_BIN="$BIN/grep-error"
run_apply_at "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/sysctl.conf"
grep_error_rc=$?
unset MOCK_GREP_BIN
assert_equal 1 "$grep_error_rc" "a backup inspection error fails closed"
assert_file_exists "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "an unreadable backup cannot cause drop-in deletion"

# 快照 cp 会跟随链接、回滚 mv 会把链接变成普通文件，因此两个包管理路径都
# 选择明确拒绝链接，保持对象及其目标不变。
mv "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/dropin.real"
ln -s "$TX_DISABLE/dropin.real" "$TX_DISABLE/sysctl.d/99-liquid-formula.conf"
run_apply_at "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/sysctl.conf"
assert_equal 1 "$?" "disable rejects a symlink drop-in"
assert_equal "$TX_DISABLE/dropin.real" "$(readlink "$TX_DISABLE/sysctl.d/99-liquid-formula.conf")" "failed disable preserves the drop-in symlink"
rm "$TX_DISABLE/sysctl.d/99-liquid-formula.conf"
mv "$TX_DISABLE/dropin.real" "$TX_DISABLE/sysctl.d/99-liquid-formula.conf"

mv "$TX_DISABLE/sysctl.conf.liquid-formula.bak" "$TX_DISABLE/backup.real"
ln -s "$TX_DISABLE/backup.real" "$TX_DISABLE/sysctl.conf.liquid-formula.bak"
run_apply_at "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "$TX_DISABLE/sysctl.conf"
assert_equal 1 "$?" "disable rejects a symlink backup"
assert_equal "$TX_DISABLE/backup.real" "$(readlink "$TX_DISABLE/sysctl.conf.liquid-formula.bak")" "failed disable preserves the backup symlink"
assert_file_exists "$TX_DISABLE/sysctl.d/99-liquid-formula.conf" "a symlink backup cannot cause drop-in deletion"
assert_contains "$APPLY" 'LOCK_FILE=.*\/var\/lock\/liquid-formula-tuning.lock' "apply and restore share a dedicated production lock"
assert_not_contains "$APPLY" 'flock.*\/dev\/null' "tuning never substitutes the global /dev/null inode for its lock"
assert_contains "$MAKEFILE_LUCI" 'refusing to uninstall because kernel tuning could not be restored' "prerm aborts instead of deleting the drop-in after restore failure"
assert_not_contains "$MAKEFILE_LUCI" 'rm -f /etc/sysctl.d/99-liquid-formula.conf' "prerm has no unconditional second deletion"
restore_line=$(grep -n 'apply-tuning.sh restore' "$MAKEFILE_LUCI" | head -n1 | cut -d: -f1)
logo_line=$(grep -n '/etc/init.d/liquid-formula-logo stop' "$MAKEFILE_LUCI" | head -n1 | cut -d: -f1)
if [ -n "$restore_line" ] && [ -n "$logo_line" ] && [ "$restore_line" -lt "$logo_line" ]; then
	record_ok "prerm restores tuning before stopping the logo service"
else
	record_failure "prerm restores tuning before stopping the logo service"
fi

# 兼容旧版本遗留的陈旧备份：没有活跃 drop-in 时，备份不再代表待恢复状态。
TX_STALE="$TEST_TMP/tx-stale"
mkdir -p "$TX_STALE/sysctl.d"
cat > "$TX_STALE/sysctl.conf" <<'EOF'
net.ipv4.ip_forward = 1
EOF
cat > "$TX_STALE/sysctl.conf.liquid-formula.bak" <<'EOF'
net.ipv4.ip_forward = 1
net.ipv4.tcp_congestion_control = reno
EOF
write_config 1 3 cake bbr 512 0
run_apply_at "$TX_STALE/sysctl.d/99-liquid-formula.conf" "$TX_STALE/sysctl.conf"
assert_equal 0 "$?" "a new management cycle ignores an old inactive backup"
assert_file_not_exists "$TX_STALE/sysctl.conf.liquid-formula.bak" "the stale backup is invalidated on successful enable"
write_config 0 3 cake bbr 512 0
run_apply_at "$TX_STALE/sysctl.d/99-liquid-formula.conf" "$TX_STALE/sysctl.conf"
assert_not_contains "$TX_STALE/sysctl.conf" 'tcp_congestion_control' "later disable does not resurrect a setting the user deleted"

# 备份必须跟着每次摘除刷新。否则"启用 -> 关闭 -> 用户改了托管键 -> 再启用 ->
# 再关闭"会把第一次的旧值还回去, 覆盖掉用户后来的设置。
write_config 1 3 cake bbr 512 0
run_apply
sed -i 's/^net.ipv4.ip_forward = 1$/net.ipv4.ip_forward = 0/' "$TEST_TMP/etc/sysctl.conf"
printf 'net.ipv4.tcp_congestion_control = reno\n' >> "$TEST_TMP/etc/sysctl.conf"
run_apply
write_config 0 3 cake bbr 512 0
run_apply
assert_contains "$TEST_TMP/etc/sysctl.conf" 'tcp_congestion_control = reno' "the restore uses the most recent backup, not the first one"
assert_contains "$TEST_TMP/etc/sysctl.conf" 'net.ipv4.ip_forward = 0' "unrelated edits made between runs survive"

# 只剩部分托管键时, 缺的那些也要补回来, 不能因为有一个还在就整批跳过。
write_config 1 3 cake bbr 512 0
run_apply
printf 'net.ipv4.tcp_fastopen = 1\n' >> "$TEST_TMP/etc/sysctl.conf"
write_config 0 3 cake bbr 512 0
run_apply
assert_contains "$TEST_TMP/etc/sysctl.conf" 'tcp_congestion_control' "a partially present key set is completed, not skipped"

# 卸载路径: restore 子命令必须撤掉 drop-in 并还回托管键。
write_config 1 3 cake bbr 512 0
run_apply
rm -f "$MOCK_APPLIED"
PATH="$BIN:$PATH" \
LFAPP_SYSCTL_DROPIN="$TEST_TMP/etc/sysctl.d/99-liquid-formula.conf" \
LFAPP_SYSCTL_CONF="$TEST_TMP/etc/sysctl.conf" \
LFAPP_INIT_DIR="$TEST_TMP/init.d" \
LFAPP_TUNING_LOCK="$TEST_TMP/tuning.lock" \
	sh "$APPLY" restore >"$TEST_TMP/restore.out" 2>&1
assert_equal 0 "$?" "the restore subcommand succeeds"
assert_contains "$TEST_TMP/restore.out" '^restored$' "the restore subcommand reports what it did"
assert_file_not_exists "$DROPIN" "uninstall removes the drop-in"
assert_contains "$TEST_TMP/etc/sysctl.conf" 'tcp_congestion_control' "uninstall gives the managed keys back"
assert_contains "$MAKEFILE_LUCI" 'apply-tuning.sh restore' "the LuCI prerm calls the restore path"

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

write_config 1 3 fq bbr 512 0
run_apply
assert_equal 1 "$?" "an unlisted queueing discipline is rejected"
assert_contains "$TEST_TMP/apply.err" 'invalid default_qdisc: fq' "the qdisc rejection names the invalid value"

write_config 1 3 cake westwood 512 0
run_apply
assert_equal 1 "$?" "an unlisted congestion control is rejected"
assert_contains "$TEST_TMP/apply.err" 'invalid congestion_control: westwood' "the congestion rejection names the invalid value"

write_config 1 3 cake bbr 129 0
run_apply
assert_equal 1 "$?" "an unlisted SYN backlog is rejected"
assert_contains "$TEST_TMP/apply.err" 'invalid tcp_max_syn_backlog: 129' "the backlog rejection names the invalid value"

printf "DISTRIB_RELEASE='24.10.4'\n" > "$OPENWRT_RELEASE"
write_config 1 3 cake_mq bbr 512 0
run_apply
assert_equal 1 "$?" "cake_mq is rejected before OpenWrt 25.12"
assert_contains "$TEST_TMP/apply.err" 'cake_mq requires OpenWrt 25.12 or newer' "the release gate explains the cake_mq requirement"

printf "DISTRIB_RELEASE='snapshot'\n" > "$OPENWRT_RELEASE"
run_apply
assert_equal 1 "$?" "cake_mq is rejected when the OpenWrt release is unknown"

printf "DISTRIB_RELEASE='25.12.0'\n" > "$OPENWRT_RELEASE"
run_apply
assert_equal 0 "$?" "cake_mq is accepted on OpenWrt 25.12"

printf "DISTRIB_RELEASE='26.1.0'\n" > "$OPENWRT_RELEASE"
run_apply
assert_equal 0 "$?" "cake_mq is accepted after OpenWrt 25.12"

# --- rpcd surface -------------------------------------------------------------

assert_contains "$RPC" 'tuning_status' "rpcd publishes the tuning status method"
assert_contains "$RPC" 'tuning_apply' "rpcd publishes the tuning apply method"
assert_contains "$RPC" 'sysctl_conf_conflict' "status reports whether sysctl.conf overrides the drop-in"
assert_contains "$RPC" 'cake_module' "status reports whether the cake module is present"
assert_contains "$RPC" 'bbr_module' "status reports whether the bbr module is present"
assert_contains "$RPC" 'openwrt_release' "status reports the OpenWrt release for version-gated controls"

assert_contains "$ACL" '"tuning"' "ACL grants access to the tuning config"
assert_contains "$ACL" '"irqbalance"' "ACL grants access to the irqbalance config"

# --- view wiring --------------------------------------------------------------

assert_contains "$VIEW" 'tuning_status' "the page reads live kernel state"
assert_contains "$VIEW" 'tuning_apply' "the page applies through the helper"
assert_contains "$VIEW" 'handleSaveApply' "saving also pushes the values into the kernel"
assert_contains "$VIEW" 'uci\.changes\(\)' "saving checks the official per-session UCI delta"
assert_contains "$VIEW" "'uci-applied'" "pending tuning waits for the official committed-UCI event"
assert_not_contains "$VIEW" "this\.super\('handleSaveApply'" "saving does not duplicate the official apply call"
assert_contains "$VIEW" 'kmod-sched-cake' "the page explains the cake module requirement"
assert_contains "$VIEW" 'kmod-tcp-bbr' "the page explains the bbr module requirement"

assert_contains "$VIEW" "s\\.option\\(form\\.ListValue, 'tuning_default_qdisc'" "qdisc uses a fixed dropdown"
assert_contains "$VIEW" "s\\.option\\(form\\.ListValue, 'tuning_congestion_control'" "congestion control uses a fixed dropdown"
assert_contains "$VIEW" "s\\.option\\(form\\.ListValue, 'tuning_backlog'" "SYN backlog uses a fixed dropdown"
assert_contains "$VIEW" "o\\.value\\('cake_mq'" "qdisc dropdown contains cake_mq"
assert_contains "$VIEW" "o\\.value\\('cubic'" "congestion dropdown contains cubic"
assert_contains "$VIEW" "o\\.value\\('reno'" "congestion dropdown contains reno"
assert_contains "$VIEW" "o\\.value\\('128'" "backlog dropdown contains 128"
assert_contains "$VIEW" "o\\.value\\('2048'" "backlog dropdown contains 2048"
assert_contains "$VIEW" 'supportsCakeMq' "cake_mq visibility uses an explicit release gate"

# 固定默认值只补真正缺失的 UCI 项；显式空值也属于用户状态，不能覆盖。
assert_not_contains "$DEFAULTS" '/proc/sys/net/core/default_qdisc' "qdisc default no longer mirrors an arbitrary live value"
assert_not_contains "$DEFAULTS" '/proc/sys/net/ipv4/tcp_congestion_control' "congestion default no longer mirrors an arbitrary live value"
assert_not_contains "$DEFAULTS" '/proc/sys/net/ipv4/tcp_max_syn_backlog' "backlog default no longer mirrors an arbitrary live value"
assert_contains "$DEFAULTS" '^seed_missing default_qdisc cake$' "fresh qdisc default is cake"
assert_contains "$DEFAULTS" '^seed_missing congestion_control bbr$' "fresh congestion default is bbr"
assert_contains "$DEFAULTS" '^seed_missing tcp_max_syn_backlog 512$' "fresh backlog default is 512"
assert_contains "$DEFAULTS" 'uci -q get "tuning.main.\$option" >/dev/null 2>&1' "fixed defaults distinguish missing options from explicit empty values"

finish_tests
