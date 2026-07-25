'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require poll';

var callStatus = rpc.declare({ object: 'liquid_formula', method: 'status', expect: { '': {} } });
var callServiceAction = rpc.declare({ object: 'liquid_formula', method: 'service_action', params: [ 'name' ], expect: { '': {} } });
var callGenerate = rpc.declare({ object: 'liquid_formula', method: 'generate', expect: { '': {} } });
var callRefresh = rpc.declare({ object: 'liquid_formula', method: 'refresh', expect: { '': {} } });
var callCheck = rpc.declare({ object: 'liquid_formula', method: 'check', expect: { '': {} } });
var callUpdate = rpc.declare({ object: 'liquid_formula', method: 'update', expect: { '': {} } });
var callListTemplates = rpc.declare({ object: 'liquid_formula', method: 'list_templates', expect: { '': {} } });
var callReadTemplate = rpc.declare({ object: 'liquid_formula', method: 'read_template', params: [ 'id', 'file' ], expect: { '': {} } });
var callWriteTemplate = rpc.declare({ object: 'liquid_formula', method: 'write_template', params: [ 'id', 'name', 'file', 'no_node', 'enabled', 'content' ], expect: { '': {} } });
var callDeleteTemplate = rpc.declare({ object: 'liquid_formula', method: 'delete_template', params: [ 'id' ], expect: { '': {} } });

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
	}, [ String(msg) ]);
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
	var ta = E('textarea', { 'style': 'position:fixed; left:-9999px; top:-9999px' }, [ String(text) ]);
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

// momo 的 nftables 会把本机产生的包一并劫持进 sing-box, 这其中包括 FakeHTTP /
// FakeSIP 通过 raw socket 注入的伪造包 —— 它们靠 SO_MARK 打标, 但仍然走
// LOCAL_OUT, 因此会被 momo 的 mangle_output 抓走, 永远到不了 WAN。把对应的
// mark 加进 momo 的 bypass_fwmark 才能放行。
//
// 掩码必须写: momo 解析 bypass_fwmark 时, 不带 "/" 则掩码默认 0xFFFFFFFF,
// 生成的规则是 "mark & 0xffffffff == 0x8000", 只有 mark 恰好等于该值才命中。
// 而包经过 momo 打标后会变成组合值(如 0x8080), 不带掩码就漏了。
var MOMO_BYPASS = [
	{ id: 'fakehttp', config: 'fakehttp', defaultMark: '0x8000',  defaultMask: '0x8000',  tool: 'FakeHTTP', proto: 'TCP' },
	{ id: 'fakesip',  config: 'fakesip',  defaultMark: '0x10000', defaultMask: '0x10000', tool: 'FakeSIP',  proto: 'UDP' }
];

function dpiBypassMark(entry) {
	var mark = uci.get(entry.config, 'main', 'fwmark') || entry.defaultMark;
	var mask = uci.get(entry.config, 'main', 'fwmask') || entry.defaultMask;
	return String(mark) + '/' + String(mask);
}

function momoBypassList() {
	var value = uci.get('momo', 'proxy', 'bypass_fwmark');
	if (value == null)
		return null;
	if (Array.isArray(value))
		return value.slice();
	return String(value).split(/\s+/).filter(function(item) { return item.length > 0; });
}

function momoBypassSet(mark, wanted) {
	var list = momoBypassList() || [];
	var at = list.indexOf(mark);
	if (wanted && at < 0)
		list.push(mark);
	else if (!wanted && at >= 0)
		list.splice(at, 1);
	else
		return;
	if (list.length)
		uci.set('momo', 'proxy', 'bypass_fwmark', list);
	else
		uci.unset('momo', 'proxy', 'bypass_fwmark');
}

// 前端校验必须和 generate-config.sh 同步。前端松、后端严的话, Save & Apply
// 会把 UCI 提交下去而生成器拒绝, 结果是 UCI 显示新值、config.yaml 还是旧的,
// 服务也照旧跑着 —— 界面和运行态就此分裂, 而且没有任何提示。
function noControlChars(value) {
	return !/[\x00-\x1f\x7f]/.test(String(value));
}

function validateScalar(value, label) {
	if (!noControlChars(value))
		return _('%s must not contain control characters').format(label);
	return true;
}

