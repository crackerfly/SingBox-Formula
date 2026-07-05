'use strict';
'require view';
'require rpc';
'require ui';

var callListTemplates = rpc.declare({ object: 'singbox_formula', method: 'list_templates', expect: { '': {} } });
var callReadTemplate = rpc.declare({ object: 'singbox_formula', method: 'read_template', params: [ 'id', 'file' ], expect: { '': {} } });
var callWriteTemplate = rpc.declare({ object: 'singbox_formula', method: 'write_template', params: [ 'id', 'name', 'file', 'no_node', 'enabled', 'content' ], expect: { '': {} } });
var callDeleteTemplate = rpc.declare({ object: 'singbox_formula', method: 'delete_template', params: [ 'id' ], expect: { '': {} } });

return view.extend({
	load: function() {
		return callListTemplates().catch(function() { return { templates: [] }; });
	},

	render: function(data) {
		var templates = (data && data.templates) ? data.templates : [];
		return this.renderTemplateManager(templates);
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
			E('p', {}, _('Upload a local JSON template, edit an existing template, save a new template or delete unused templates. The current default template (set in the Overview tab) cannot be deleted.')),
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
				E('button', { 'class': 'btn cbi-button cbi-button-apply', 'click': function(ev) { ev.preventDefault(); self.saveTemplate(ev.currentTarget); } }, _('Save template')),
				' ',
				E('button', { 'class': 'btn cbi-button', 'click': function(ev) { ev.preventDefault(); self.newTemplate(); } }, _('Clear editor')),
				E('span', { 'id': 'sbsc_tpl_save_status', 'style': 'margin-left:1em; vertical-align:middle' })
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

	setSaveStatus: function(msg, isError) {
		var el = document.getElementById('sbsc_tpl_save_status');
		if (!el)
			return;
		el.textContent = msg || '';
		el.style.color = isError ? '#b00' : 'green';
	},

	saveTemplate: function(btn) {
		var id = document.getElementById('sbsc_tpl_id').value;
		var name = document.getElementById('sbsc_tpl_name').value;
		var file = document.getElementById('sbsc_tpl_file').value;
		var noNode = document.getElementById('sbsc_tpl_no_node').value || '➜ Direct';
		var enabled = document.getElementById('sbsc_tpl_enabled').checked;
		var content = document.getElementById('sbsc_tpl_content').value;
		var self = this;
		if (btn) { btn.classList.add('spinning'); btn.disabled = true; }
		self.setSaveStatus('', false);
		var done = function() { if (btn) { btn.classList.remove('spinning'); btn.disabled = false; } };
		return callWriteTemplate(id, name, file, noNode, enabled, content).then(function(res) {
			if (res && res.error) {
				self.setSaveStatus(res.error, true);
				done();
				return;
			}
			self.setSaveStatus(_('Template saved.'), false);
			return self.reloadTemplateList().then(done, done);
		}).catch(function(err) {
			self.setSaveStatus((err && err.message) || String(err), true);
			done();
		});
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
