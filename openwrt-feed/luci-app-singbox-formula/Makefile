include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI app for SingBox Formula
PKG_VERSION:=1.3.1
PKG_RELEASE:=1
LUCI_DEPENDS:=+singbox-formula +luci-base +rpcd +rpcd-mod-file
LUCI_PKGARCH:=all


define Package/luci-app-singbox-formula/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	chmod 0755 /usr/libexec/rpcd/singbox_formula /usr/libexec/rpcd/singbox-formula 2>/dev/null || true
	rm -f /tmp/luci-indexcache 2>/dev/null || true
	rm -rf /tmp/luci-modulecache /tmp/luci-templatecache 2>/dev/null || true
	/etc/init.d/rpcd restart >/dev/null 2>&1 || true
	/etc/init.d/uhttpd restart >/dev/null 2>&1 || true
}
exit 0
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
