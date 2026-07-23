'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require poll';

var callStatus = rpc.declare({ object: 'singbox_formula', method: 'status', expect: { '': {} } });
var callServiceAction = rpc.declare({ object: 'singbox_formula', method: 'service_action', params: [ 'name' ], expect: { '': {} } });
var callGenerate = rpc.declare({ object: 'singbox_formula', method: 'generate', expect: { '': {} } });
var callRefresh = rpc.declare({ object: 'singbox_formula', method: 'refresh', expect: { '': {} } });
var callCheck = rpc.declare({ object: 'singbox_formula', method: 'check', expect: { '': {} } });
var callUpdate = rpc.declare({ object: 'singbox_formula', method: 'update', expect: { '': {} } });
var callListTemplates = rpc.declare({ object: 'singbox_formula', method: 'list_templates', expect: { '': {} } });

// Actions that run detached in the backend (see _spawn_update in the rpcd
// script) because they may outlive the ~20s ubus timeout. The action call
// returns "queued" instantly; completion is read back from status polling.
var ASYNC = { refresh: true, check: true, update: true };
var ACTION_CALLS = { generate: callGenerate, refresh: callRefresh, check: callCheck, update: callUpdate };

function strictResult(res) {
	if (!res || typeof res.code !== 'number' || typeof res.output !== 'string')
		throw new Error(_('Invalid response from RPC backend.'));
	return res;
}

// Transient floating toast, replacing ui.addNotification: non-blocking,
// always visible regardless of scroll position, auto-dismisses (click to
// dismiss immediately). Errors stay longer and are tinted red.
function toast(msg, isError) {
	var wrap = document.getElementById('sbf_toast_wrap');
	if (!wrap) {
		wrap = E('div', { 'id': 'sbf_toast_wrap', 'style': 'position:fixed; top:3.5em; left:50%; transform:translateX(-50%); z-index:20000; display:flex; flex-direction:column; align-items:center; pointer-events:none; max-width:90vw' });
		document.body.appendChild(wrap);
	}
	var box = E('div', {
		'style': 'pointer-events:auto; margin-top:.5em; padding:.6em 1.4em; border-radius:4px; ' +
			'box-shadow:0 2px 14px rgba(0,0,0,.45); color:#fff; font-size:95%; cursor:pointer; ' +
			'white-space:pre-wrap; word-break:break-word; max-width:80vw; ' +
			'background:' + (isError ? 'rgba(150,30,30,.96)' : 'rgba(38,38,38,.94)'),
		'click': function() { if (box.parentNode) box.parentNode.removeChild(box); }
	}, msg);
	wrap.appendChild(box);
	window.setTimeout(function() {
		box.style.transition = 'opacity .4s';
		box.style.opacity = '0';
		window.setTimeout(function() { if (box.parentNode) box.parentNode.removeChild(box); }, 450);
	}, isError ? 6000 : 2800);
}

