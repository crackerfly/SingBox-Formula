#!/bin/sh
set -u

REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$REPO_ROOT/tests/shell/harness.sh"

RPC="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula"
ACL="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/share/rpcd/acl.d/luci-app-liquid-formula.json"
OVERVIEW="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js"
MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/Makefile"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
: > "$TMP/functions.sh"
cat > "$TMP/updater" <<'EOF'
#!/bin/sh
sleep 2
printf 'run\n' >> "$SBF_TEST_RUNS"
EOF
chmod 0755 "$TMP/updater"
cat > "$TMP/process-start" <<'EOF'
#!/bin/sh
[ "$1" = 999999 ] && exit 1
printf '%s\n' "$1"
EOF
chmod 0755 "$TMP/process-start"

SBF_FUNCTIONS_SH="$TMP/functions.sh" "$RPC" list > "$TMP/list.json"
assert_command_success 'rpc list is valid JSON' python3 -m json.tool "$TMP/list.json"
for method in status service_action generate refresh check update list_templates read_template write_template delete_template; do
	assert_contains "$TMP/list.json" "\"$method\"" "publishes narrow RPC method: $method"
done
assert_not_contains "$TMP/list.json" '"action"' 'does not publish the legacy generic action method'
# The converter RPC needs no file capability, but the merged Custom Logo and
# upload features do. Assert the file grants stay scoped to the package's own
# paths instead of a wildcard root grant.
assert_not_contains "$ACL" '"/\*"' 'ACL grants no wildcard filesystem root'
assert_not_contains "$ACL" '"/etc/\*"' 'ACL grants no wildcard /etc access'
assert_contains "$ACL" '/etc/liquid-formula/assets' 'logo file grants are scoped to the package assets dir'
assert_contains "$ACL" '/var/run/liquid-formula-upload/' 'upload file grants are scoped to the package staging dir' 
assert_not_contains "$MAKEFILE" 'rpcd-mod-file' 'LuCI package no longer depends on rpcd-mod-file'
assert_contains "$RPC" 'STATE_DIR=.*\/var/run\/liquid-formula' 'runtime action state lives under /var/run'
assert_contains "$RPC" 'chmod 0700 "\$STATE_DIR"' 'runtime action state is root-only'
# 只匹配真正的调用, 不匹配解释为何不用它的注释。pgrep 会命中任何同名进程,
# 服务状态必须问 procd 的 running。
assert_not_contains "$RPC" '^[[:space:]]*[^#[:space:]].*pgrep' 'RPC service state never depends on pgrep'
assert_contains "$RPC" 'irqbalance" running' 'irqbalance state comes from procd, not a process name match'
assert_contains "$RPC" 'start manual' 'manual service starts bypass boot delay explicitly'
assert_contains "$RPC" '_action_timeout' 'background RPC workers derive their bound from current UCI settings'
assert_contains "$RPC" 'request_timeout=.*sub_timeout.*ACTION_TEMPLATE_COUNT' 'RPC timeout counts every enabled template'
assert_contains "$RPC" 'request_timeout \* 3' 'check/apply budget covers startup, refresh, and fetch'
assert_not_contains "$RPC" '^ACTION_TIMEOUT=900$' 'background RPC workers no longer use a fixed 900-second ceiling'
assert_contains "$RPC" 'ACTION_TIMEOUT_FALLBACK=11160' 'fallback also covers the largest valid dynamic operation'
assert_contains "$RPC" "service.*singbox-subscribe-convert" 'health response verifies converter identity'
assert_contains "$RPC" 'config_digest' 'status exposes a content digest'
assert_not_contains "$OVERVIEW" "method: 'action'" 'Overview uses split RPC methods'
assert_contains "$OVERVIEW" 'typeof res.code !== .number.' 'frontend rejects a missing or nonnumeric result code'
assert_contains "$OVERVIEW" "out !== 'queued'|Invalid asynchronous response" 'frontend rejects nonexact asynchronous acknowledgements'
assert_contains "$OVERVIEW" 'config_digest' 'Save & Apply is digest-driven'
assert_not_contains "$OVERVIEW" 'config_mtime' 'Save & Apply no longer coordinates by second-resolution mtime'
assert_contains "$OVERVIEW" "actionWaitSeconds: function\\(name\\)" 'frontend wait budget depends on the queued action'
assert_contains "$OVERVIEW" 'requestTimeout [*] 3 [+] 180' 'frontend check/update budget mirrors the RPC worker watchdog'
assert_contains "$OVERVIEW" "_\('Converter URL \(this device\)'\)" 'integration exposes the loopback converter URL'
assert_contains "$OVERVIEW" "_\('Converter URL \(LAN\)'\)" 'integration exposes the LAN converter URL'
assert_contains "$RPC" 'lan_url' 'status exposes a LAN converter URL'
assert_contains "$RPC" '_valid_ipv4' 'status validates the LAN address before publishing it'

