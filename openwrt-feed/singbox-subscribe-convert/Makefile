include $(TOPDIR)/rules.mk

PKG_NAME:=singbox-subscribe-convert
PKG_VERSION:=0.7.2
PKG_RELEASE:=2
PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=myheadisradio

include $(INCLUDE_DIR)/package.mk

define Package/singbox-subscribe-convert
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Singbox Subscribe Convert service for OpenWrt
  DEPENDS:=+libc +curl +jsonfilter
endef

define Package/singbox-subscribe-convert/description
  Prebuilt singbox-subscribe-convert 0.7.2 service wrapper, templates and OpenWrt/Momo profile integration.
  The bundled sb-sub-c binary is Linux arm64/aarch64 and intended for aarch64_cortex-a53 targets such as Linksys E8450.
endef

define Package/singbox-subscribe-convert/conffiles
/etc/config/singbox_subscribe_convert
/etc/singbox-subscribe-convert/config.yaml
endef

define Build/Compile
endef

define Package/singbox-subscribe-convert/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) ./files/usr/bin/sb-sub-c $(1)/usr/bin/sb-sub-c
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/etc/init.d/singbox-subscribe-convert $(1)/etc/init.d/singbox-subscribe-convert
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/etc/config/singbox_subscribe_convert $(1)/etc/config/singbox_subscribe_convert
	$(INSTALL_DIR) $(1)/etc/singbox-subscribe-convert
	$(INSTALL_DATA) ./files/etc/singbox-subscribe-convert/config.yaml.example $(1)/etc/singbox-subscribe-convert/config.yaml.example
	$(INSTALL_DIR) $(1)/usr/share/singbox-subscribe-convert
	$(INSTALL_BIN) ./files/usr/share/singbox-subscribe-convert/generate-config.sh $(1)/usr/share/singbox-subscribe-convert/generate-config.sh
	$(INSTALL_BIN) ./files/usr/share/singbox-subscribe-convert/update.sh $(1)/usr/share/singbox-subscribe-convert/update.sh
	$(INSTALL_BIN) ./files/usr/share/singbox-subscribe-convert/validate-template.sh $(1)/usr/share/singbox-subscribe-convert/validate-template.sh
	$(INSTALL_DIR) $(1)/www/singbox-subscribe-convert/templates
	$(INSTALL_DATA) ./files/www/singbox-subscribe-convert/templates/openwrt-1.12-e8450.json $(1)/www/singbox-subscribe-convert/templates/openwrt-1.12-e8450.json
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/etc/uci-defaults/99-singbox-subscribe-convert $(1)/etc/uci-defaults/99-singbox-subscribe-convert
endef

$(eval $(call BuildPackage,singbox-subscribe-convert))
