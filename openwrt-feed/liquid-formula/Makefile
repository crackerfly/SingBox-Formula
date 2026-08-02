include $(TOPDIR)/rules.mk

PKG_NAME:=liquid-formula
PKG_VERSION:=1.8.7
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0 GPL-3.0-or-later MIT
PKG_LICENSE_FILES:=LICENSE LICENSES/GPL-3.0-or-later.txt LICENSES/MIT-lumberjack.txt \
		   src-dpi/fakehttp/LICENSE src-dpi/fakesip/LICENSE
PKG_MAINTAINER:=myheadisradio
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/haierkeys/singbox-subscribe-convert
GO_PKG_BUILD_PKG:=github.com/haierkeys/singbox-subscribe-convert
GO_PKG_INSTALL_EXTRA:=config/config.yaml

# ---------------------------------------------------------- DPI helpers ----
# FakeHTTP and FakeSIP used to live in a separate liquid-formula-dpi package.
# They are built from the vendored sources under src-dpi/ and installed here,
# so a single package covers the converter and the obfuscation helpers.
#
# Both are project-maintained forks; see THIRD_PARTY_SOURCES.md for the exact
# delta over upstream. The FakeHTTP base stays at 0.9.18 because that release
# builds its payload ring by inserting each entry after the current node, so the
# init script's reverse ordering compensates for it; a newer base would reverse
# the order twice.
FAKEHTTP_VERSION:=0.9.21
FAKESIP_VERSION:=0.9.8

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

# The SDK package tree can itself live inside a Git worktree. Apply this to
# both `go list` and `go install`; the helper's install-only flag is too late
# to prevent VCS probing while targets are discovered.
GO_PKG_BUILD_VARS += GOFLAGS=-buildvcs=false

define Package/liquid-formula
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Liquid Formula service for OpenWrt
  DEPENDS:=$(GO_ARCH_DEPENDS) +libc +curl +jsonfilter \
	   +libnetfilter-queue +libnfnetlink +libmnl +nftables +kmod-nft-queue +flock
endef

define Package/liquid-formula/description
  Liquid Formula: subscription-to-sing-box converter service wrapper, default
  templates and OpenWrt/Momo profile integration, plus the FakeHTTP and FakeSIP
  DPI obfuscation helpers. Everything is cross-compiled from the vendored
  sources in this package.

  The obfuscation helpers are not proxies or tunnels: they do not hide the real
  destination and offer no guarantee against manual traffic analysis. Both ship
  disabled and have their own switches on the LuCI pages.
endef

define Package/liquid-formula/conffiles
/etc/config/liquid_formula
/etc/config/fakehttp
/etc/config/fakesip
/etc/liquid-formula/config.yaml
/www/liquid-formula/templates/momo-template.json
/www/liquid-formula/templates/localdns-template.json
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) ./src/. $(PKG_BUILD_DIR)/
	# Kept out of the Go module tree so `go install ./...` never walks into it.
	mkdir -p $(PKG_BUILD_DIR)/dpi
	$(CP) ./src-dpi/. $(PKG_BUILD_DIR)/dpi/
endef

# Both upstream Makefiles use `override CFLAGS+=` / `override LDFLAGS+=`, so the
# toolchain values passed here are kept and their own flags appended.
define Build/Compile/dpi
	$(MAKE) -C $(PKG_BUILD_DIR)/dpi/$(1) \
		CC="$(TARGET_CC)" \
		STRIP="$(TARGET_CROSS)strip" \
		CFLAGS="$(TARGET_CFLAGS) $(TARGET_CPPFLAGS)" \
		LDFLAGS="$(TARGET_LDFLAGS)" \
		VERSION="$(2)" \
		all
endef

define Build/Compile
	$(call GoPackage/Build/Compile)
	$(CP) $(GO_PKG_BUILD_BIN_DIR)/singbox-subscribe-convert $(PKG_BUILD_DIR)/sb-sub-c
	$(call Build/Compile/dpi,fakehttp,$(FAKEHTTP_VERSION))
	$(call Build/Compile/dpi,fakesip,$(FAKESIP_VERSION))
endef

