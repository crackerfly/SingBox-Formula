'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';

var callStatus = rpc.declare({ object: 'singbox_formula', method: 'status', expect: { '': {} } });
var callAction = rpc.declare({ object: 'singbox_formula', method: 'action', params: [ 'name' ], expect: { '': {} } });
var callListTemplates = rpc.declare({ object: 'singbox_formula', method: 'list_templates', expect: { '': {} } });
var callReadTemplate = rpc.declare({ object: 'singbox_formula', method: 'read_template', params: [ 'id', 'file' ], expect: { '': {} } });
var callWriteTemplate = rpc.declare({ object: 'singbox_formula', method: 'write_template', params: [ 'id', 'name', 'file', 'no_node', 'enabled', 'content' ], expect: { '': {} } });
var callDeleteTemplate = rpc.declare({ object: 'singbox_formula', method: 'delete_template', params: [ 'id' ], expect: { '': {} } });

function showResult(res) {
	var msg = (res && res.output) ? res.output : JSON.stringify(res || {});
	ui.addNotification(null, E('pre', { 'style': 'white-space:pre-wrap' }, msg || _('Done')));
}

function actionButton(label, name) {
	return E('button', {
		'class': 'btn cbi-button cbi-button-apply',
		'click': function(ev) {
			ev.preventDefault();
			return callAction(name).then(showResult).catch(ui.addNotification.bind(ui, null));
		}
	}, label);
}

function copyText(text) {
	if (!text)
		return ui.addNotification(null, E('p', {}, _('Nothing to copy.')));

	if (navigator.clipboard && navigator.clipboard.writeText) {
		return navigator.clipboard.writeText(text).then(function() {
			ui.addNotification(null, E('p', {}, _('Copied to clipboard.')));
		}).catch(function() {
			return fallbackCopy(text);
		});
	}

	return fallbackCopy(text);
}

function fallbackCopy(text) {
	var ta = E('textarea', { 'style': 'position:fixed; left:-9999px; top:-9999px' }, text);
	document.body.appendChild(ta);
	ta.focus();
	ta.select();
	try {
		document.execCommand('copy');
		ui.addNotification(null, E('p', {}, _('Copied to clipboard.')));
	} catch (e) {
		ui.addNotification(null, E('p', {}, _('Copy failed. Please copy the URL manually.')));
	}
	document.body.removeChild(ta);
}

