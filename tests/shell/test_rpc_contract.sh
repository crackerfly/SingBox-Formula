#!/bin/sh
set -u

REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$REPO_ROOT/tests/shell/harness.sh"

RPC="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula"
ACL="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/share/rpcd/acl.d/luci-app-liquid-formula.json"
OVERVIEW="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/overview.js"
MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/Makefile"
BUDGET_CASES="$REPO_ROOT/tests/subscription/budget-cases.tsv"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
cat > "$TMP/functions.sh" <<'EOF'
config_load() { return 0; }
config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}"
	eval "$destination=\$default"
}
config_get_bool() { config_get "$@"; }
config_foreach() { return 0; }
config_list_foreach() {
	[ "$1.$2" = main.subscription_url ] || return 0
	"$3" 'https://provider.example/sub'
}
EOF
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
assert_contains "$RPC" '_action_checked_multiply "\$ACTION_TEMPLATE_COUNT" "\$sub_timeout"' 'RPC timeout counts every enabled template'
assert_contains "$RPC" '_action_checked_multiply "\$request_timeout" 3' 'refresh budget covers refresh, lock acquisition, and synchronization'
assert_contains "$RPC" '_action_checked_multiply "\$request_timeout" 5' 'check/apply budget covers startup, refresh, lock acquisition, synchronization, and fetch'
assert_not_contains "$RPC" '^ACTION_TIMEOUT=900$' 'background RPC workers no longer use a fixed 900-second ceiling'
assert_not_contains "$RPC" 'ACTION_TIMEOUT_FALLBACK|11160|3660' 'RPC removed fixed timeout caps and fallbacks'
assert_contains "$RPC" "service.*singbox-subscribe-convert" 'health response verifies converter identity'
assert_contains "$RPC" 'config_digest' 'status exposes a content digest'
assert_not_contains "$OVERVIEW" "method: 'action'" 'Overview uses split RPC methods'
assert_contains "$OVERVIEW" 'typeof res.code !== .number.' 'frontend rejects a missing or nonnumeric result code'
assert_contains "$OVERVIEW" "out !== 'queued'|Invalid asynchronous response" 'frontend rejects nonexact asynchronous acknowledgements'
assert_contains "$OVERVIEW" 'config_digest' 'Save & Apply is digest-driven'
assert_not_contains "$OVERVIEW" 'config_mtime' 'Save & Apply no longer coordinates by second-resolution mtime'
assert_contains "$OVERVIEW" "actionWaitSeconds: function\\(name\\)" 'frontend wait budget depends on the queued action'
assert_contains "$OVERVIEW" 'checkedMultiply\(requestTimeout, 3\)' 'frontend refresh budget mirrors the RPC worker watchdog'
assert_contains "$OVERVIEW" 'checkedMultiply\(requestTimeout, 5\)' 'frontend check/update budget mirrors the RPC worker watchdog'
assert_contains "$OVERVIEW" "_\('Converter URL \(this device\)'\)" 'integration exposes the loopback converter URL'
assert_contains "$OVERVIEW" "_\('Converter URL \(LAN\)'\)" 'integration exposes the LAN converter URL'
assert_contains "$RPC" 'lan_url' 'status exposes a LAN converter URL'
assert_contains "$RPC" '_valid_ipv4' 'status validates the LAN address before publishing it'
assert_contains "$RPC" 'config_list_foreach main subscription_url' 'RPC derives subscription state from the ordered UCI URL list'
assert_not_contains "$RPC" 'aggregate\.json|objects/.*\.json' 'RPC never loads subscription aggregate or object payloads while polling status'
assert_contains "$RPC" 'config_get sub_timeout main subscription_timeout 60' 'RPC keeps one global subscription timeout for action budgets'
assert_not_contains "$RPC" 'config_list_foreach main subscription_timeout' 'RPC never turns the global timeout into a per-URL list'
assert_not_contains "$RPC" 'config_list_foreach main user_agent' 'RPC never turns the global User-Agent into a per-URL list'
assert_not_contains "$RPC" 'config_list_foreach main refresh_interval' 'RPC never turns the global refresh interval into a per-URL list'

