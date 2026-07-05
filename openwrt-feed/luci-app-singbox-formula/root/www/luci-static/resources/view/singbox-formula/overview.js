'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';

var callStatus = rpc.declare({ object: 'singbox_formula', method: 'status', expect: { '': {} } });
var callAction = rpc.declare({ object: 'singbox_formula', method: 'action', params: [ 'name' ], expect: { '': {} } });
var callListTemplates = rpc.declare({ object: 'singbox_formula', method: 'list_templates', expect: { '': {} } });

function showResult(res, successMsg) {
	var out = (res && typeof res.output === 'string') ? res.output.replace(/\s+$/, '') : '';
	var code = (res && typeof res.code === 'number') ? res.code : 0;
	if (code !== 0) {
		return ui.addNotification(null, E('pre', { 'style': 'white-space:pre-wrap' },
			out || (_('Command failed with code ') + code)), 'danger');
	}
	if (out)
		return ui.addNotification(null, E('pre', { 'style': 'white-space:pre-wrap' }, out));
	return ui.addNotification(null, E('p', {}, successMsg || _('Done.')));
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
			_('Convert a source subscription into a sing-box JSON profile and update the configured output file. This app does not manage the sing-box runtime — use a runtime such as OpenWrt-momo to run sing-box, firewall rules, access control and profile scheduling.'));

		s = m.section(form.NamedSection, 'main', 'global', _('Basic Settings'));
		s.anonymous = true;

		o = s.option(form.Flag, 'enabled', _('Enable converter service'),
			_('When enabled, the converter starts immediately on Save & Apply and also autostarts on boot (after the boot delay below). Uncheck and Save & Apply to stop it. For quick manual control without changing this setting, use the Start / Stop buttons in the Converter Service section.'));
		o.default = '0';

		o = s.option(form.Value, 'boot_delay', _('Boot delay'),
			_('Seconds to wait before autostarting on boot. This delay applies ONLY to autostart on boot; starting via Save & Apply or the buttons is immediate.'));
		o.datatype = 'uinteger';
		o.default = '90';

		o = s.option(form.Value, 'subscription_url', _('Source subscription URL'));
		o.rmempty = false;
		o.placeholder = 'https://example.com/your/subscription';

		o = s.option(form.Flag, 'singbox_flag', _('Request sing-box format (flag=singbox)'),
			_('Automatically append flag=singbox to the subscription URL when generating the converter config. Enable this if your provider returns a base64 / URI node list instead of sing-box JSON. Skipped automatically if the URL already contains a flag= parameter.'));
		o.default = '1';

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

		o = s.option(form.ListValue, 'default_template', _('Default template'),
			_('Which template is used when a request does not specify one. It must be a template that is enabled in the Templates tab.'));
		o.rmempty = false;
		var seenTpl = {};
		for (var i = 0; i < templates.length; i++) {
			o.value(templates[i].id, '%s (%s)'.format(templates[i].name || templates[i].id, templates[i].id));
			seenTpl[templates[i].id] = true;
		}
		var curTpl = uci.get('singbox_formula', 'main', 'default_template');
		if (curTpl && !seenTpl[curTpl]) {
			o.value(curTpl, curTpl);
			seenTpl[curTpl] = true;
		}
		if (!Object.keys(seenTpl).length)
			o.value('momo_template', 'Momo Template (momo_template)');

		o = s.option(form.Value, 'output_config', _('Output config path'), _('The generated file is written here after validation. A sing-box runtime such as OpenWrt-momo can use this profile path.'));
		o.default = '/etc/momo/profiles/config.json';

		o = s.option(form.Value, 'template_base_url', _('Template base URL'), _('Local HTTP URL prefix used by the converter to fetch JSON templates.'));
		o.default = 'http://127.0.0.1/singbox-formula/templates';

		return m.render().then(L.bind(function(formEl) {
			return E('div', {}, [
				formEl,
				this.renderIntegration(status),
				this.renderStatus(status)
			]);
		}, this));
	},

	renderIntegration: function(status) {
		var url = status.converted_url || '';
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Sing-Box Integration')),
			E('p', {}, _('This converter produces a sing-box JSON profile at the output path and also serves it over the local URL below. Point your sing-box runtime (for example OpenWrt-momo) at this URL, or let it read the output file, so it fetches the generated profile from this router.')),
			E('p', {}, [
				E('a', {
					'class': 'btn cbi-button',
					'href': 'https://github.com/nikkinikki-org/OpenWrt-momo',
					'target': '_blank',
					'rel': 'noreferrer'
				}, _('OpenWrt-momo on GitHub'))
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Local converted URL')),
				E('div', { 'class': 'cbi-value-field' }, [
					E('input', { 'id': 'sbsc_converted_url', 'class': 'cbi-input-text', 'style': 'width:70%', 'readonly': 'readonly', 'value': url }),
					' ',
					E('button', { 'class': 'btn cbi-button cbi-button-apply', 'click': function(ev) { ev.preventDefault(); copyText(url); } }, _('Copy URL'))
				])
			]),
			E('p', { 'class': 'cbi-value-description' }, _('The URL uses 127.0.0.1 and is meant for services running on this OpenWrt device. It is generated from saved settings. Save & Apply first if you changed the port, password or default template.'))
		]);
	},

	// Run an action with a spinner on the clicked button, show a readable result,
	// then refresh the status card in place.
	doAction: function(name, successMsg, btn) {
		var self = this;
		if (btn) { btn.classList.add('spinning'); btn.disabled = true; }
		var done = function() { if (btn) { btn.classList.remove('spinning'); btn.disabled = false; } };
		return callAction(name).then(function(res) {
			showResult(res, successMsg);
			return self.reloadStatus().then(done, done);
		}).catch(function(err) {
			done();
			ui.addNotification(null, E('p', {}, (err && err.message) || String(err)));
		});
	},

	// Re-fetch status and rebuild the contents of the status card without a reload.
	reloadStatus: function() {
		var self = this;
		return callStatus().then(function(status) {
			var el = document.getElementById('sbf_status_section');
			if (!el)
				return;
			while (el.firstChild)
				el.removeChild(el.firstChild);
			var kids = self.statusChildren(status || {});
			for (var i = 0; i < kids.length; i++) {
				var k = kids[i];
				if (k === '' || k == null)
					continue;
				if (typeof k === 'string')
					k = document.createTextNode(k);
				el.appendChild(k);
			}
		}).catch(function() { /* keep the current card on error */ });
	},

	renderStatus: function(status) {
		return E('div', { 'id': 'sbf_status_section', 'class': 'cbi-section' }, this.statusChildren(status));
	},

	statusChildren: function(status) {
		var self = this;
		var running = !!status.running;
		var enabled = !!status.enabled;
		var health = status.health ? ((typeof status.health == 'string') ? status.health : JSON.stringify(status.health, null, 2)) : _('Not connected or service is not running.');
		var rpcError = status._error ? E('div', { 'class': 'alert-message warning' }, [
			E('strong', {}, _('RPC backend is not available.')), ' ',
			_('Restart rpcd and uhttpd, then refresh this page. Error: '), String(status._error)
		]) : '';

		// Single toggle: run state (Start when stopped, Stop when running).
		var runToggle = E('button', {
			'class': 'btn cbi-button ' + (running ? 'cbi-button-negative' : 'cbi-button-positive'),
			'click': function(ev) {
				ev.preventDefault();
				return running ? self.doAction('stop', _('Converter stopped.'), ev.currentTarget)
				               : self.doAction('start', _('Converter started.'), ev.currentTarget);
			}
		}, running ? _('Stop converter') : _('Start converter'));

		// Single toggle: autostart flag (also starts/stops now to match).
		var autostartToggle = E('button', {
			'class': 'btn cbi-button',
			'click': function(ev) {
				ev.preventDefault();
				return enabled ? self.doAction('disable', _('Autostart disabled; converter stopped.'), ev.currentTarget)
				               : self.doAction('enable', _('Autostart enabled; converter started.'), ev.currentTarget);
			}
		}, enabled ? _('Disable autostart') : _('Enable autostart'));

		var mkBtn = function(label, name, msg) {
			return E('button', { 'class': 'btn cbi-button', 'click': function(ev) { ev.preventDefault(); return self.doAction(name, msg, ev.currentTarget); } }, label);
		};

		return [
			rpcError,
			E('h3', {}, _('Converter Service')),
			E('p', {}, [
				_('Status: '), running ? E('strong', { 'style': 'color:green' }, _('Running')) : E('strong', { 'style': 'color:#b00' }, _('Stopped')),
				' / ', _('Autostart: '), enabled ? E('strong', { 'style': 'color:green' }, _('On')) : E('strong', {}, _('Off')),
				' / ', _('Port: '), String(status.port || ''),
				' / ', _('Default template: '), String(status.default_template || ''),
				' / ', _('Output: '), String(status.output_config || '')
			]),
			E('div', { 'class': 'cbi-page-actions', 'style': 'text-align:left; margin-top:.5em' }, [
				runToggle, ' ',
				mkBtn(_('Restart converter'), 'restart', _('Converter restarted.')), ' ',
				autostartToggle,
				E('span', { 'style': 'display:inline-block; width:1.5em' }, ' '),
				mkBtn(_('Generate config.yaml'), 'generate', _('config.yaml generated.')), ' ',
				mkBtn(_('Refresh subscription'), 'refresh', _('Subscription refreshed.')), ' ',
				mkBtn(_('Check generated config'), 'check', _('Generated config is valid.')), ' ',
				mkBtn(_('Update output file'), 'update', _('Output file updated.'))
			]),
			E('p', { 'class': 'cbi-value-description' }, [
				_('Enable / Disable autostart is the master switch: it starts or stops the converter now and sets whether it autostarts on boot. Start / Stop / Restart control the process right now without changing the autostart setting. '),
				_('Refresh, Check and Update need the converter running — they will start it automatically if needed. Update output file only writes the generated JSON; it does not restart sing-box (manage that from your sing-box runtime).')
			]),
			E('h4', {}, _('Health Check')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 180px; overflow:auto' }, health),
			E('h4', {}, _('Recent Update Log')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 220px; overflow:auto' }, status.update_log || '')
		];
	}
});
