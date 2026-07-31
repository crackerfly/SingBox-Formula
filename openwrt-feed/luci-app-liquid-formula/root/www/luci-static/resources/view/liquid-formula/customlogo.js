'use strict';
'require view';
'require form';
'require uci';
'require request';
'require rpc';
'require ui';

var DEFAULT_LOGO = '/etc/liquid-formula/assets/default-logo.svg';
var ASSET_PREFIX = '/etc/liquid-formula/assets/';
var MAX_UPLOAD_SIZE = 512 * 1024;
var UPLOAD_URL = (L.env.cgi_base || '/cgi-bin') + '/liquid-formula-upload';
var TUNING_RPC_TIMEOUT = 45;
var TUNING_RELOAD_DELAY = 60;
var TUNING_CLIENT_GUARD = 50;

var callTuningStatus = rpc.declare({ object: 'liquid_formula', method: 'tuning_status', expect: { '': {} } });
var callTuningApply  = rpc.declare({ object: 'liquid_formula', method: 'tuning_apply',  expect: { '': {} } });

// 这些选项存在 tuning 配置里, 但和 Custom Logo 共用一个 form.Map。
// uci.save() 会把所有已加载的配置一起提交, 所以跨配置读写是安全的。
function tuningValue(option, fallback) {
	var value = uci.get('tuning', 'main', option);
	return (value == null || value === '') ? fallback : String(value);
}

function bindTuning(o, option, fallback) {
	o.cfgvalue = function() { return tuningValue(option, fallback); };
	o.write = function(section_id, value) { uci.set('tuning', 'main', option, value); };
	o.remove = function() { uci.set('tuning', 'main', option, fallback); };
	o.forcewrite = true;
	return o;
}

function liveRow(label, value, note) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'style': 'width:38%' }, [ label ]),
		E('td', { 'class': 'td left' }, [
			E('strong', {}, [ value || '?' ]),
			note ? E('span', { 'class': 'cbi-value-description' }, [ ' ' + note ]) : ''
		])
	]);
}

function basename(path) {
	var parts = String(path || '').split(/[\\/]/);
	return parts[parts.length - 1] || '';
}

function extension(path) {
	var name = basename(path);
	var pos = name.lastIndexOf('.');
	return pos >= 0 ? name.substring(pos + 1).toLowerCase() : '';
}

function safeFilename(name, extensions) {
	return /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name) &&
		name.length <= 96 && extensions.indexOf(extension(name)) >= 0;
}