function validateOutputPath(value, label) {
	var text = String(value || '');
	var scalar = validateScalar(text, label);
	if (scalar !== true)
		return scalar;
	if (/\/\//.test(text) || /\/(?:\.|\.\.)(?:\/|$)/.test(text))
		return _('%s must not contain empty, . or .. path segments').format(label);
	if (!/^\/(?:etc\/momo\/profiles|etc\/sing-box|var\/lib\/liquid-formula\/output)\/.*\.json$/.test(text))
		return _('%s must be a JSON file under /etc/momo/profiles, /etc/sing-box or /var/lib/liquid-formula/output').format(label);
	return true;
}

function validateTemplateBaseUrl(value) {
	var text = String(value || '');
	var scalar = validateScalar(text, _('The template base URL'));
	var match;
	if (scalar !== true)
		return scalar;
	match = text.match(/^http:\/\/(?:127\.0\.0\.1|localhost)(?::([^/]+))?(?:\/.*)?$/);
	if (!match)
		return _('The template base URL must point at http://127.0.0.1 or http://localhost');
	if (match[1] != null &&
	    (!/^[1-9][0-9]{0,4}$/.test(match[1]) || Number(match[1]) > 65535))
		return _('The template base URL port must be an integer from 1 to 65535');
	return true;
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
		var self = this;
		return Promise.all([
			uci.load('liquid_formula'),
			safe(callStatus(), {}),
			safe(callListTemplates(), { templates: [] }),
			// momo 是可选运行时。没装就不显示这一节, 而不是抛错。
			uci.load('momo').then(function() { self._momo = true; },
			                      function() { self._momo = false; }),
			// mark/mask 可在两个 DPI 页面修改，Overview 必须读取同一份 UCI，
			// 不能继续把默认值当成实际运行值。
			safe(uci.load('fakehttp'), {}),
			safe(uci.load('fakesip'), {})
		]);
	},

	render: function(data) {
		var status = data[1] || {};
		var templates = (data[2] && data[2].templates) ? data[2].templates : [];
		var m, s, o;

		this._lastStatus = status;
		if (data[2] && Array.isArray(data[2].templates) && !data[2]._error) {
			this._enabledTemplateCount = templates.filter(function(template) {
				return !!template.enabled;
			}).length;
		} else {
			this._enabledTemplateCount = undefined;
		}

		m = new form.Map('liquid_formula', _('Liquid Formula'),
			_('Convert a source subscription into a sing-box JSON profile and update the configured output file. This app does not manage the sing-box runtime — use a runtime such as OpenWrt-momo to run sing-box, firewall rules, access control and profile scheduling.'));

		s = m.section(form.NamedSection, 'main', 'global', _('Basic Settings'));
		s.anonymous = true;

		o = s.option(form.Flag, 'enabled', _('Enable converter service'),
			_('Master switch. On Save & Apply this page brings the service in line with the switch: it starts the converter when enabled, stops it when disabled, and restarts it when settings changed so they take effect. When enabled, it also autostarts on boot (after the boot delay below).'));
		o.default = '0';

		o = s.option(form.Value, 'boot_delay', _('Boot delay'),
			_('Seconds to wait before autostarting on boot, 0 to 600. This delay applies ONLY to autostart on boot; starting via Save & Apply or the buttons is immediate.'));
		o.datatype = 'range(0,600)';
		o.default = '90';

		o = s.option(form.Value, 'subscription_url', _('Source subscription URL'),
			_('Paste the link exactly as your provider gives it, including any extra query parameter your panel needs.'));
		o.rmempty = false;
		o.placeholder = 'https://example.com/your/subscription';
		o.validate = function(section_id, value) {
			if (!value)
				return _('The subscription URL is required');
			if (!/^https?:\/\/.+/.test(value))
				return _('The subscription URL must start with http:// or https://');
			return validateScalar(value, _('The subscription URL'));
		};

		o = s.option(form.Value, 'user_agent', _('Subscription User-Agent'),
			_('Most provider panels decide what to send - sing-box JSON, Clash YAML or a base64 node list - from this header, and reject unknown or outdated clients with "client version too low". Pick a preset or type your own. The converter now decodes base64 / URI lists automatically, so a v2rayN style UA is a safe fallback when your provider has no sing-box support.'));
		o.default = 'sing-box 1.11.0';
		o.rmempty = false;
		o.placeholder = 'sing-box 1.11.0';
		o.validate = function(section_id, value) {
			if (!value)
				return true;
			if (value.length > 200)
				return _('The User-Agent must be at most 200 characters');
			if (!/^[\x20-\x7e]*$/.test(value))
				return _('The User-Agent must be printable ASCII');
			return true;
		};
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
		o.validate = function(section_id, value) {
			if (!value)
				return _('The password must not be empty');
			return validateScalar(value, _('The password'));
		};

		o = s.option(form.Value, 'port', _('Converter service port'));
		o.datatype = 'port';
		o.default = '9716';
		o.rmempty = false;

		o = s.option(form.Value, 'refresh_interval', _('Subscription refresh interval'), _('Minutes, 1 to 10080 (one week). This maps to subscription.refresh_interval in config.yaml.'));
		o.datatype = 'range(1,10080)';
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
		o.validate = function(section_id, value) {
			return validateOutputPath(value, _('The output config path'));
		};

		o = s.option(form.Value, 'template_base_url', _('Template base URL'), _('Local HTTP URL prefix used by the converter to fetch JSON templates. It must stay on the loopback interface.'));
		o.default = 'http://127.0.0.1/liquid-formula/templates';
		o.validate = function(section_id, value) {
			return validateTemplateBaseUrl(value);
		};

		if (this._momo) {
			s = m.section(form.NamedSection, 'main', 'global', _('momo Firewall Bypass'),
				_('momo redirects locally generated packets into sing-box as well. Anti-DPI helpers such as FakeHTTP and FakeSIP inject forged packets from a raw socket, so momo swallows them and they never reach the WAN. Each switch below keeps its firewall mark in momo\'s bypass list so those packets pass through untouched.') + ' ' +
				_('These switches are enforced into momo\'s own configuration (momo.proxy.bypass_fwmark) on Save & Apply. Any further bypass rules — other marks, DSCP, IP ranges — belong in momo\'s own Proxy → Bypass page, not here.'));
			s.anonymous = true;

			MOMO_BYPASS.forEach(function(entry) {
				var mark = dpiBypassMark(entry);
				var bare = mark.split('/')[0];
				o = s.option(form.Flag, 'momo_bypass_' + entry.id,
					_('Bypass %s (%s)').format(entry.tool, mark),
					_('Skip momo for %s packets marked %s. Enable this if you run %s with "-m %s". Leaving it on is harmless: a mark nothing sets simply never matches.')
						.format(entry.proto, bare, entry.tool, bare));
				o.default = '1';
				o.rmempty = false;
				// 每次 Save & Apply 都把状态推进 momo, 否则用户没动过开关时
				// LuCI 会跳过 write, 推荐配置就永远落不了盘。
				o.forcewrite = true;
				o.write = function(section_id, value) {
					uci.set('liquid_formula', section_id, this.option, value);
					momoBypassSet(dpiBypassMark(entry), value == '1');
				};
				o.remove = function(section_id) {
					uci.set('liquid_formula', section_id, this.option, '0');
					momoBypassSet(dpiBypassMark(entry), false);
				};
			});
		}

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
				this.renderStatus(status),
				// 模板管理原本是独立页面, 现在收进本页, 排在更新日志之后。
				this.renderTemplateManager(templates)
			]);
		}, this));
	},

	renderIntegration: function(status) {
		var url = status.converted_url || '';
		var lanUrl = status.lan_url || '';

		function urlRow(id, label, value, hint) {
			return E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, [ label ]),
				E('div', { 'class': 'cbi-value-field' }, [
					E('input', { 'id': id, 'class': 'cbi-input-text', 'style': 'width:70%', 'readonly': 'readonly', 'value': value }),
					' ',
					E('button', {
						'class': 'btn cbi-button cbi-button-apply',
						'click': function(ev) { ev.preventDefault(); copyText(value); }
					}, _('Copy URL')),
					E('div', { 'class': 'cbi-value-description' }, [ hint ])
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
				// 摘要没变说明生成器没有产出新的 config.yaml。UCI 已经提交,
				// 但运行的还是旧配置 —— 不说出来的话界面显示新值、服务跑旧值,
				// 用户完全看不出。
				if (!changed)
					ui.addNotification(null, E('p', {}, [
						_('Settings were saved, but the converter configuration was not regenerated, so the running service still uses the previous values. '),
						lastSt.config_error
							? E('code', {}, [ String(lastSt.config_error) ])
							: _('Check the update log below for the reason.')
					]), 'warning');
				else if (lastSt.config_error)
					ui.addNotification(null, E('p', {}, [
						_('The converter reports a configuration problem: '),
						E('code', {}, [ String(lastSt.config_error) ])
					]), 'warning');
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
						toast(_('Another update is already running, or a stale lock is left in /var/run/liquid-formula/. Check the update log below.'), true);
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

	// 与 rpcd 的 worker watchdog 使用同一预算。refresh 只发一次请求；check /
	// update 还可能依次等待临时服务启动、刷新和拉取成品，因此是三段预算。
	// 两边不一致会让界面在合法后台动作尚未结束时提前报告“仍在运行”。
	actionWaitSeconds: function(name) {
		var rawTimeout = String(uci.get('liquid_formula', 'main', 'subscription_timeout') || '60');
		if (!/^(?:0|[1-9][0-9]*)$/.test(rawTimeout) ||
		    Number(rawTimeout) < 5 || Number(rawTimeout) > 600)
			return 11160;
		var timeout = Number(rawTimeout);
		var enabled = this._enabledTemplateCount;
		if (typeof enabled !== 'number') {
			enabled = 0;
			(uci.sections('liquid_formula', 'template') || []).forEach(function(section) {
				if (section.enabled === undefined || section.enabled === '1')
					enabled++;
			});
		}
		var requestTimeout = Math.min(timeout * (enabled + 1) + 60, 3660);
		if (name === 'refresh')
			return requestTimeout + 30;
		if (name === 'check' || name === 'update')
			return requestTimeout * 3 + 180;
		return 11160;
	},

	waitAction: function(name) {
		var self = this, waited = 0, last = null;
		var limit = this.actionWaitSeconds(name);
		var step = function() {
			return callStatus().then(function(st) {
				last = st || {};
				self._applyStatus(last);
				if (last.action === name && last.action_state === 'running' && waited < limit) {
					waited += 2;
					return new Promise(function(r) { window.setTimeout(r, 2000); }).then(step);
				}
				return last;
			}).catch(function() {
				if (waited < limit) {
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
			}, [ label ]);
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
				E('em', {}, [
					_('Background operation in progress: ') + String(status.action || '') +
						_(' — progress appears in the update log below.')
				])
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
				_('Refresh, Check and Update run in the background: progress appears in the update log below and a message pops up when they finish. Refresh needs the converter already running. Check and Update will start it temporarily if it is off and stop it again when they finish, so the master switch is left as you set it. Update output file only writes the generated JSON; it does not restart sing-box. This card refreshes automatically.')
			]),
			E('h4', {}, _('Health Check')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 180px; overflow:auto' }, [ health ]),
			E('h4', {}, _('Recent Update Log')),
			E('pre', { 'style': 'white-space:pre-wrap; max-height: 220px; overflow:auto' },
				[ String(status.update_log || '') ])
		];
	},

	buildTemplateRow: function(t) {
		var self = this;
		return E('tr', { 'class': 'tr cbi-section-table-row' }, [
			E('td', { 'class': 'td cbi-section-table-cell', 'data-title': 'ID' }, [ String(t.id || '') ]),
			E('td', { 'class': 'td cbi-section-table-cell', 'data-title': _('Enabled') },
				[ t.enabled ? _('Yes') : _('No') ]),
			E('td', { 'class': 'td cbi-section-table-cell', 'data-title': _('Name') }, [ String(t.name || '') ]),
			E('td', { 'class': 'td cbi-section-table-cell', 'data-title': _('File') }, [ String(t.file || '') ]),
			E('td', { 'class': 'td cbi-section-table-cell', 'data-title': _('Size') }, [ String(t.size || 0) ]),
			E('td', { 'class': 'td cbi-section-table-cell', 'data-title': _('Modified') }, [ String(t.mtime || '') ]),
			E('td', {
				'class': 'td cbi-section-table-cell nowrap cbi-section-actions',
				'data-title': _('Actions')
			}, E('div', {}, [
				E('button', { 'class': 'btn cbi-button', 'click': function(ev) { ev.preventDefault(); self.loadTemplate(t); } }, [ _('Edit') ]),
				' ',
				E('button', { 'class': 'btn cbi-button-negative', 'click': function(ev) { ev.preventDefault(); self.deleteTemplate(t.id); } }, [ _('Delete') ])
			]))
		]);
	},

	// Re-fetch the template list and rebuild the table in place, so Save/Delete
	// reflect immediately without a manual page reload.
	reloadTemplateList: function() {
		var self = this;
		return callListTemplates().then(function(res) {
			var templates = (res && Array.isArray(res.templates)) ? res.templates : [];
			if (!res || !Array.isArray(res.templates))
				throw new Error(_('Invalid response from RPC backend.'));
			self._enabledTemplateCount = templates.filter(function(template) {
				return !!template.enabled;
			}).length;
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
		var tbody = E('tbody', {
			'id': 'sbsc_tpl_tbody',
			'class': 'tbody cbi-section-tbody'
		});
		var self = this;
		for (var i = 0; i < templates.length; i++)
			tbody.appendChild(this.buildTemplateRow(templates[i]));

		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, _('Template Management')),
			E('p', {}, _('Upload a local JSON template, edit an existing template, save a new template or delete unused templates. The current default template (set in the Overview tab) cannot be deleted.')),
			E('table', { 'class': 'table cbi-section-table' }, [
				E('thead', { 'class': 'thead cbi-section-thead' }, E('tr', {
					'class': 'tr cbi-section-table-titles anonymous'
				}, [
					E('th', { 'class': 'th cbi-section-table-cell' }, 'ID'),
					E('th', { 'class': 'th cbi-section-table-cell' }, _('Enabled')),
					E('th', { 'class': 'th cbi-section-table-cell' }, _('Name')),
					E('th', { 'class': 'th cbi-section-table-cell' }, _('File')),
					E('th', { 'class': 'th cbi-section-table-cell' }, _('Size')),
					E('th', { 'class': 'th cbi-section-table-cell' }, _('Modified')),
					E('th', {
						'class': 'th cbi-section-table-cell cbi-section-actions'
					}, _('Actions'))
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
		var self = this;
		return callReadTemplate(t.id, t.file).then(function(res) {
			if (!res || res.ok !== true)
				return toast((res && res.error) || _('Invalid response from RPC backend.'), true);
			document.getElementById('sbsc_tpl_id').value = t.id || '';
			document.getElementById('sbsc_tpl_name').value = t.name || '';
			document.getElementById('sbsc_tpl_file').value = t.file || res.file || '';
			document.getElementById('sbsc_tpl_no_node').value = t.no_node || '➜ Direct';
			document.getElementById('sbsc_tpl_enabled').checked = !!t.enabled;
			document.getElementById('sbsc_tpl_content').value = res.content || '';
			document.getElementById('sbsc_tpl_id').readOnly = true;
			document.getElementById('sbsc_tpl_file').readOnly = true;
			self._editingTemplate = t.id || '';
		}).catch(function(err) { toast((err && err.message) || String(err), true); });
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
		var done = function() { if (btn) { btn.classList.remove('spinning'); btn.disabled = false; } };
		if (btn) { btn.classList.add('spinning'); btn.disabled = true; }
		self.setSaveStatus('', false);
		if (new TextEncoder().encode(content).length > 1048576) {
			self.setSaveStatus(_('Template exceeds the 1 MiB limit.'), true);
			done();
			return Promise.resolve();
		}
		return callWriteTemplate(id, name, file, noNode, enabled, content).then(function(res) {
			if (!res || res.ok !== true || res.phase !== 'complete') {
				self.setSaveStatus((res && res.error) || _('Invalid response from RPC backend.'), true);
				done();
				return;
			}
			// 后端在重启失败时仍然返回 ok:true(改动确实已经落盘), 但会带一条
			// warning。不显示的话用户会以为新模板已经生效。
			if (res.warning)
				self.setSaveStatus(_('Template saved, but: ') + String(res.warning), true);
			else
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
			if (!res || res.ok !== true || res.phase !== 'complete')
				return toast((res && res.error) || _('Invalid response from RPC backend.'), true);
			if (res.warning)
				toast(_('Template deleted, but: ') + String(res.warning), true);
			else
				toast(_('Template deleted.'));
			return self.reloadTemplateList();
		}).catch(function(err) { toast((err && err.message) || String(err), true); });
	},

	newTemplate: function() {
		document.getElementById('sbsc_tpl_id').value = '';
		document.getElementById('sbsc_tpl_name').value = '';
		document.getElementById('sbsc_tpl_file').value = '';
		document.getElementById('sbsc_tpl_no_node').value = '➜ Direct';
		document.getElementById('sbsc_tpl_enabled').checked = true;
		document.getElementById('sbsc_tpl_content').value = '';
		document.getElementById('sbsc_tpl_id').readOnly = false;
		document.getElementById('sbsc_tpl_file').readOnly = false;
		this._editingTemplate = '';
	}
});