function copyText(text) {
	if (!text)
		return toast(_('Nothing to copy.'), true);
	if (navigator.clipboard && navigator.clipboard.writeText) {
		return navigator.clipboard.writeText(text).then(function() {
			toast(_('Copied to clipboard.'));
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
		toast(_('Copied to clipboard.'));
	} catch (e) {
		toast(_('Copy failed. Please copy the URL manually.'), true);
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

		this._lastStatus = status;

		m = new form.Map('singbox_formula', _('SingBox Formula'),
			_('Convert a source subscription into a sing-box JSON profile and update the configured output file. This app does not manage the sing-box runtime — use a runtime such as OpenWrt-momo to run sing-box, firewall rules, access control and profile scheduling.'));

		s = m.section(form.NamedSection, 'main', 'global', _('Basic Settings'));
		s.anonymous = true;

		o = s.option(form.Flag, 'enabled', _('Enable converter service'),
			_('Master switch. On Save & Apply this page brings the service in line with the switch: it starts the converter when enabled, stops it when disabled, and restarts it when settings changed so they take effect. When enabled, it also autostarts on boot (after the boot delay below).'));
		o.default = '0';

		o = s.option(form.Value, 'boot_delay', _('Boot delay'),
			_('Seconds to wait before autostarting on boot. This delay applies ONLY to autostart on boot; starting via Save & Apply or the buttons is immediate.'));
		o.datatype = 'uinteger';
		o.default = '90';

		o = s.option(form.Value, 'subscription_url', _('Source subscription URL'),
			_('Paste the link exactly as your provider gives it. Any extra query parameter your panel needs (for example flag=singbox, which only a few providers honour) can simply be part of this URL.'));
		o.rmempty = false;
		o.placeholder = 'https://example.com/your/subscription';

		o = s.option(form.Value, 'user_agent', _('Subscription User-Agent'),
			_('Most provider panels decide what to send - sing-box JSON, Clash YAML or a base64 node list - from this header, and reject unknown or outdated clients with "client version too low". Pick a preset or type your own. The converter now decodes base64 / URI lists automatically, so a v2rayN style UA is a safe fallback when your provider has no sing-box support.'));
		o.default = 'sing-box 1.11.0';
		o.rmempty = false;
		o.placeholder = 'sing-box 1.11.0';
		[
			['sing-box 1.11.0',                 'sing-box ' + _('(core, recommended)')],
			['SFI/1.11.0 (sing-box 1.11.0)',    'sing-box iOS (SFI)'],
			['SFA/1.11.0 (sing-box 1.11.0)',    'sing-box Android (SFA)'],
			['SFM/1.11.0 (sing-box 1.11.0)',    'sing-box macOS (SFM)'],
			['mihomo/1.19.0',                   'mihomo (Clash.Meta)'],
			['clash-verge/v2.0.3',              'Clash Verge Rev'],
			['ClashforWindows/0.19.23',         'Clash for Windows'],
			['ClashMetaForAndroid/2.11.0.Meta', 'Clash Meta for Android'],
			['Clash/v1.18.0',                   'Clash Premium'],
			['v2rayN/7.0.0',                    'v2rayN ' + _('(base64 node list)')],
			['v2rayNG/1.9.16',                  'v2rayNG ' + _('(base64 node list)')],
			['Shadowrocket/2.2.35',             'Shadowrocket'],
			['Quantumult%20X/1.0.30',           'Quantumult X'],
			['Surge/2900',                      'Surge'],
			['Stash/2.7.0',                     'Stash'],
			['Loon/3.2.0',                      'Loon'],
			['Karing/1.0.0',                    'Karing'],
			['NekoBox/1.3.6',                   'NekoBox for Android']
		].forEach(function(preset) { o.value(preset[0], preset[1] + ' - ' + preset[0]); });

		o = s.option(form.Value, 'password', _('Converter access password'));
		o.password = true;
		o.rmempty = false;

		o = s.option(form.Value, 'port', _('Converter service port'));
		o.datatype = 'port';
		o.default = '9716';
		o.rmempty = false;

		o = s.option(form.Value, 'refresh_interval', _('Subscription refresh interval'), _('Minutes. This maps to subscription.refresh_interval in config.yaml.'));
		o.datatype = 'uinteger';
		o.default = '360';

		o = s.option(form.ListValue, 'default_template', _('Default template'),
			_('Which template is used when a request does not specify one. It must be a template that is enabled in the Templates tab.'));
		o.rmempty = false;
		var seenTpl = {};
		for (var i = 0; i < templates.length; i++) {
			if (!templates[i].enabled)
				continue;
			o.value(templates[i].id, '%s (%s)'.format(templates[i].name || templates[i].id, templates[i].id));
			seenTpl[templates[i].id] = true;
		}
		if (!Object.keys(seenTpl).length)
			o.placeholder = _('Enable at least one template first.');

		o = s.option(form.Value, 'output_config', _('Output config path'), _('The generated file is written here after validation. A sing-box runtime such as OpenWrt-momo can use this profile path.'));
		o.default = '/etc/momo/profiles/config.json';

		o = s.option(form.Value, 'template_base_url', _('Template base URL'), _('Local HTTP URL prefix used by the converter to fetch JSON templates.'));
		o.default = 'http://127.0.0.1/singbox-formula/templates';

		return m.render().then(L.bind(function(formEl) {
			// Keep the status card current without a manual page reload; paused
			// while a button action is mid-flight so it cannot fight the spinner.
			poll.add(L.bind(function() {
				if (this._busy)
					return Promise.resolve();
				return this.reloadStatus();
			}, this), 5);
			return E('div', {}, [
				formEl,
				this.renderIntegration(status),
				this.renderStatus(status)
			]);
		}, this));
	},

	renderIntegration: function(status) {
		var url = status.converted_url || '';
		var lanUrl = status.lan_url || '';

		function urlRow(id, label, value, hint) {
			return E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, label),
				E('div', { 'class': 'cbi-value-field' }, [
					E('input', { 'id': id, 'class': 'cbi-input-text', 'style': 'width:70%', 'readonly': 'readonly', 'value': value }),
					' ',
					E('button', {
						'class': 'btn cbi-button cbi-button-apply',
						'click': function(ev) { ev.preventDefault(); copyText(value); }
					}, _('Copy URL')),
					E('div', { 'class': 'cbi-value-description' }, hint)
				])
			]);
		}

		var children = [
			E('h3', {}, _('Sing-Box Integration')),
			E('p', {}, _('This converter produces a sing-box JSON profile at the output path and also serves it over the URLs below. Point your sing-box runtime (for example OpenWrt-momo) at one of them, or let it read the output file, so it fetches the generated profile from this router.')),
			E('p', {}, [
				E('a', {
					'class': 'btn cbi-button',
					'href': 'https://github.com/nikkinikki-org/OpenWrt-momo',
					'target': '_blank',
					'rel': 'noreferrer'
				}, _('OpenWrt-momo on GitHub'))
			]),
			urlRow('sbsc_converted_url', _('Converter URL (this device)'), url,
				_('For sing-box runtimes running on this OpenWrt device itself.'))
		];

		if (lanUrl)
			children.push(urlRow('sbsc_lan_url', _('Converter URL (LAN)'), lanUrl,
				_('For clients on your local network — phones, PCs, other routers.')));
		else
			children.push(E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Converter URL (LAN)')),
				E('div', { 'class': 'cbi-value-field' },
					E('em', {}, _('LAN address unavailable — could not read an IPv4 address from the lan interface.')))
			]));

		children.push(E('p', { 'class': 'cbi-value-description' },
			_('Both URLs are generated from saved settings. Save & Apply first if you changed the port, password or default template. The LAN URL carries the converter password in plain text, so treat it as a secret and only use it on a network you trust.')));

		return E('div', { 'class': 'cbi-section' }, children);
	},

	// Save & Apply flow. Mirrors the core implementation (handleSave, then
	// ui.changes.apply — which is fire-and-forget), and afterwards reconciles
	// the running state with the switch. Because apply is asynchronous, we
	// detect the committed generated configuration by content digest. This also
	// distinguishes first creation and equal-size changes within one second.
	handleSaveApply: function(ev, mode) {
		var self = this;
		return callStatus().catch(function() { return self._lastStatus || {}; }).then(function(pre) {
			var preDigest = (pre && pre.config_digest) || '';
			return self.handleSave(ev).then(function() {
				ui.changes.apply(mode == '0');
				return self.reconcileAfterApply(preDigest);
			});
		});
	},

	reconcileAfterApply: function(preDigest) {
		var self = this, tries = 0, lastSt = null;
		var step = function() {
			return callStatus().then(function(st) {
				lastSt = st || {};
				self._applyStatus(lastSt);
				var changed = !!(lastSt.config_digest && lastSt.config_digest !== preDigest);
				if (!changed && tries++ < 15)
					return new Promise(function(r) { window.setTimeout(r, 1000); }).then(step);
				return self._reconcile(lastSt, changed);
			}).catch(function() {
				if (tries++ < 15)
					return new Promise(function(r) { window.setTimeout(r, 1000); }).then(step);
			});
		};
		return step();
	},

	_reconcile: function(st, changed) {
		var desired = !!(st && st.enabled);
		var running = !!(st && st.running);
		if (desired && (!running || changed))
			return this.doAction('restart', changed ? _('Settings applied — converter restarted.') : _('Converter started.'));
		if (!desired && running)
			return this.doAction('stop', _('Converter stopped.'));
		return Promise.resolve();
	},

	// Run an action with a spinner on the clicked button; results appear as a
	// floating toast. Background actions (refresh/check/update) are polled via
	// status until the backend marks them done; the status card (including the
	// update log) live-refreshes while they run.
	doAction: function(name, successMsg, btn) {
		var self = this;
		if (self._busy)
			return Promise.resolve();
		self._busy = true;
		if (btn) { btn.classList.add('spinning'); btn.disabled = true; }
		var finish = function() {
			self._busy = false;
			if (btn && btn.isConnected) { btn.classList.remove('spinning'); btn.disabled = false; }
		};
		var request = ACTION_CALLS[name] ? ACTION_CALLS[name]() : callServiceAction(name);
		return request.then(strictResult).then(function(res) {
			var code = res.code;
			var out = res.output.replace(/\s+$/, '');
			if (ASYNC[name] && code === 0 && out === 'queued') {
				return self.waitAction(name).then(function(st) {
					finish();
					if (st && st.action === name && st.action_state === 'done' && st.action_code === 0)
						toast(successMsg || _('Done.'));
					else if (st && st.action === name && st.action_state === 'running')
						toast(_('Still running in the background — watch the update log below.'), true);
					else if (st && st.action === name && st.action_state === 'stale')
						toast(_('Operation was interrupted — see the update log below.'), true);
					else if (st && st.action_code === 75)
						toast(_('Another update is already running, or a stale lock is left in /var/run/singbox-formula/. Check the update log below.'), true);
					else
						toast(_('Operation failed (exit %d) — see the update log below for details.').format(
							(st && typeof st.action_code === 'number') ? st.action_code : -1), true);
					return self.reloadStatus();
				});
			}
			if (ASYNC[name] && code === 0)
				throw new Error(_('Invalid asynchronous response from RPC backend.'));
			finish();
			if (code !== 0)
				toast(out || _('Command failed with code %d').format(code), true);
			else
				toast(out || successMsg || _('Done.'));
			return self.reloadStatus();
		}).catch(function(err) {
			finish();
			toast((err && err.message) || String(err), true);
		});
	},

	waitAction: function(name) {
		var self = this, waited = 0, last = null;
		var step = function() {
			return callStatus().then(function(st) {
				last = st || {};
				self._applyStatus(last);
				if (last.action === name && last.action_state === 'running' && waited < 180) {
					waited += 2;
					return new Promise(function(r) { window.setTimeout(r, 2000); }).then(step);
				}
				return last;
			}).catch(function() {
				if (waited < 180) {
					waited += 2;
					return new Promise(function(r) { window.setTimeout(r, 2000); }).then(step);
				}
				return last;
			});
		};
		return step();
	},

	reloadStatus: function() {
		var self = this;
		return callStatus().then(function(status) {
			self._applyStatus(status || {});
		}).catch(function(err) {
			var stale = Object.assign({}, self._lastStatus || {});
			stale._error = (err && err.message) || String(err);
			stale._stale = true;
			self._applyStatus(stale);
		});
	},

	// Rebuild the status card in place from a status object.
	_applyStatus: function(status) {
		this._lastStatus = status;
		var el = document.getElementById('sbf_status_section');
		if (!el)
			return;
		while (el.firstChild)
			el.removeChild(el.firstChild);
		var kids = this.statusChildren(status);
		for (var i = 0; i < kids.length; i++) {
			var k = kids[i];
			if (k === '' || k == null)
				continue;
			if (typeof k === 'string')
				k = document.createTextNode(k);
			el.appendChild(k);
		}
	},

	renderStatus: function(status) {
		return E('div', { 'id': 'sbf_status_section', 'class': 'cbi-section' }, this.statusChildren(status));
	},

	statusChildren: function(status) {
		var self = this;
		var running = !!status.running;
		var healthy = !!status.healthy;
		var enabled = !!status.enabled;
		var bg = (status.action_state === 'running');
		var rpcError = status._error ? E('div', { 'class': 'alert-message warning' }, [
			E('strong', {}, _('RPC backend is not available.')), ' ',
			_('Displayed values are stale. Restart rpcd, then refresh this page. Error: '), String(status._error)
		]) : '';

		// Three-state: procd instance present + /health answering = Running;
		// instance present but health failing = starting up / crashed-waiting-
		// respawn; no instance = Stopped. This is what makes the status line and
		// the health check agree with each other.
		var stateEl = running
			? (healthy
				? E('strong', { 'style': 'color:green' }, _('Running'))
				: E('strong', { 'style': 'color:#c60' }, _('Running (not ready)')))
			: E('strong', { 'style': 'color:#b00' }, _('Stopped'));

		var health;
		if (!running)
			health = _('Converter is not running.');
		else if (!healthy)
			health = _('The converter process exists but its health endpoint is not answering yet.');
		else
			health = status.health ? ((typeof status.health == 'string') ? status.health : JSON.stringify(status.health, null, 2)) : '';

		var mkBtn = function(label, name, msg, extraClass) {
			return E('button', {
				'class': 'btn cbi-button' + (extraClass ? ' ' + extraClass : ''),
				'disabled': bg ? 'disabled' : null,
				'title': bg ? _('A background operation is in progress') : null,
				'click': function(ev) { ev.preventDefault(); return self.doAction(name, msg, ev.currentTarget); }
			}, label);
		};

		return [
			rpcError,
			E('h3', {}, _('Converter Service')),
			E('p', {}, [
				_('Status: '), stateEl,
				' / ', _('Autostart: '), enabled ? E('strong', { 'style': 'color:green' }, _('On')) : E('strong', {}, _('Off')),
				' / ', _('Converter version: '), String(status.converter_version || 'unknown'),
				' / ', _('Port: '), String(status.port || ''),
				' / ', _('Default template: '), String(status.default_template || ''),
				' / ', _('Output: '), String(status.output_config || '')
			]),
			bg ? E('p', { 'style': 'color:#c60' }, [
				E('em', {}, _('Background operation in progress: ') + String(status.action || '') + _(' — progress appears in the update log below.'))
			]) : '',
			E('div', { 'class': 'cbi-page-actions', 'style': 'text-align:left; margin-top:.5em' }, [
				mkBtn(_('Restart converter'), 'restart', _('Converter restarted.'), 'cbi-button-apply'), ' ',
				mkBtn(_('Generate config.yaml'), 'generate', _('config.yaml generated.')), ' ',
				mkBtn(_('Refresh subscription'), 'refresh', _('Subscription refreshed.')), ' ',
				mkBtn(_('Check generated config'), 'check', _('Generated config is valid.')), ' ',
				mkBtn(_('Update output file'), 'update', _('Output file updated.'))
			]),
			E('p', { 'class': 'cbi-value-description' }, [
				_('The converter is started and stopped by the Enable converter service switch above (Save & Apply); when settings change it is restarted automatically so they take effect. '),
				_('Refresh, Check and Update run in the background: progress appears in the update log below and a message pops up when they finish. They start the converter automatically if needed. Update output file only writes the generated JSON; it does not restart sing-box. This card refreshes automatically.')
			]),
			E('h4', {}, _('Health Check')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 180px; overflow:auto' }, health),
			E('h4', {}, _('Recent Update Log')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 220px; overflow:auto' }, status.update_log || '')
		];
	}
});