return view.extend({
	load: function() {
		function safe(promise, fallback) {
			return promise.catch(function(err) {
				fallback = fallback || {};
				fallback._error = err && (err.message || err.toString()) || String(err);
				return fallback;
			});
		}

		return Promise.all([
			uci.load('singbox_formula'),
			safe(callStatus(), {}),
			safe(callListTemplates(), { templates: [] })
		]);
	},

	render: function(data) {
		var status = data[1] || {};
		var templates = (data[2] && data[2].templates) ? data[2].templates : [];
		var m, s, o;

		m = new form.Map('singbox_formula', _('SingBox Formula'),
			_('Convert a source subscription into a sing-box JSON profile and update the configured output file. This app does not manage the sing-box runtime. Use OpenWrt-momo to run sing-box, firewall rules, access control and profile scheduling.'));

		s = m.section(form.NamedSection, 'main', 'global', _('Basic Settings'));
		s.anonymous = true;

		o = s.option(form.Flag, 'enabled', _('Enable converter service'),
			_('Master autostart switch. When enabled, the converter starts on boot (after the boot delay) and after Save & Apply. Use the Start/Stop buttons below for immediate manual control.'));
		o.default = '0';

		o = s.option(form.Value, 'boot_delay', _('Boot delay'), _('Seconds to wait before starting the converter service during boot. Manual start is not delayed.'));
		o.datatype = 'uinteger';
		o.default = '300';

		o = s.option(form.Value, 'subscription_url', _('Source subscription URL'));
		o.rmempty = false;
		o.placeholder = 'https://example.com/your/singbox/node-subscription.json';

		o = s.option(form.Value, 'password', _('Converter access password'));
		o.password = true;
		o.rmempty = false;

		o = s.option(form.Value, 'port', _('Converter service port'));
		o.datatype = 'port';
		o.default = '9716';
		o.rmempty = false;

		o = s.option(form.Value, 'refresh_interval', _('Subscription refresh interval'), _('Minutes. This maps to subscription.refresh_interval in config.yaml.'));
		o.datatype = 'uinteger';
		o.default = '2';

		o = s.option(form.ListValue, 'default_template', _('Default template'));
		o.rmempty = false;
		var seenTpl = {};
		for (var i = 0; i < templates.length; i++) {
			o.value(templates[i].id, '%s (%s)'.format(templates[i].name || templates[i].id, templates[i].id));
			seenTpl[templates[i].id] = true;
		}
		// Keep the currently saved value selectable even if its template is missing
		// (e.g. list failed to load, or it points at a deleted template).
		var curTpl = uci.get('singbox_formula', 'main', 'default_template');
		if (curTpl && !seenTpl[curTpl]) {
			o.value(curTpl, curTpl);
			seenTpl[curTpl] = true;
		}
		if (!Object.keys(seenTpl).length)
			o.value('openwrt', 'openwrt');

		o = s.option(form.Value, 'output_config', _('Output config path'), _('The generated file is written here after validation. OpenWrt-momo can use this profile path.'));
		o.default = '/etc/momo/profiles/config.json';

		o = s.option(form.Value, 'template_base_url', _('Template base URL'), _('Local HTTP URL prefix used by the converter to fetch JSON templates.'));
		o.default = 'http://127.0.0.1/singbox-formula/templates';

		return m.render().then(L.bind(function(formEl) {
			return E('div', {}, [
				formEl,
				this.renderMomoIntegration(status),
				this.renderStatus(status),
				this.renderTemplateManager(templates)
			]);
		}, this));
	},

	renderMomoIntegration: function(status) {
		var url = status.converted_url || '';
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('OpenWrt-momo Integration')),
			E('p', {}, _('This converter is intended to work together with OpenWrt-momo. Paste the local converted URL below into the Momo profile or subscription field when you want Momo to fetch the generated sing-box JSON from this router.')),
			E('p', {}, [
				E('a', { 'class': 'btn cbi-button', 'href': L.url('admin/services/momo/profile') }, _('Open Momo Profile')),
				' ',
				E('a', { 'class': 'btn cbi-button', 'href': L.url('admin/services/momo') }, _('Open Momo'))
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Local converted URL')),
				E('div', { 'class': 'cbi-value-field' }, [
					E('input', { 'id': 'sbsc_converted_url', 'class': 'cbi-input-text', 'style': 'width:70%', 'readonly': 'readonly', 'value': url }),
					' ',
					E('button', { 'class': 'btn cbi-button cbi-button-apply', 'click': function(ev) { ev.preventDefault(); copyText(url); } }, _('Copy URL'))
				])
			]),
			E('p', { 'class': 'cbi-value-description' }, _('The URL uses 127.0.0.1 and is meant for services running on this OpenWrt device. It is generated from saved settings. Save and apply first if you changed the port, password or default template.'))
		]);
	},

	renderStatus: function(status) {
		var health = status.health ? ((typeof status.health == 'string') ? status.health : JSON.stringify(status.health, null, 2)) : _('Not connected or service is not running.');
		var rpcError = status._error ? E('div', { 'class': 'alert-message warning' }, [
			E('strong', {}, _('RPC backend is not available.')), ' ',
			_('Restart rpcd and uhttpd, then refresh this page. Error: '), String(status._error)
		]) : '';
		return E('div', { 'class': 'cbi-section' }, [
			rpcError,
			E('h3', {}, _('Converter Service')),
			E('p', {}, [
				_('Status: '), status.running ? E('strong', { 'style': 'color:green' }, _('Running')) : E('strong', { 'style': 'color:red' }, _('Stopped')),
				' / ', _('Port: '), String(status.port || ''),
				' / ', _('Default template: '), String(status.default_template || ''),
				' / ', _('Output: '), String(status.output_config || '')
			]),
			E('div', { 'class': 'cbi-page-actions' }, [
				actionButton(_('Start converter'), 'start'), ' ',
				actionButton(_('Stop converter'), 'stop'), ' ',
				actionButton(_('Restart converter'), 'restart'), ' ',
				actionButton(_('Enable autostart'), 'enable'), ' ',
				actionButton(_('Disable autostart'), 'disable'), ' ',
				actionButton(_('Generate config.yaml'), 'generate'), ' ',
				actionButton(_('Refresh subscription'), 'refresh'), ' ',
				actionButton(_('Check generated config'), 'check'), ' ',
				actionButton(_('Update output file'), 'update')
			]),
			E('p', { 'class': 'cbi-value-description' }, _('Update output file only writes the generated JSON file. It does not restart sing-box. Manage sing-box from OpenWrt-momo.')),
			E('h4', {}, _('Health Check')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 180px; overflow:auto' }, health),
			E('h4', {}, _('Recent Update Log')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 220px; overflow:auto' }, status.update_log || '')
		]);
	},

	buildTemplateRow: function(t) {
		var self = this;
		return E('tr', {}, [
			E('td', {}, t.id),
			E('td', {}, t.enabled ? _('Yes') : _('No')),
			E('td', {}, t.name || ''),
			E('td', {}, t.file || ''),
			E('td', {}, String(t.size || 0)),
			E('td', {}, t.mtime || ''),
			E('td', {}, [
				E('button', { 'class': 'btn cbi-button', 'click': function(ev) { ev.preventDefault(); self.loadTemplate(t); } }, _('Edit')),
				' ',
				E('button', { 'class': 'btn cbi-button-negative', 'click': function(ev) { ev.preventDefault(); self.deleteTemplate(t.id); } }, _('Delete'))
			])
		]);
	},

	// Re-fetch the template list and rebuild the table in place, so Save/Delete
	// reflect immediately without a manual page reload.
	reloadTemplateList: function() {
		var self = this;
		return callListTemplates().then(function(res) {
			var templates = (res && res.templates) ? res.templates : [];
			var tbody = document.getElementById('sbsc_tpl_tbody');
			if (!tbody)
				return;
			while (tbody.firstChild)
				tbody.removeChild(tbody.firstChild);
			for (var i = 0; i < templates.length; i++)
				tbody.appendChild(self.buildTemplateRow(templates[i]));
		}).catch(function() { /* leave the existing table as-is on error */ });
	},

	renderTemplateManager: function(templates) {
		var tbody = E('tbody', { 'id': 'sbsc_tpl_tbody' });
		var self = this;
		for (var i = 0; i < templates.length; i++)
			tbody.appendChild(this.buildTemplateRow(templates[i]));

		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Template Management')),
			E('p', {}, _('Upload a local JSON template, edit an existing template, save a new template or delete unused templates. The current default_template cannot be deleted.')),
			E('table', { 'class': 'table cbi-section-table' }, [
				E('thead', {}, E('tr', {}, [
					E('th', {}, 'ID'),
					E('th', {}, _('Enabled')),
					E('th', {}, _('Name')),
					E('th', {}, _('File')),
					E('th', {}, _('Size')),
					E('th', {}, _('Modified')),
					E('th', {}, _('Actions'))
				])),
				tbody
			]),
			E('hr'),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, _('Template ID')), E('div', { 'class': 'cbi-value-field' }, E('input', { 'id': 'sbsc_tpl_id', 'class': 'cbi-input-text', 'placeholder': 'openwrt_custom' })) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, _('Template name')), E('div', { 'class': 'cbi-value-field' }, E('input', { 'id': 'sbsc_tpl_name', 'class': 'cbi-input-text', 'placeholder': 'OpenWrt Custom' })) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, _('File name')), E('div', { 'class': 'cbi-value-field' }, E('input', { 'id': 'sbsc_tpl_file', 'class': 'cbi-input-text', 'placeholder': 'openwrt-custom.json' })) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, _('Fallback outbound')), E('div', { 'class': 'cbi-value-field' }, E('input', { 'id': 'sbsc_tpl_no_node', 'class': 'cbi-input-text', 'value': '➜ Direct' })) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, _('Enable template')), E('div', { 'class': 'cbi-value-field' }, E('input', { 'id': 'sbsc_tpl_enabled', 'type': 'checkbox', 'checked': 'checked' })) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, _('Upload JSON template')), E('div', { 'class': 'cbi-value-field' }, E('input', { 'id': 'sbsc_tpl_upload', 'type': 'file', 'accept': '.json,application/json', 'change': function(ev) { self.uploadTemplate(ev); } })) ]),
			E('textarea', { 'id': 'sbsc_tpl_content', 'style': 'width:100%; min-height:420px; font-family:monospace', 'placeholder': '{\n  "outbounds": [\n    {{ Nodes }}\n  ]\n}' }),
			E('div', { 'class': 'cbi-page-actions' }, [
				E('button', { 'class': 'btn cbi-button cbi-button-apply', 'click': function(ev) { ev.preventDefault(); self.saveTemplate(); } }, _('Save template')),
				' ',
				E('button', { 'class': 'btn cbi-button', 'click': function(ev) { ev.preventDefault(); self.newTemplate(); } }, _('Clear editor'))
			])
		]);
	},

	loadTemplate: function(t) {
		return callReadTemplate(t.id, t.file).then(function(res) {
			if (res.error)
				return ui.addNotification(null, E('p', {}, res.error));
			document.getElementById('sbsc_tpl_id').value = t.id || '';
			document.getElementById('sbsc_tpl_name').value = t.name || '';
			document.getElementById('sbsc_tpl_file').value = t.file || res.file || '';
			document.getElementById('sbsc_tpl_no_node').value = t.no_node || '➜ Direct';
			document.getElementById('sbsc_tpl_enabled').checked = !!t.enabled;
			document.getElementById('sbsc_tpl_content').value = res.content || '';
		}).catch(ui.addNotification.bind(ui, null));
	},

	uploadTemplate: function(ev) {
		var file = ev.target.files[0];
		if (!file)
			return;
		var reader = new FileReader();
		reader.onload = function(e) {
			document.getElementById('sbsc_tpl_content').value = e.target.result;
			if (!document.getElementById('sbsc_tpl_file').value)
				document.getElementById('sbsc_tpl_file').value = file.name;
			if (!document.getElementById('sbsc_tpl_id').value)
				document.getElementById('sbsc_tpl_id').value = file.name.replace(/\.json$/i, '').replace(/[^A-Za-z0-9_]/g, '_');
			if (!document.getElementById('sbsc_tpl_name').value)
				document.getElementById('sbsc_tpl_name').value = file.name.replace(/\.json$/i, '');
		};
		reader.readAsText(file);
	},

	saveTemplate: function() {
		var id = document.getElementById('sbsc_tpl_id').value;
		var name = document.getElementById('sbsc_tpl_name').value;
		var file = document.getElementById('sbsc_tpl_file').value;
		var noNode = document.getElementById('sbsc_tpl_no_node').value || '➜ Direct';
		var enabled = document.getElementById('sbsc_tpl_enabled').checked;
		var content = document.getElementById('sbsc_tpl_content').value;
		var self = this;
		return callWriteTemplate(id, name, file, noNode, enabled, content).then(function(res) {
			if (res.error)
				return ui.addNotification(null, E('p', {}, res.error));
			ui.addNotification(null, E('p', {}, _('Template saved.')));
			return self.reloadTemplateList();
		}).catch(ui.addNotification.bind(ui, null));
	},

	deleteTemplate: function(id) {
		if (!confirm(_('Delete template ') + id + '?'))
			return;
		var self = this;
		return callDeleteTemplate(id).then(function(res) {
			if (res.error)
				return ui.addNotification(null, E('p', {}, res.error));
			ui.addNotification(null, E('p', {}, _('Template deleted.')));
			return self.reloadTemplateList();
		}).catch(ui.addNotification.bind(ui, null));
	},

	newTemplate: function() {
		document.getElementById('sbsc_tpl_id').value = '';
		document.getElementById('sbsc_tpl_name').value = '';
		document.getElementById('sbsc_tpl_file').value = '';
		document.getElementById('sbsc_tpl_no_node').value = '➜ Direct';
		document.getElementById('sbsc_tpl_enabled').checked = true;
		document.getElementById('sbsc_tpl_content').value = '';
	}
});