define Package/liquid-formula/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/sb-sub-c $(1)/usr/bin/sb-sub-c
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/etc/init.d/liquid-formula $(1)/etc/init.d/liquid-formula
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/etc/config/liquid_formula $(1)/etc/config/liquid_formula
	$(INSTALL_DIR) $(1)/etc/liquid-formula
	$(INSTALL_DATA) ./files/etc/liquid-formula/config.yaml.example $(1)/etc/liquid-formula/config.yaml.example
	$(INSTALL_DIR) $(1)/usr/share/liquid-formula
	$(INSTALL_BIN) ./files/usr/share/liquid-formula/generate-config.sh $(1)/usr/share/liquid-formula/generate-config.sh
	$(INSTALL_BIN) ./files/usr/share/liquid-formula/update.sh $(1)/usr/share/liquid-formula/update.sh
	$(INSTALL_BIN) ./files/usr/share/liquid-formula/validate-template.sh $(1)/usr/share/liquid-formula/validate-template.sh
	$(INSTALL_BIN) ./files/usr/share/liquid-formula/run-delayed.sh $(1)/usr/share/liquid-formula/run-delayed.sh
	$(INSTALL_DIR) $(1)/www/liquid-formula/templates
	$(INSTALL_DATA) ./files/www/liquid-formula/templates/momo-template.json $(1)/www/liquid-formula/templates/momo-template.json
	$(INSTALL_DATA) ./files/www/liquid-formula/templates/localdns-template.json $(1)/www/liquid-formula/templates/localdns-template.json
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/etc/uci-defaults/99-liquid-formula $(1)/etc/uci-defaults/99-liquid-formula

	# --- DPI helpers ---
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/dpi/fakehttp/build/fakehttp $(1)/usr/bin/fakehttp
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/dpi/fakesip/build/fakesip $(1)/usr/bin/fakesip

	$(INSTALL_DIR) $(1)/usr/share/liquid-formula-dpi
	$(INSTALL_DATA) ./files/usr/share/liquid-formula-dpi/service-common.sh \
		$(1)/usr/share/liquid-formula-dpi/service-common.sh
	$(INSTALL_BIN) ./files/usr/share/liquid-formula-dpi/wan-resolver.sh $(1)/usr/share/liquid-formula-dpi/wan-resolver.sh
	$(INSTALL_BIN) ./files/usr/share/liquid-formula-dpi/boot-delay-runner.sh \
		$(1)/usr/share/liquid-formula-dpi/boot-delay-runner.sh

	$(INSTALL_CONF) ./files/etc/config/fakehttp $(1)/etc/config/fakehttp
	$(INSTALL_CONF) ./files/etc/config/fakesip $(1)/etc/config/fakesip

	$(INSTALL_BIN) ./files/etc/init.d/fakehttp $(1)/etc/init.d/fakehttp
	$(INSTALL_BIN) ./files/etc/init.d/fakesip $(1)/etc/init.d/fakesip
	$(INSTALL_BIN) ./files/etc/init.d/liquid-formula-boot-delay \
		$(1)/etc/init.d/liquid-formula-boot-delay

	$(INSTALL_BIN) ./files/etc/uci-defaults/99-liquid-formula-dpi \
		$(1)/etc/uci-defaults/99-liquid-formula-dpi

	$(INSTALL_DIR) $(1)/etc/hotplug.d/iface
	$(INSTALL_DATA) ./files/etc/hotplug.d/iface/99-liquid-formula-dpi \
		$(1)/etc/hotplug.d/iface/99-liquid-formula-dpi

	$(INSTALL_DIR) $(1)/lib/upgrade/keep.d
	$(INSTALL_DATA) ./files/lib/upgrade/keep.d/liquid-formula-dpi \
		$(1)/lib/upgrade/keep.d/liquid-formula-dpi
endef


define Package/liquid-formula/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	mkdir -p /etc/liquid-formula /var/lib/liquid-formula/cache /var/log/liquid-formula /www/liquid-formula/templates /etc/momo/profiles
	if [ -x /etc/uci-defaults/99-liquid-formula ]; then
		/etc/uci-defaults/99-liquid-formula >/dev/null 2>&1 || exit 1
	fi
	if [ -x /etc/uci-defaults/99-liquid-formula-dpi ]; then
		/etc/uci-defaults/99-liquid-formula-dpi >/dev/null 2>&1 || exit 1
	fi
	# Register the service so the boot() hook is considered; the UCI 'enabled'
	# flag remains the actual autostart switch (default off).
	[ -x /etc/init.d/liquid-formula ] && /etc/init.d/liquid-formula enable >/dev/null 2>&1 || true
}
exit 0
endef

define Package/liquid-formula/prerm
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] && exit 0
# Stop and disable the service on removal. Doing it here (quietly) avoids the
# spurious "ubus call service delete ... (Not found)" that procd prints when the
# default uninstall step stops a service that is not currently running.
if [ -x /etc/init.d/liquid-formula ]; then
	/etc/init.d/liquid-formula disable >/dev/null 2>&1 || true
	/etc/init.d/liquid-formula stop >/dev/null 2>&1 || true
fi
	for svc in liquid-formula-boot-delay fakehttp fakesip; do
		if [ -x /etc/init.d/$$svc ]; then
			/etc/init.d/$$svc stop >/dev/null 2>&1 || true
			/etc/init.d/$$svc disable >/dev/null 2>&1 || true
		fi
	done
	# Best-effort cleanup if an upstream process was killed rather than stopped.
	# Shared nftables state and unrelated tables are never touched.
	for family in ip ip6; do
		nft delete table "$$family" fakehttp >/dev/null 2>&1 || true
		nft delete table "$$family" fakesip  >/dev/null 2>&1 || true
	done
	# 两个工具在 nft 不可用时会回退到 iptables。只删 nft 表的话, mangle 里的
	# 链和 PREROUTING/POSTROUTING 跳转会在卸载后永久残留。
	for bin in iptables ip6tables; do
		command -v "$$bin" >/dev/null 2>&1 || continue
		for upper in FAKEHTTP FAKESIP; do
			"$$bin" -w -t mangle -n -L "$${upper}_S" >/dev/null 2>&1 || continue
			"$$bin" -w -t mangle -F "$${upper}_R" >/dev/null 2>&1 || true
			"$$bin" -w -t mangle -F "$${upper}_S" >/dev/null 2>&1 || true
			"$$bin" -w -t mangle -F "$${upper}_D" >/dev/null 2>&1 || true
			while "$$bin" -w -t mangle -D PREROUTING -j "$${upper}_S" >/dev/null 2>&1; do :; done
			while "$$bin" -w -t mangle -D POSTROUTING -j "$${upper}_D" >/dev/null 2>&1; do :; done
			"$$bin" -w -t mangle -X "$${upper}_R" >/dev/null 2>&1 || true
			"$$bin" -w -t mangle -X "$${upper}_S" >/dev/null 2>&1 || true
			"$$bin" -w -t mangle -X "$${upper}_D" >/dev/null 2>&1 || true
		done
	done
	rm -rf /var/run/liquid-formula-boot-delay 2>/dev/null || true
exit 0
endef

$(eval $(call BuildPackage,liquid-formula))
