#!/bin/sh
set -u

REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$REPO_ROOT/tests/shell/harness.sh"
RPC="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula"
UI="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/templates.js"

assert_contains "$RPC" 'template.lock' 'template writes use a separate atomic lock'
assert_contains "$RPC" 'mktemp "\$TPL_DIR/\.template.new' 'template staging shares the destination filesystem for atomic rename'
assert_contains "$RPC" '1048576' 'RPC enforces the 1 MiB template limit'
assert_contains "$RPC" 'valid_id' 'template IDs are strictly validated before path access'
assert_contains "$RPC" 'valid_file' 'template filenames are strictly validated before path access'
assert_contains "$RPC" 'template id and file are immutable' 'editing cannot change persistent identity'
assert_contains "$RPC" 'cannot disable current default_template' 'default template cannot be disabled'
assert_contains "$RPC" 'cannot delete current default_template' 'default template cannot be deleted'
assert_contains "$RPC" 'uci export' 'transaction snapshots UCI before persistence'
assert_contains "$RPC" 'uci import' 'transaction can restore its UCI snapshot'
assert_contains "$RPC" 'template_reply_error rollback' 'transaction reports rollback failures by phase'
assert_contains "$RPC" 'phase.*complete' 'successful transaction reports the complete phase'
assert_contains "$UI" 'TextEncoder.*1048576|1048576' 'browser enforces the UTF-8 1 MiB limit'
assert_contains "$UI" 'readOnly = true' 'editing locks template ID and filename'
assert_contains "$UI" "res.phase !== 'complete'" 'browser fails closed on incomplete transactions'

finish_tests