var SecureUploadValue = form.Value.extend({
	__init__: function(map, section, option, title, description, settings) {
		this.super('__init__', [map, section, option, title, description]);
		this.uploadKind = settings.kind;
		this.extensions = settings.extensions;
		this.accept = settings.accept;
		this.stagingPath = settings.stagingPath;
		this.builtinPath = settings.builtinPath;
	},

	renderWidget: function(section_id, option_index, cfgvalue) {
		var current = cfgvalue || this.default || '';
		var readonly = (this.readonly != null) ? this.readonly : this.map.readonly;
		var valueWidget = new ui.Textfield(current, {
			id: this.cbid(section_id),
			optional: true,
			readonly: true,
			validate: this.getValidator(section_id),
			disabled: readonly
		});
		var valueNode = valueWidget.render();
		var fileInput = E('input', {
			'type': 'file',
			'accept': this.accept,
			'disabled': readonly ? '' : null,
			'style': 'display:none'
		});
		var status = E('span', {
			'class': 'upload-status',
			'aria-live': 'polite',
			'style': 'margin-inline-start:.75em'
		});
		var uploadButton = E('button', {
			'type': 'button',
			'class': 'btn cbi-button cbi-button-action',
			'disabled': readonly ? '' : null,
			'click': function(ev) {
				ev.preventDefault();
				fileInput.click();
			}
		}, _('Upload file…'));
		var builtinButton = this.builtinPath ? E('button', {
			'type': 'button',
			'class': 'btn cbi-button',
			'disabled': readonly ? '' : null,
			'style': 'margin-inline-start:.5em',
			'click': function(ev) {
				ev.preventDefault();
				valueWidget.setValue(this.builtinPath);
				valueNode.querySelector('input').dispatchEvent(new Event('change', { bubbles: true }));
				status.textContent = _('Built-in asset selected.');
			}.bind(this)
		}, _('Use built-in asset')) : null;

		fileInput.addEventListener('change', function() {
			var file = fileInput.files && fileInput.files[0];
			var name = file ? basename(file.name) : '';

			if (!file)
				return;

			if (!safeFilename(name, this.extensions)) {
				ui.addNotification(null, E('p', [
					_('Unsupported file name or type. Allowed extensions: %s.')
						.format(this.extensions.join(', '))
				]));
				fileInput.value = '';
				return;
			}

			if (file.size < 1 || file.size > MAX_UPLOAD_SIZE) {
				ui.addNotification(null, E('p',
					_('The file must be between 1 byte and 512 KiB.')));
				fileInput.value = '';
				return;
			}

			var data = new FormData();
			data.append('sessionid', rpc.getSessionID());
			data.append('filename', this.stagingPath);
			data.append('filedata', file);
			status.textContent = _('Uploading…');
			uploadButton.disabled = true;
			if (builtinButton)
				builtinButton.disabled = true;

			request.post('%s?kind=%s&name=%s'.format(
				UPLOAD_URL, encodeURIComponent(this.uploadKind), encodeURIComponent(name)), data, {
				timeout: 0,
				progress: function(pev) {
					if (pev.total)
						status.textContent = _('Uploading… %s%%')
							.format(Math.floor((pev.loaded / pev.total) * 100));
				}
			}).then(function(res) {
				var reply = res.json();

				if (!reply || reply.ok !== true || !reply.path)
					throw new Error(reply && reply.error ? reply.error : _('Upload failed.'));

				valueWidget.setValue(reply.path);
				valueNode.querySelector('input').dispatchEvent(new Event('change', { bubbles: true }));
				status.textContent = _('Upload complete. Click “Save & Apply” to activate it.');
			}).catch(function(err) {
				status.textContent = _('Upload failed.');
				ui.addNotification(null, E('p', [
					_('Upload failed: %s').format(err.message || err)
				]));
			}).finally(function() {
				uploadButton.disabled = readonly;
				if (builtinButton)
					builtinButton.disabled = readonly;
				fileInput.value = '';
			});
		}.bind(this));

		// form.Value.renderFrame() supplies the surrounding
		// .cbi-value-field. Returning another one here creates a nested field
		// with an extra Bootstrap margin and inconsistent third-party-theme
		// layout.
		return E('div', {}, [
			valueNode,
			E('div', { 'style': 'margin-top:.5em' }, [
				fileInput,
				uploadButton,
				builtinButton,
				status
			])
		]);
	},

	validate: function(section_id, value) {
		var name;

		if (!value || (this.builtinPath && value === this.builtinPath))
			return true;

		if (String(value).indexOf(ASSET_PREFIX) !== 0)
			return _('Select the built-in logo or upload a file from this page.');

		name = basename(value);
		if (value !== ASSET_PREFIX + name || !safeFilename(name, this.extensions))
			return _('The selected asset path is invalid.');

		return true;
	}
});

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('customlogo'),
			uci.load('tuning').catch(function() {}),
			callTuningStatus().catch(function() { return null; })
		]);
	},

	render: function(data) {
		var m, s, o;
		var tuning = (data && data[2]) || {};
		var live = tuning.live || {};
		var irq = tuning.irqbalance || {};

		m = new form.Map('customlogo', _('Tuning Utility'),
			_('Use the trusted built-in SVG or upload a PNG logo and a PNG/ICO browser icon. User-supplied SVG files are intentionally blocked for security.'));

		s = m.section(form.NamedSection, 'main', 'customlogo', _('Basic Settings'));
		s.addremove = false;
		s.anonymous = true;

		o = s.option(form.Flag, 'enable', _('Enable Custom Logo'));
		o.rmempty = false;

		o = s.option(SecureUploadValue, 'logo', _('Navigation Bar Logo'),
			_('Default: the built-in SVG supplied with this package. Custom files must be PNG and no larger than 512 KiB.'), {
				kind: 'logo',
				extensions: ['png'],
				accept: '.png,image/png',
				stagingPath: '/var/run/liquid-formula-upload/pending-logo',
				builtinPath: DEFAULT_LOGO
			});
	o.default = DEFAULT_LOGO;
	o.rmempty = true;
	o.retain = true;
	o.depends('enable', '1');

		o = s.option(SecureUploadValue, 'favicon', _('Web Icon (Favicon)'),
			_('Default: the built-in SVG. Custom files must be PNG or ICO and no larger than 512 KiB.'), {
				kind: 'favicon',
				extensions: ['png', 'ico'],
				accept: '.png,.ico,image/png,image/x-icon',
				stagingPath: '/var/run/liquid-formula-upload/pending-favicon',
				builtinPath: DEFAULT_LOGO
			});
	o.default = DEFAULT_LOGO;
	o.rmempty = true;
	o.retain = true;
	o.depends('enable', '1');


		// ------------------------------------------------ 内核网络调优 ----

		s = m.section(form.NamedSection, 'main', 'customlogo', _('Kernel Network Tuning'),
			_('These settings are written to /etc/sysctl.d/99-liquid-formula.conf and applied immediately. That drop-in is owned by this package and rewritten on every apply, so nothing accumulates.'));
		s.addremove = false;
		s.anonymous = true;

		o = s.option(form.DummyValue, '_live', _('Currently active'));
		o.rawhtml = true;
		o.cfgvalue = function() {
			var rows = [
				liveRow(_('TCP Fast Open'), live.tcp_fastopen),
				liveRow(_('Default queueing discipline'), live.default_qdisc),
				liveRow(_('Congestion control'), live.congestion_control),
				liveRow(_('SYN backlog'), live.tcp_max_syn_backlog),
				liveRow(_('irqbalance'),
					irq.installed === false ? _('not installed')
						: (irq.running ? _('running') : _('stopped')))
			];
			var notes = [];

			if (tuning.sysctl_conf_conflict)
				notes.push(E('p', { 'class': 'alert-message warning' },
					_('/etc/sysctl.conf still sets one of these keys. OpenWrt loads it after /etc/sysctl.d/, so it overrides this page. Applying below moves those keys aside and keeps a .liquid-formula.bak backup next to it.')));
			if (tuning.cake_module === false)
				notes.push(E('p', { 'class': 'alert-message warning' },
					_('kmod-sched-cake is not installed, so the cake queueing discipline cannot be applied. Install it, or pick fq_codel instead.')));
			if (tuning.bbr_module === false)
				notes.push(E('p', { 'class': 'alert-message warning' },
					_('kmod-tcp-bbr is not installed, so BBR congestion control cannot be applied. Install it, or pick one of the algorithms the kernel reports as available.')));

			return E('div', {}, [E('table', { 'class': 'table' }, rows)].concat(notes)).innerHTML;
		};

		o = bindTuning(s.option(form.Flag, 'tuning_enabled', _('Manage kernel parameters'),
			_('Off by default: installing this package should not silently change kernel networking behaviour. Turning it on makes this page the owner of the four values below.')),
			'enabled', '0');

		o = bindTuning(s.option(form.ListValue, 'tuning_tcp_fastopen', _('TCP Fast Open'),
			_('Allows data in the opening handshake, saving one round trip on repeat connections.')),
			'tcp_fastopen', '3');
		o.value('0', _('0 - disabled'));
		o.value('1', _('1 - client only'));
		o.value('2', _('2 - server only'));
		o.value('3', _('3 - client and server'));
		o.depends('tuning_enabled', '1');

		o = bindTuning(s.option(form.Value, 'tuning_default_qdisc', _('Default queueing discipline'),
			_('cake needs kmod-sched-cake; fq_codel is usually built in. Anything not listed can be typed in.')),
			'default_qdisc', 'cake');
		o.value('cake', 'cake' + (tuning.cake_module === false ? ' (' + _('module missing') + ')' : ''));
		o.value('fq_codel', 'fq_codel');
		o.value('fq', 'fq');
		o.value('pfifo_fast', 'pfifo_fast');
		o.depends('tuning_enabled', '1');

		o = bindTuning(s.option(form.Value, 'tuning_congestion_control', _('Congestion control'),
			_('bbr needs kmod-tcp-bbr. Reported as available by the running kernel: %s')
				.format(tuning.available_congestion_control || _('unknown'))),
			'congestion_control', 'bbr');
		o.value('bbr', 'bbr' + (tuning.bbr_module === false ? ' (' + _('module missing') + ')' : ''));
		String(tuning.available_congestion_control || '').split(/\s+/).forEach(function(name) {
			if (name && name !== 'bbr')
				o.value(name, name);
		});
		o.depends('tuning_enabled', '1');

		o = bindTuning(s.option(form.Value, 'tuning_backlog', _('SYN backlog'),
			_('Length of the half-open connection queue. Minimum 128.')),
			'tcp_max_syn_backlog', '512');
		o.datatype = 'min(128)';
		o.depends('tuning_enabled', '1');

		o = bindTuning(s.option(form.Flag, 'tuning_irqbalance', _('Balance hardware interrupts'),
			_('MT7622 is a dual-core A53. With irqbalance off, NIC interrupts tend to pile onto CPU0, and a saturated core caps throughput on high-traffic or PPPoE forwarding. This writes irqbalance\'s own configuration and restarts the service.')),
			'irqbalance', '0');
		if (irq.installed === false)
			o.description = _('The irqbalance package is not installed, so this switch has no effect until you install it.');

		return m.render();
	},

	_clearTuningApplyListeners: function() {
		if (this._tuningAppliedListener)
			document.removeEventListener('uci-applied', this._tuningAppliedListener);
		if (this._tuningRevertedListener)
			document.removeEventListener('uci-reverted', this._tuningRevertedListener);
		this._tuningAppliedListener = null;
		this._tuningRevertedListener = null;
	},

	_clearTuningApplyGuard: function() {
		if (this._tuningApplyGuard)
			window.clearTimeout(this._tuningApplyGuard);
		this._tuningApplyGuard = null;
	},

	_restoreApplyEnvironment: function() {
		if (this._originalApplyDisplay !== undefined) {
			L.env.apply_display = this._originalApplyDisplay;
			this._originalApplyDisplay = undefined;
		}
		if (this._originalRpcTimeout !== undefined) {
			L.env.rpctimeout = this._originalRpcTimeout;
			this._originalRpcTimeout = undefined;
		}
	},

	_runTuningApply: function() {
		var self = this;
		if (this._originalRpcTimeout === undefined)
			this._originalRpcTimeout = L.env.rpctimeout;
		L.env.rpctimeout = Math.max(Number(L.env.rpctimeout) || 0, TUNING_RPC_TIMEOUT);

		return callTuningApply().then(function(res) {
			return res || { code: 1, output: 'the tuning helper returned nothing' };
		}, function(error) {
			return { code: 1, output: String((error && error.message) || error || 'RPC call failed') };
		}).then(function(res) {
			// rpc.call() reads rpctimeout when the request starts, so restoring it
			// here does not shorten this request.
			if (self._originalRpcTimeout !== undefined) {
				L.env.rpctimeout = self._originalRpcTimeout;
				self._originalRpcTimeout = undefined;
			}
			return res;
		});
	},

	_reportTuningApply: function(res, inApplyModal) {
		if (res.code === 0)
			return true;
		var text = String(res.output || '').replace(/^partial\s*/, '');
		var message = res.code === 2
			? E('p', {}, [
				_('Saved, but the kernel refused these keys (usually a missing module): '),
				E('code', {}, [ text ])
			])
			: E('p', {}, [
				_('Kernel tuning could not be applied: '),
				E('code', {}, [ text ])
			]);

		// During the official apply flow its status modal covers normal page
		// notifications. Reuse that modal so a helper failure remains visible
		// until the delayed official reload.
		if (inApplyModal && ui.changes && typeof ui.changes.displayStatus === 'function')
			ui.changes.displayStatus('warning', message);
		else
			ui.addNotification(null, message, res.code === 2 ? 'warning' : 'error');
		return false;
	},

	_hasPendingChanges: function(changes) {
		if (!changes || typeof changes !== 'object')
			return false;
		for (var config in changes)
			if (Object.prototype.hasOwnProperty.call(changes, config) &&
			    Array.isArray(changes[config]) && changes[config].length)
				return true;
		return false;
	},

	_reloadAfterTuningApply: function() {
		window.location = window.location.href.split('#')[0];
	},

	_armTuningAfterApply: function() {
		var self = this;
		this._clearTuningApplyListeners();
		this._clearTuningApplyGuard();
		this._restoreApplyEnvironment();

		this._tuningAppliedListener = function() {
			var settled = false;
			self._clearTuningApplyListeners();

			// The official handler schedules its reload only after synchronous
			// uci-applied listeners return. Raise the delay now so the helper's
			// 30-second lock wait and RPC response can finish first.
			self._originalApplyDisplay = L.env.apply_display;
			L.env.apply_display = Math.max(Number(L.env.apply_display) || 0, TUNING_RELOAD_DELAY);
			self._tuningApplyGuard = window.setTimeout(function() {
				if (settled)
					return;
				settled = true;
				self._clearTuningApplyGuard();
				self._restoreApplyEnvironment();
				self._reportTuningApply({
					code: 1,
					output: 'the tuning helper did not finish before the client safety deadline'
				}, true);
			}, TUNING_CLIENT_GUARD * 1000);

			self._runTuningApply().then(function(res) {
				if (settled)
					return;
				settled = true;
				self._clearTuningApplyGuard();
				self._restoreApplyEnvironment();
				if (self._reportTuningApply(res, true))
					self._reloadAfterTuningApply();
			});
		};
		this._tuningRevertedListener = function() {
			self._clearTuningApplyListeners();
			self._clearTuningApplyGuard();
			self._restoreApplyEnvironment();
		};
		document.addEventListener('uci-applied', this._tuningAppliedListener);
		document.addEventListener('uci-reverted', this._tuningRevertedListener);
	},

	// handleSave() stages the form values in LuCI's RPC UCI session. The
	// command-line uci used by tuning_apply cannot see that private delta
	// directory until the official apply flow commits it. LuCI 24.10, 25.12
	// and master all dispatch "uci-applied" after that commit and before their
	// delayed page reload, so run the helper from that event instead of
	// chaining from the fire-and-forget parent handleSaveApply().
	handleSaveApply: function(ev, mode) {
		var self = this;
		return this.handleSave(ev).then(function() {
			return uci.changes();
		}).then(function(changes) {
			self._clearTuningApplyListeners();
			self._clearTuningApplyGuard();
			self._restoreApplyEnvironment();
			var pending = self._hasPendingChanges(changes);
			var startOfficialApply = function() {
				try {
					// Match the official LuCI View implementation exactly. This
					// is deliberately invoked once and is not treated as a
					// Promise.
					ui.changes.apply(mode == '0');
				}
				catch (error) {
					self._clearTuningApplyListeners();
					self._clearTuningApplyGuard();
					self._restoreApplyEnvironment();
					throw error;
				}
			};

			if (!pending) {
				// No private UCI delta exists, so the helper already sees the
				// same values as the form. Run it now; the official no-changes
				// branch does not emit uci-applied.
				return self._runTuningApply().then(function(res) {
					self._reportTuningApply(res, false);
					startOfficialApply();
				});
			}

			// A failed/cancelled checked apply may leave the page open without
			// emitting either event. _armTuningAfterApply() removes stale
			// listeners from an earlier attempt, preventing a later success
			// from running the helper twice.
			self._armTuningAfterApply();
			startOfficialApply();
		});
	}
});
