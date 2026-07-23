#!/bin/sh
# 订阅 User-Agent 探测器
#
# 用法:
#   ./probe-subscription.sh                 # 从 UCI 读取 subscription_url
#   ./probe-subscription.sh '<订阅链接>'    # 直接指定链接
#
# 依次用常见客户端 UA 拉取订阅，报告每次拿到的内容格式与体量，
# 帮你快速定位机场认哪个 UA。只读，不修改任何配置。

set -u

URL="${1:-}"
if [ -z "$URL" ]; then
	URL=$(uci -q get singbox_formula.main.subscription_url 2>/dev/null)
fi
[ -n "$URL" ] || {
	echo "用法: $0 '<订阅链接>'   (或先在 UCI 里配置 singbox_formula.main.subscription_url)" >&2
	exit 2
}

TIMEOUT="${PROBE_TIMEOUT:-25}"
WORK=$(mktemp -d /tmp/sbf-probe.XXXXXX) || exit 1
trap 'rm -rf "$WORK"' 0 INT TERM HUP

UA_LIST='sing-box 1.11.0
SFI/1.11.0 (sing-box 1.11.0)
SFA/1.11.0 (sing-box 1.11.0)
mihomo/1.19.0
clash-verge/v2.0.3
ClashMetaForAndroid/2.11.0.Meta
Clash/v1.18.0
v2rayN/7.0.0
v2rayNG/1.9.16
Shadowrocket/2.2.35
Stash/2.7.0
Loon/3.2.0
Karing/1.0.0'

# classify <文件> -> 打印格式判断
classify() {
	local file="$1" head_bytes
	head_bytes=$(head -c 400 "$file" 2>/dev/null)

	case "$head_bytes" in
		'{'*|'['*)
			if grep -q '"outbounds"' "$file" 2>/dev/null; then
				printf 'sing-box JSON  (outbounds=%s)' "$(grep -o '"tag"' "$file" | wc -l)"
			else
				printf 'JSON (无 outbounds)'
			fi
			return
			;;
	esac
	case "$head_bytes" in
		*'proxies:'*|*'mixed-port:'*|*'proxy-groups:'*)
			printf 'Clash/Meta YAML'
			return
			;;
	esac
	case "$head_bytes" in
		*'://'*)
			printf '明文 URI 列表  (%s 行)' "$(grep -c '://' "$file")"
			return
			;;
	esac

	# 尝试 base64
	if base64 -d < "$file" 2>/dev/null | head -c 200 | grep -q '://'; then
		printf 'base64 URI 列表  (%s 个节点)' \
			"$(base64 -d < "$file" 2>/dev/null | grep -c '://')"
		return
	fi

	# 面板的文字错误提示通常很短
	if [ "$(wc -c < "$file")" -lt 300 ]; then
		printf '疑似错误提示: %s' "$(tr -d '\r\n' < "$file" | head -c 120)"
		return
	fi
	printf '未识别 (%s 字节)' "$(wc -c < "$file")"
}

probe_one() {
	local ua="$1" target="$2" body="$WORK/body" code size
	rm -f "$body"
	code=$(curl -sS -o "$body" -w '%{http_code}' \
		--connect-timeout 8 --max-time "$TIMEOUT" \
		-H "User-Agent: $ua" -H 'Accept: */*' \
		"$target" 2>"$WORK/err") || code="ERR"

	if [ "$code" = "ERR" ]; then
		printf 'HTTP -    %s\n' "$(head -c 80 "$WORK/err" | tr -d '\r\n')"
		return
	fi
	size=$(wc -c < "$body" 2>/dev/null || echo 0)
	if [ "$code" != "200" ]; then
		printf 'HTTP %-4s %s\n' "$code" "$(tr -d '\r\n' < "$body" | head -c 100)"
		return
	fi
	printf 'HTTP %-4s %-7s %s\n' "$code" "${size}B" "$(classify "$body")"
}

echo "订阅探测: $URL"
echo "超时: ${TIMEOUT}s"
echo
printf '%s\n' "----------------------------------------------------------------------"

printf '%s\n' "$UA_LIST" | while IFS= read -r ua; do
	[ -n "$ua" ] || continue
	printf '%-34s ' "$ua"
	probe_one "$ua" "$URL"
done

printf '%s\n' "----------------------------------------------------------------------"
cat <<'TIP'
怎么读这张表:
  * "sing-box JSON"      -> 最理想, 把该 UA 填进 LuCI 的「Subscription User-Agent」
  * "base64 URI 列表"    -> 也完全可用, 转换器会自动解码成 sing-box outbounds
  * "明文 URI 列表"      -> 同上, 直接可用
  * "Clash/Meta YAML"    -> 本转换器不解析, 换一个 UA
  * "疑似错误提示"       -> 该 UA 被机场拒绝(版本过低/不支持), 换下一个
TIP
