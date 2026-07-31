#!/bin/sh
#
# 从 luci-app-taoistfuchen 移植的 DPI / Custom Logo 行为回归。
#
# test_upload.sh 必须以 root 运行: 上传 CGI 只接受 root 拥有的私有目录,
# 用非特权用户跑等于把生产环境的安全检查削弱掉来迁就测试环境。CI 里用
# sudo -E 调用本脚本。

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
DPI="$ROOT/tests/dpi"

"$DPI/test_service_common.sh"
"$DPI/test_boot_delay.sh"
"$DPI/test_wan_resolver.sh"
"$DPI/test_service_commands.sh"
"$DPI/test_hotplug.sh"
"$DPI/test_upload.sh"
"$DPI/test_theme_runtime.sh"
"$DPI/test_firewall_cleanup.sh"
node "$DPI/test_luci_boot_delay.js"
python3 "$DPI/test_fakesip_source.py"

# 畸形 TCP data offset 的回归。上游 0.9.18 缺这个校验, 转发的 SYN 就能让
# root 守护进程越界读, 所以每次构建都要证明修复还在。
FAKEHTTP_SRC="$ROOT/openwrt-feed/liquid-formula/src-dpi/fakehttp"
DOFF_BIN="$(mktemp "${TMPDIR:-/tmp}/fakehttp-doff.XXXXXX")"
trap 'rm -f "$DOFF_BIN"' EXIT HUP INT TERM
# 只有"确实没有头文件"才允许跳过。把编译失败一律当成缺头文件的话, API 变更、
# 链接错误、测试自身写错都会被吞掉, 这条 C 回归就永远是假绿的。
if printf '#include <libnetfilter_queue/libnetfilter_queue.h>\nint main(void){return 0;}\n' \
	| cc -x c -o /dev/null - 2>/dev/null; then
	cc -std=gnu99 -I"$FAKEHTTP_SRC/include" -o "$DOFF_BIN" \
		"$DPI/fakehttp_doff_test.c" \
		"$FAKEHTTP_SRC/src/ipv4pkt.c" "$FAKEHTTP_SRC/src/ipv6pkt.c" \
		"$FAKEHTTP_SRC/src/logging.c" "$FAKEHTTP_SRC/src/globvar.c" \
		-lnetfilter_queue -lnfnetlink -lmnl
	"$DOFF_BIN"

	# 环形缓存回绕: srci_end 归零时反向扫描原本会从错误槽位开始。
	for fork in fakehttp fakesip; do
		prefix=fh
		[ "$fork" = fakesip ] && prefix=fs
		src="$ROOT/openwrt-feed/liquid-formula/src-dpi/$fork"
		wrap_bin="$(mktemp "${TMPDIR:-/tmp}/srcinfo-wrap.XXXXXX")"
		cc -std=gnu99 -I"$src/include" \
			-DSRCINFO_PUT=${prefix}_srcinfo_put -DSRCINFO_GET=${prefix}_srcinfo_get \
			-DSRCINFO_SETUP=${prefix}_srcinfo_setup -DSRCINFO_CLEANUP=${prefix}_srcinfo_cleanup \
			-o "$wrap_bin" "$DPI/srcinfo_wrap_test.c" \
			"$src/src/srcinfo.c" "$src/src/logging.c" "$src/src/globvar.c" \
			-lnetfilter_queue -lnfnetlink -lmnl
		"$wrap_bin"
		rm -f "$wrap_bin"
	done
else
	echo "fakehttp doff test: skipped (libnetfilter-queue development headers are not installed)" >&2
fi

echo "dpi tests: ok"
