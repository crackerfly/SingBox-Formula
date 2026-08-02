include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI app for Liquid Formula
PKG_VERSION:=1.8.7
PKG_RELEASE:=1
# irqbalance 是 Tuning Utility 页上的一个开关, 装了包就应该随时能开, 所以
# 直接依赖它而不是让用户自己发现要另装。
LUCI_DEPENDS:=+liquid-formula +luci-base +rpcd +cgi-io +coreutils-stat +coreutils-od +flock +irqbalance
LUCI_PKGARCH:=all


define Package/luci-app-liquid-formula/conffiles
/etc/config/customlogo
/etc/config/tuning
endef

define Package/luci-app-liquid-formula/prerm
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	# 先做唯一会中止卸载的事务。若恢复失败，必须在停止 logo 等其他服务之前
	# 退出，让仍安装着的包保持原运行状态。
	if [ -x /usr/share/liquid-formula/apply-tuning.sh ]; then
		/usr/share/liquid-formula/apply-tuning.sh restore >/dev/null 2>&1 || {
			echo "liquid-formula: refusing to uninstall because kernel tuning could not be restored" >&2
			exit 1
		}
	elif [ -e /etc/sysctl.d/99-liquid-formula.conf ]; then
		echo "liquid-formula: refusing to uninstall with an active tuning drop-in and no restore helper" >&2
		exit 1
	fi
	# stop_service 会调用 remove_all_markers, 把注入到主题 header/login 模板里的
	# 标记清掉。不在卸载前跑这一步, 标记会永久留在模板里, 而且之后没有任何
	# 组件认识它们。
	if [ -x /etc/init.d/liquid-formula-logo ]; then
		/etc/init.d/liquid-formula-logo stop >/dev/null 2>&1 || true
		/etc/init.d/liquid-formula-logo disable >/dev/null 2>&1 || true
	fi
	rm -f /tmp/luci-indexcache /tmp/luci-indexcache.* 2>/dev/null || true
	rm -rf /tmp/luci-modulecache /tmp/luci-templatecache 2>/dev/null || true
}
exit 0
endef

define Package/luci-app-liquid-formula/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	chmod 0755 /usr/libexec/rpcd/liquid_formula 2>/dev/null || exit 1
	# 这两个是本包自带的; 用 -f 判断而不是硬失败, 免得将来改名时
	# 整个包的安装被一条 chmod 拖挂。
	[ ! -f /etc/init.d/liquid-formula-logo ] || \
		chmod 0755 /etc/init.d/liquid-formula-logo 2>/dev/null || true
	[ ! -f /www/cgi-bin/liquid-formula-upload ] || \
		chmod 0755 /www/cgi-bin/liquid-formula-upload 2>/dev/null || true
	[ ! -f /usr/share/liquid-formula/apply-tuning.sh ] || \
		chmod 0755 /usr/share/liquid-formula/apply-tuning.sh 2>/dev/null || true
	rm -f /tmp/luci-indexcache /tmp/luci-indexcache.* 2>/dev/null || true
	rm -rf /tmp/luci-modulecache /tmp/luci-templatecache 2>/dev/null || true
	/etc/init.d/rpcd restart >/dev/null 2>&1 || exit 1
}
exit 0
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