# busybox 的 timeout applet 是可裁剪的。缺了它, worker 会以 127 退出而
# updater 根本不运行 —— 没有任何日志, 界面只报一个不透明的失败。
assert_contains "$RPC" '_run_with_timeout' "background work goes through the timeout wrapper"
assert_contains "$RPC" 'command -v timeout' "rpcd detects whether the timeout applet exists"
assert_not_contains "$RPC" '^[[:space:]]*timeout "\$action_timeout"' "rpcd never calls the timeout applet unconditionally"
assert_contains "$RPC" 'is not executable' "rpcd rejects a non executable updater with a readable message"
assert_contains "$RPC" '2>"\$ACTION_ERR"' "worker stderr is kept instead of discarded"

# Behavioral check: one enabled template at the maximum subscription timeout gives the
# updater 1,260 seconds per HTTP phase, so apply must be allowed 3,960 seconds overall.
# This is intentionally above the old fixed 900-second watchdog.
cat > "$TMP/dynamic-functions.sh" <<'EOF'
config_load() {
	return 0
}
config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}" value=$default
	[ "$section.$option" != main.subscription_timeout ] || value=600
	eval "$destination=\$value"
}
config_get_bool() {
	eval "$1=1"
}
config_foreach() {
	"$1" template_one
}
EOF
mkdir "$TMP/dynamic-bin"
cat > "$TMP/dynamic-bin/timeout" <<'EOF'
#!/bin/sh
printf '%s\n' "$1" > "$SBF_TIMEOUT_CAPTURE"
shift
exec "$@"
EOF
chmod 0755 "$TMP/dynamic-bin/timeout"
mkdir "$TMP/dynamic-state"
PATH="$TMP/dynamic-bin:$PATH" \
SBF_FUNCTIONS_SH="$TMP/dynamic-functions.sh" \
SBF_UPDATER="$TMP/updater" \
SBF_STATE_DIR="$TMP/dynamic-state" \
SBF_TEST_RUNS="$TMP/dynamic-runs" \
SBF_TIMEOUT_CAPTURE="$TMP/dynamic-timeout" \
SBF_PROCESS_START_HELPER="$TMP/process-start" \
	"$RPC" call update </dev/null > "$TMP/dynamic.out" 2>&1
assert_contains "$TMP/dynamic.out" '"code":0,"output":"queued"' "dynamic-budget update dispatch succeeds"
n=0
while [ ! -s "$TMP/dynamic-timeout" ] && [ "$n" -lt 20 ]; do sleep 0.1; n=$((n + 1)); done
assert_file_content 3960 "$TMP/dynamic-timeout" "apply worker receives a timeout above the old 900-second ceiling"
n=0
while ! grep -q '^update done ' "$TMP/dynamic-state/action.state" 2>/dev/null && [ "$n" -lt 40 ]; do sleep 0.1; n=$((n + 1)); done

# 行为验证: 在没有 timeout 的 PATH 下, 动作必须真的执行完并以 0 结束。
NOTIMEOUT="$TMP/nobin"
mkdir -p "$NOTIMEOUT"
for tool in sh dash mktemp cat rm mkdir rmdir mv chmod date od tr awk sed grep head ls sleep kill wc cut find readlink dirname basename uci; do
	tool_path=$(command -v "$tool" 2>/dev/null) && ln -sf "$tool_path" "$NOTIMEOUT/$tool"
done
if [ -e "$NOTIMEOUT/timeout" ]; then
	record_failure "test fixture PATH really has no timeout applet"
else
	record_ok "test fixture PATH really has no timeout applet"
fi
rm -rf "$TMP/state2" "$TMP/runs2"
mkdir -p "$TMP/state2"
PATH="$NOTIMEOUT" \
SBF_FUNCTIONS_SH="$TMP/functions.sh" \
SBF_UPDATER="$TMP/updater" \
SBF_STATE_DIR="$TMP/state2" \
SBF_TEST_RUNS="$TMP/runs2" \
SBF_PROCESS_START_HELPER="$TMP/process-start" \
	"$RPC" call refresh </dev/null > "$TMP/notimeout.out" 2>&1
assert_contains "$TMP/notimeout.out" '"code":0,"output":"queued"' "dispatch succeeds without the timeout applet"
n=0
while [ ! -f "$TMP/runs2" ] && [ "$n" -lt 10 ]; do sleep 1; n=$((n + 1)); done
assert_file_exists "$TMP/runs2" "updater actually runs when the timeout applet is missing"

# momo bypass 开关。掩码必须写进 momo 的值里，但 mark/mask 本身可在两个
# DPI 页面修改，所以必须从 fakehttp/fakesip UCI 动态读取。
ACL="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/share/rpcd/acl.d/luci-app-liquid-formula.json"
UCI_DEFAULTS="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula"