cat > "$TMP/status-functions.sh" <<'EOF'
config_load() { return 0; }
config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}" value="$default"
	case "$section.$option" in
		main.enabled) value=1 ;;
		main.port) value=9716 ;;
		main.password) value='router-password' ;;
		main.subscription_url) value='' ;;
		main.default_template) value=momo_template ;;
		main.output_config) value=/etc/momo/profiles/config.json ;;
	esac
	eval "$destination=\$value"
}
config_get_bool() { config_get "$@"; }
config_foreach() { return 0; }
config_list_foreach() {
	[ "$1.$2" = 'main.subscription_url' ] || return 0
	"$3" 'https://private.example/sub?token=O%27Brien&region=東京'
	"$3" 'https://backup.example/sub?password=hidden'
}
EOF
cat > "$TMP/status-init" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$TMP/status-init"
printf 'generated gateway config\n' > "$TMP/status-config.yaml"
status_config_digest=$(sha256sum "$TMP/status-config.yaml")
status_config_digest=${status_config_digest%% *}
cat > "$TMP/status-gateway" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" > "$SBF_STATUS_ARGUMENTS"
[ "$1" = status ] || exit 2
[ "$2" = --config ] || exit 2
[ "$3" = "$SBF_EXPECTED_CONFIG" ] || exit 2
[ "$4" = --expected-digest ] || exit 2
[ "$5" = "$SBF_EXPECTED_DIGEST" ] || exit 2
printf '%s\n' '{"schema":1,"overall_state":"failed","config_match":true,"active_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","total_sources":2,"fresh_count":1,"fallback_indices":[2],"sources":[{"index":1,"result":"fresh","fetch_code":"ok","format":"singbox-json","accepted":4,"skipped":0,"warnings":[]},{"index":2,"result":"fallback","fetch_code":"http_status","format":"clash-yaml","accepted":3,"skipped":1,"warnings":[{"code":"invalid_field","node_index":7,"type":"vmess","field":"port"}]}],"last_attempt":{"state":"failed","total_sources":2,"failure_stage":"source_fetch","code":"source_unavailable","fetch_code":"http_status","source_index":2,"preserved":true}}'
EOF
chmod 0755 "$TMP/status-gateway"
SBF_FUNCTIONS_SH="$TMP/status-functions.sh" \
SBF_INIT_SCRIPT="$TMP/status-init" \
SBF_SUBSCRIPTION_GATEWAY="$TMP/status-gateway" \
SBF_GENERATED_CONFIG="$TMP/status-config.yaml" \
SBF_EXPECTED_CONFIG="$TMP/status-config.yaml" \
SBF_EXPECTED_DIGEST="$status_config_digest" \
SBF_STATUS_ARGUMENTS="$TMP/status-arguments" \
	"$RPC" call status </dev/null > "$TMP/subscription-status.json"
assert_command_success 'ordered-list status response remains valid JSON' python3 -m json.tool "$TMP/subscription-status.json"
assert_contains "$TMP/subscription-status.json" '"subscription_set":true' 'status reports a configured ordered URL list without reading a legacy scalar'
assert_not_contains "$TMP/subscription-status.json" 'private\.example|O%27Brien|東京' 'status never copies full subscription URLs into its summary'
assert_contains "$TMP/status-arguments" "^status --config $TMP/status-config.yaml --expected-digest $status_config_digest$" 'RPC binds the status CLI to the exact generated config digest'
assert_contains "$TMP/subscription-status.json" '"subscription":\{"schema":1,"overall_state":"failed","config_match":true' 'RPC exposes the safe overall subscription failure state'
assert_contains "$TMP/subscription-status.json" '"total_sources":2,"fresh_count":1,"fallback_indices":\[2\]' 'RPC exposes source totals and fallback indices'
assert_contains "$TMP/subscription-status.json" '"format":"clash-yaml","accepted":3,"skipped":1' 'RPC exposes bounded per-source normalization counts'
assert_contains "$TMP/subscription-status.json" '"failure_stage":"source_fetch","code":"source_unavailable","fetch_code":"http_status","source_index":2,"preserved":true' 'RPC exposes the bounded last-attempt failure contract'
assert_not_contains "$TMP/subscription-status.json" 'backup\.example|password=hidden|url_sha256|object_sha256|node_name|raw_error|"subscription":.*"config_digest"' 'subscription status exposes no URL, raw error, node name, object, or internal config digest'

cat > "$TMP/status-gateway-fail" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$TMP/status-gateway-fail"
SBF_FUNCTIONS_SH="$TMP/status-functions.sh" \
SBF_INIT_SCRIPT="$TMP/status-init" \
SBF_SUBSCRIPTION_GATEWAY="$TMP/status-gateway-fail" \
SBF_GENERATED_CONFIG="$TMP/status-config.yaml" \
	"$RPC" call status </dev/null > "$TMP/subscription-status-unavailable.json"
assert_command_success 'unavailable subscription status remains valid JSON' python3 -m json.tool "$TMP/subscription-status-unavailable.json"
assert_contains "$TMP/subscription-status-unavailable.json" '"subscription":\{"schema":1,"overall_state":"unavailable","config_match":false,"active_generation":"","total_sources":2' 'RPC fails closed to bounded unavailable metadata when the helper fails'

# busybox 的 timeout applet 是可裁剪的。缺了它, worker 会以 127 退出而
# updater 根本不运行 —— 没有任何日志, 界面只报一个不透明的失败。
assert_contains "$RPC" '_run_with_timeout' "background work goes through the timeout wrapper"
assert_contains "$RPC" 'command -v timeout' "rpcd detects whether the timeout applet exists"
assert_not_contains "$RPC" '^[[:space:]]*timeout "\$action_timeout"' "rpcd never calls the timeout applet unconditionally"
assert_contains "$RPC" 'is not executable' "rpcd rejects a non executable updater with a readable message"
assert_contains "$RPC" '2>"\$ACTION_ERR"' "worker stderr is kept instead of discarded"

