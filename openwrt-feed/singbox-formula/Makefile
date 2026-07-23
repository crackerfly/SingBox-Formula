include $(TOPDIR)/rules.mk

PKG_NAME:=singbox-formula
PKG_VERSION:=1.6.0
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0 GPL-3.0-or-later MIT
PKG_LICENSE_FILES:=LICENSE LICENSES/GPL-3.0-or-later.txt LICENSES/MIT-lumberjack.txt
PKG_MAINTAINER:=myheadisradio
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/haierkeys/singbox-subscribe-convert
GO_PKG_BUILD_PKG:=github.com/haierkeys/singbox-subscribe-convert
GO_PKG_INSTALL_EXTRA:=config/config.yaml

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

# The SDK package tree can itself live inside a Git worktree. Apply this to
# both `go list` and `go install`; the helper's install-only flag is too late
# to prevent VCS probing while targets are discovered.
GO_PKG_BUILD_VARS += GOFLAGS=-buildvcs=false

define Package/singbox-formula
  SECTION:=net
  CATEGORY:=Network
  TITLE:=SingBox Formula service for OpenWrt
  DEPENDS:=@TARGET_mediatek_mt7622 $(GO_ARCH_DEPENDS) +libc +curl +jsonfilter
endef

define Package/singbox-formula/description
  SingBox Formula: subscription-to-sing-box converter service wrapper, default templates
  and OpenWrt/Momo profile integration. Builds the pinned sb-sub-c converter source for
  the mediatek/mt7622 target.
endef

define Package/singbox-formula/conffiles
/etc/config/singbox_formula
/etc/singbox-formula/config.yaml
/www/singbox-formula/templates/momo-template.json
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) ./src/. $(PKG_BUILD_DIR)/
endef

define Build/Compile
	$(call GoPackage/Build/Compile)
	$(CP) $(GO_PKG_BUILD_BIN_DIR)/singbox-subscribe-convert $(PKG_BUILD_DIR)/sb-sub-c
endef

define Package/singbox-formula/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/sb-sub-c $(1)/usr/bin/sb-sub-c
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/etc/init.d/singbox-formula $(1)/etc/init.d/singbox-formula
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/etc/config/singbox_formula $(1)/etc/config/singbox_formula
	$(INSTALL_DIR) $(1)/etc/singbox-formula
	$(INSTALL_DATA) ./files/etc/singbox-formula/config.yaml.example $(1)/etc/singbox-formula/config.yaml.example
	$(INSTALL_DIR) $(1)/usr/share/singbox-formula
	$(INSTALL_BIN) ./files/usr/share/singbox-formula/generate-config.sh $(1)/usr/share/singbox-formula/generate-config.sh
	$(INSTALL_BIN) ./files/usr/share/singbox-formula/update.sh $(1)/usr/share/singbox-formula/update.sh
	$(INSTALL_BIN) ./files/usr/share/singbox-formula/validate-template.sh $(1)/usr/share/singbox-formula/validate-template.sh
	$(INSTALL_BIN) ./files/usr/share/singbox-formula/run-delayed.sh $(1)/usr/share/singbox-formula/run-delayed.sh
	$(INSTALL_DIR) $(1)/www/singbox-formula/templates
	$(INSTALL_DATA) ./files/www/singbox-formula/templates/momo-template.json $(1)/www/singbox-formula/templates/momo-template.json
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/etc/uci-defaults/99-singbox-formula $(1)/etc/uci-defaults/99-singbox-formula
endef


define Package/singbox-formula/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	mkdir -p /etc/singbox-formula /var/lib/singbox-formula/cache /var/log/singbox-formula /www/singbox-formula/templates /etc/momo/profiles
	[ -x /etc/uci-defaults/99-singbox-formula ] && /etc/uci-defaults/99-singbox-formula >/dev/null 2>&1 || true
	# Register the service so the boot() hook is considered; the UCI 'enabled'
	# flag remains the actual autostart switch (default off).
	[ -x /etc/init.d/singbox-formula ] && /etc/init.d/singbox-formula enable >/dev/null 2>&1 || true
}
exit 0
endef

define Package/singbox-formula/prerm
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] && exit 0
# Stop and disable the service on removal. Doing it here (quietly) avoids the
# spurious "ubus call service delete ... (Not found)" that procd prints when the
# default uninstall step stops a service that is not currently running.
if [ -x /etc/init.d/singbox-formula ]; then
	/etc/init.d/singbox-formula disable >/dev/null 2>&1 || true
	/etc/init.d/singbox-formula stop >/dev/null 2>&1 || true
fi
exit 0
endef

$(eval $(call BuildPackage,singbox-formula))