assert_contains "$OVERVIEW" "uci.load\('momo'\)" "overview loads momo config before offering its bypass switches"
assert_contains "$OVERVIEW" "uci.load\('fakehttp'\)" "overview loads FakeHTTP mark settings"
assert_contains "$OVERVIEW" "uci.load\('fakesip'\)" "overview loads FakeSIP mark settings"
assert_contains "$OVERVIEW" "uci.get\\(entry.config, 'main', 'fwmark'\\)" "bypass mark comes from the DPI service UCI"
assert_contains "$OVERVIEW" "uci.get\\(entry.config, 'main', 'fwmask'\\)" "bypass mask comes from the DPI service UCI"
assert_contains "$OVERVIEW" "String\\(mark\\) \\+ '/' \\+ String\\(mask\\)" "momo receives both the dynamic mark and mask"
assert_contains "$OVERVIEW" 'FakeHTTP' "FakeHTTP is named so users know what the mark is for"
assert_contains "$OVERVIEW" 'FakeSIP' "FakeSIP is named so users know what the mark is for"
assert_contains "$OVERVIEW" "bypass_fwmark" "overview writes momo's bypass_fwmark list"
assert_not_contains "$OVERVIEW" "mark: '0x8000/0x8000'" "FakeHTTP bypass is not frozen at its package default"
assert_contains "$ACL" '"momo"' "ACL grants access to momo uci so the switches can apply"

# 模板事务的回滚只能碰它自己那个 section。模板锁拦不住普通的 Overview 保存,
# 整份 uci import 会把另一个会话刚提交的端口、密码悄悄回退成快照时的旧值。
assert_contains "$RPC" 'template_section_snapshot' "template transactions snapshot only their own section"
assert_contains "$RPC" 'template_section_restore' "template rollback restores only its own section"
assert_not_contains "$RPC" '^[[:space:]]*uci import' "template rollback never re-imports the whole config"

# restart 失败原先被吞掉并返回 ok:true, 磁盘变了而进程还在用旧模板。
assert_contains "$RPC" 'could not be restarted' "template operations report a failed converter restart"
assert_contains "$OVERVIEW" 'res.warning' "the page surfaces the restart warning"
assert_contains "$UCI_DEFAULTS" 'momo_bypass_fakehttp 1' "FakeHTTP bypass defaults to on"
assert_contains "$UCI_DEFAULTS" 'momo_bypass_fakesip 1' "FakeSIP bypass defaults to on"

mkdir "$TMP/responses"
i=1
while [ "$i" -le 20 ]; do
	SBF_FUNCTIONS_SH="$TMP/functions.sh" \
	SBF_UPDATER="$TMP/updater" \
	SBF_STATE_DIR="$TMP/state" \
	SBF_TEST_RUNS="$TMP/runs" \
	SBF_PROCESS_START_HELPER="$TMP/process-start" \
		"$RPC" call refresh </dev/null > "$TMP/responses/$i" &
	i=$((i + 1))
done
wait
n=0
while [ ! -f "$TMP/runs" ] && [ "$n" -lt 5 ]; do
	sleep 1
	n=$((n + 1))
done
queued=$(grep -l '"code":0,"output":"queued"' "$TMP"/responses/* 2>/dev/null | wc -l | tr -d '[:space:]')
busy=$(grep -l '"code":75' "$TMP"/responses/* 2>/dev/null | wc -l | tr -d '[:space:]')
assert_equal 1 "$queued" 'twenty parallel background calls produce exactly one lock winner'
assert_equal 19 "$busy" 'all parallel lock losers fail explicitly'
assert_file_line_count 1 "$TMP/runs" 'only the RPC lock winner reaches the updater'
assert_equal 700 "$(stat -c %a "$TMP/state")" 'runtime state directory is mode 0700'
assert_file_not_exists "$TMP/state/rpc-action.lock" 'worker releases the action lock after completion'
assert_contains "$TMP/state/action.state" '^refresh done 0 [0-9]+ [A-Za-z0-9._-]+$' 'worker publishes a complete atomic terminal state'

mkdir "$TMP/state/rpc-action.lock"
printf '999999 999999 stale-owner\n' > "$TMP/state/rpc-action.lock/owner"
SBF_FUNCTIONS_SH="$TMP/functions.sh" \
SBF_UPDATER="$TMP/updater" \
SBF_STATE_DIR="$TMP/state" \
SBF_TEST_RUNS="$TMP/runs" \
SBF_PROCESS_START_HELPER="$TMP/process-start" \
	"$RPC" call update </dev/null > "$TMP/stale-recovery.json"
assert_contains "$TMP/stale-recovery.json" '"code":0,"output":"queued"' 'a dead owner is recovered before queueing the next worker'
n=0
while [ "$(wc -l < "$TMP/runs")" -lt 2 ] && [ "$n" -lt 5 ]; do sleep 1; n=$((n + 1)); done
assert_file_line_count 2 "$TMP/runs" 'recovered dead ownership still runs exactly one new updater'

finish_tests