# Behavioral check: one source and one enabled template at the maximum timeout gives
# A=660 and R=1,320, so apply must be allowed 6,900 seconds overall.
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
config_list_foreach() {
	[ "$1.$2" = main.subscription_url ] || return 0
	"$3" 'https://provider.example/sub'
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
assert_file_content 6900 "$TMP/dynamic-timeout" "apply worker receives the exact uncapped dynamic timeout"
n=0
while ! grep -q '^update done ' "$TMP/dynamic-state/action.state" 2>/dev/null && [ "$n" -lt 40 ]; do sleep 0.1; n=$((n + 1)); done

cat > "$TMP/budget-functions.sh" <<'EOF'
config_load() { return 0; }
config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}" value="$default"
	[ "$section.$option" != main.subscription_timeout ] || value=$BUDGET_TIMEOUT
	eval "$destination=\$value"
}
config_get_bool() { eval "$1=1"; }
config_list_foreach() {
	local callback="$3" index=0
	[ "$1.$2" = main.subscription_url ] || return 0
	while [ "$index" -lt "$BUDGET_SOURCES" ]; do
		"$callback" 'https://duplicate.example/sub' || return $?
		index=$((index + 1))
	done
}
config_foreach() {
	ACTION_TEMPLATE_COUNT=$BUDGET_TEMPLATES
	return 0
}
EOF
cat > "$TMP/budget-updater" <<'EOF'
#!/bin/sh
exit 0
EOF
mkdir "$TMP/budget-bin"
cat > "$TMP/budget-bin/timeout" <<'EOF'
#!/bin/sh
printf '%s\n' "$1" > "$BUDGET_CAPTURE"
shift
exec "$@"
EOF
chmod 0755 "$TMP/budget-updater" "$TMP/budget-bin/timeout"

while IFS='	' read -r budget_name budget_sources budget_timeout budget_templates \
	budget_request budget_refresh budget_apply; do
	case "$budget_name" in ''|'#'*) continue ;; esac
	for budget_method in refresh update; do
		if [ "$budget_method" = refresh ]; then
			budget_expected=$budget_refresh
		else
			budget_expected=$budget_apply
		fi
		budget_state="$TMP/budget-$budget_name-$budget_method"
		budget_capture="$TMP/budget-$budget_name-$budget_method.timeout"
		mkdir "$budget_state"
		PATH="$TMP/budget-bin:$PATH" \
		BUDGET_SOURCES="$budget_sources" \
		BUDGET_TIMEOUT="$budget_timeout" \
		BUDGET_TEMPLATES="$budget_templates" \
		BUDGET_CAPTURE="$budget_capture" \
		SBF_FUNCTIONS_SH="$TMP/budget-functions.sh" \
		SBF_UPDATER="$TMP/budget-updater" \
		SBF_STATE_DIR="$budget_state" \
		SBF_PROCESS_START_HELPER="$TMP/process-start" \
			"$RPC" call "$budget_method" </dev/null \
			> "$TMP/budget-$budget_name-$budget_method.json"
		if [ "$budget_expected" = invalid ]; then
			assert_contains \
				"$TMP/budget-$budget_name-$budget_method.json" \
				'"code":2' \
				"RPC budget fixture $budget_name/$budget_method rejects overflow"
			assert_file_not_exists \
				"$budget_capture" \
				"RPC budget fixture $budget_name/$budget_method performs no worker action"
		else
			assert_contains \
				"$TMP/budget-$budget_name-$budget_method.json" \
				'"code":0,"output":"queued"' \
				"RPC budget fixture $budget_name/$budget_method dispatches"
			n=0
			while [ ! -s "$budget_capture" ] && [ "$n" -lt 40 ]; do
				sleep 0.05
				n=$((n + 1))
			done
			assert_file_content \
				"$budget_expected" \
				"$budget_capture" \
				"RPC budget fixture $budget_name/$budget_method matches $budget_expected"
		fi
	done
done < "$BUDGET_CASES"

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
# The UI may preserve a dirty unavailable selection long enough for the user to
# choose another enabled template, but the RPC remains the final protection for
# persisted defaults. Keep these server-side guards independent of the LuCI
# refresh behavior.
assert_contains "$RPC" 'cannot disable current default_template' "backend still rejects disabling the persisted default template"
assert_contains "$RPC" 'cannot delete current default_template' "backend still rejects deleting the persisted default template"

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
n=0
while [ -e "$TMP/state/rpc-action.lock" ] && [ "$n" -lt 50 ]; do
	sleep 0.1
	n=$((n + 1))
done
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
