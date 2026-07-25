'use strict';
'require view';
'require form';
'require uci';
'require fs';
'require rpc';
'require poll';

var PAYLOAD_DIR = '/etc/liquid-formula/fakehttp-payloads';
var UPLOAD_STAGING = '/var/run/liquid-formula-upload/pending-fakehttp';

var callServiceList = rpc.declare({
	object: 'service',
	method: 'list',
	params: [ 'name' ],
	expect: { '': {} }
});

function serviceRunning(name) {
	return L.resolveDefault(callServiceList(name), {}).then(function(res) {
		var instances = res[name] && res[name].instances || {};
		for (var key in instances)
			if (instances[key] && instances[key].running)
				return true;
		return false;
	});
}

function listInterfaces() {
	return L.resolveDefault(fs.list('/sys/class/net'), []).then(function(entries) {
		return (entries || []).map(function(entry) { return entry && entry.name; })
			.filter(function(name) { return name && name !== 'lo' && validInterface(name); })
			.sort();
	});
}

function detectWanDevice() {
	return L.resolveDefault(fs.read('/proc/net/route'), '').then(function(text) {
		var lines = String(text || '').split(/\n/);
		for (var i = 1; i < lines.length; i++) {
			var fields = lines[i].trim().split(/\s+/);
			if (fields.length > 1 && fields[1] === '00000000' && validInterface(fields[0]))
				return fields[0];
		}
		return '';
	});
}

function listPayloadFiles() {
	return L.resolveDefault(fs.list(PAYLOAD_DIR), []).then(function(entries) {
		return (entries || []).filter(function(entry) {
			return entry && entry.type === 'file' && /\.bin$/.test(entry.name) &&
				entry.size >= 1 && entry.size <= 1200;
		}).map(function(entry) {
			return { name: entry.name, path: PAYLOAD_DIR + '/' + entry.name, size: entry.size };
		}).sort(function(a, b) { return a.name.localeCompare(b.name); });
	});
}

function validInterface(value) {
	return /^[A-Za-z0-9_.-]{1,15}$/.test(String(value || ''));
}

function validHostname(value) {
	value = String(value || '');
	if (!value || value.length > 253 || value[0] === '.' || value[value.length - 1] === '.' || value.indexOf('..') !== -1)
		return false;
	var labels = value.split('.');
	for (var i = 0; i < labels.length; i++)
		if (!/^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(labels[i]))
			return false;
	return true;
}

function validInteger(value, minimum, maximum) {
	return /^(?:0|[1-9]\d*)$/.test(String(value || '')) && Number(value) >= minimum && Number(value) <= maximum;
}

function validU32(value) {
	value = String(value || '');
	if (!/^(?:0[xX][0-9A-Fa-f]{1,8}|[1-9]\d{0,9})$/.test(value))
		return false;
	var number = Number(value);
	return Number.isInteger(number) && number > 0 && number <= 4294967295;
}

function markFitsMask(mark, mask) {
	if (!validU32(mark) || !validU32(mask))
		return false;
	var m = Number(mark) >>> 0;
	var x = Number(mask) >>> 0;
	return (((m & x) >>> 0) === m);
}

function normalizeList(value) {
	if (Array.isArray(value))
		return value.filter(function(item) { return item != null && String(item) !== ''; }).map(String);
	if (value == null || String(value) === '')
		return [];
	return [ String(value) ];
}

function optionValue(map, name, sectionId, fallback) {
	var option = map.lookupOption(name, sectionId);
	return option && option[0] ? option[0].formvalue(sectionId) : fallback;
}

function serviceEnabled(map) {
	return optionValue(map, 'enabled', 'main', '0') === '1';
}

function refreshLog() {
	return L.resolveDefault(fs.exec_direct('/sbin/logread', [ '-e', 'fakehttp' ]), '').then(function(text) {
		var lines = String(text || '').replace(/\s+$/, '').split(/\n/).filter(Boolean).slice(-200);
		var node = document.getElementById('fakehttp-logview');
		if (node) {
			node.value = lines.join('\n') || _('No FakeHTTP records are currently available in logd.');
			node.scrollTop = node.scrollHeight;
		}
	});
}

function refreshStatus() {
	return serviceRunning('fakehttp').then(function(running) {
		var node = document.getElementById('fakehttp-status');
		if (node) {
			node.textContent = running ? _('Running') : _('Stopped');
			node.style.color = running ? '#2d8a34' : '#a33';
		}
	});
}

function uploadPayload(file, output, button) {
	var name = file && file.name || '';
	if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,91}\.bin$/.test(name)) {
		output.textContent = _('Use a .bin filename containing only letters, numbers, dot, underscore, or hyphen (maximum 96 characters).');
		return Promise.resolve();
	}
	if (!file.size || file.size > 1200) {
		output.textContent = _('The binary payload must be between 1 and 1200 bytes.');
		return Promise.resolve();
	}

	button.disabled = true;
	output.textContent = _('Uploading…');
	return new Promise(function(resolve) {
		var data = new FormData();
		data.append('sessionid', rpc.getSessionID());
		data.append('filename', UPLOAD_STAGING);
		data.append('filedata', file);

		var xhr = new XMLHttpRequest();
		xhr.open('POST', L.env.cgi_base + '/liquid-formula-upload?kind=fakehttp&name=' + encodeURIComponent(name), true);
		xhr.onload = function() {
			var response;
			try { response = JSON.parse(xhr.responseText || '{}'); }
			catch (e) { response = { ok: false, error: _('Invalid response from upload service.') }; }
			if (xhr.status >= 200 && xhr.status < 300 && response.ok) {
				output.textContent = _('Uploaded %s. Reloading the payload list…').format(response.name || name);
				window.setTimeout(function() { window.location.reload(); }, 700);
			} else {
				output.textContent = response.error || _('Upload failed.');
			}
			button.disabled = false;
			resolve();
		};
		xhr.onerror = function() {
			output.textContent = _('Network error while uploading.');
			button.disabled = false;
			resolve();
		};
		xhr.send(data);
	});
}

function renderUploader() {
	var input = E('input', { 'type': 'file', 'accept': '.bin,application/octet-stream' });
	var output = E('span', { 'style': 'margin-left:10px;' }, '');
	var button = E('button', {
		'type': 'button',
		'class': 'btn cbi-button cbi-button-action',
		'click': function(ev) {
			ev.preventDefault();
			if (!input.files || !input.files[0]) {
				output.textContent = _('Choose a .bin file first.');
				return;
			}
			return uploadPayload(input.files[0], output, button);
		}
	}, [ _('Upload binary payload') ]);

	return E('div', { 'class': 'cbi-section' }, [
		E('h3', {}, _('Binary payload library')),
		E('div', { 'class': 'cbi-section-descr' },
			_('Optional expert feature. Upload a captured TCP payload, then select it in an ordered Binary row below. The router enforces a 1–1200 byte limit and stores only validated .bin files. Save any pending form changes first because a successful upload reloads this page.')),
		E('div', {}, [ input, ' ', button, output ])
	]);
}

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('fakehttp'),
			uci.load('fakesip'),
			listInterfaces(),
			detectWanDevice(),
			listPayloadFiles(),
			serviceRunning('fakehttp')
		]);
	},

	render: function(data) {
		var interfaces = data[2] || [];
		var wanDevice = data[3] || '';
		var payloadFiles = data[4] || [];
		var running = data[5] || false;
		var m = new form.Map('fakehttp', _('FakeHTTP'),
			_('Router-wide TCP DPI obfuscation using FakeHTTP 0.9.18. It adds a short decoy payload to selected connections; it is not a VPN, proxy, encryption layer, or protection against manual packet analysis. Start with outbound traffic on the actual WAN device.'));
		var s = m.section(form.NamedSection, 'main', 'fakehttp', _('Service settings'));
		s.addremove = false;
		s.anonymous = true;
		s.tab('basic', _('Basic settings'));
		s.tab('advanced', _('Advanced settings'));

		var o = s.taboption('basic', form.DummyValue, '_status', _('Service status'));
		o.rawhtml = true;
		o.cfgvalue = function() {
			return '<span id="fakehttp-status" style="font-weight:bold;color:' + (running ? '#2d8a34' : '#a33') + '">' +
				(running ? _('Running') : _('Stopped')) + '</span>';
		};

		o = s.taboption('basic', form.Flag, 'enabled', _('Enable FakeHTTP'));
		o.rmempty = false;

		o = s.taboption('basic', form.Value, 'boot_delay',
			_('Boot auto-start delay (seconds)'),
			_('Only delays automatic service startup during router boot. Enabling, disabling, or applying settings from this page takes effect immediately.'));
		o.default = '60';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			return validInteger(value, 0, 600) ||
				_('Enter an integer from 0 to 600 without leading zeros. Use 0 for no delay.');
		};

		o = s.taboption('basic', form.ListValue, 'interface_mode', _('Interface scope'));
		o.value('selected', _('Selected Internet-facing devices'));
		o.value('all', _('All devices (expert mode)'));
		o.default = 'selected';
		o.rmempty = false;

		o = s.taboption('basic', form.MultiValue, 'interface', _('Internet-facing devices'),
			_('Select actual Linux devices, not LuCI network names. PPPoE is commonly pppoe-wan. Multiple devices are supported and duplicate selections are ignored.'));
		interfaces.forEach(function(name) { o.value(name); });
		if (wanDevice && interfaces.indexOf(wanDevice) !== -1)
			o.default = [ wanDevice ];
		o.depends('interface_mode', 'selected');
		o.rmempty = true;
		o.validate = function(sectionId, value) {
			var enabled = optionValue(this.map, 'enabled', sectionId, '0');
			var mode = optionValue(this.map, 'interface_mode', sectionId, 'selected');
			var values = normalizeList(value);
			if (enabled !== '1' || mode === 'all')
				return true;
			if (enabled === '1' && mode === 'selected' && values.length === 0)
				return _('Select at least one Internet-facing device.');
			for (var i = 0; i < values.length; i++)
				if (!validInterface(values[i]) || interfaces.indexOf(values[i]) === -1)
					return _('Select only devices currently present on this router.');
			return true;
		};

		o = s.taboption('basic', form.ListValue, 'direction', _('Traffic direction'));
		o.value('outbound', _('Outbound only (recommended)'));
		o.value('inbound', _('Inbound only'));
		o.value('both', _('Inbound and outbound'));
		o.default = 'outbound';
		o.rmempty = false;

		o = s.taboption('basic', form.ListValue, 'family', _('Address family'));
		o.value('dual', _('IPv4 and IPv6'));
		o.value('ipv4', _('IPv4 only'));
		o.value('ipv6', _('IPv6 only'));
		o.default = 'dual';
		o.rmempty = false;

		o = s.taboption('advanced', form.Value, 'repeat', _('Generated packet copies'));
		o.default = '2';
		o.rmempty = false;
		o.validate = function(sectionId, value) { return !serviceEnabled(this.map) || validInteger(value, 1, 10) || _('Enter an integer from 1 to 10.'); };

		o = s.taboption('advanced', form.Value, 'ttl', _('Generated packet TTL'));
		o.default = '3';
		o.rmempty = false;
		o.validate = function(sectionId, value) { return !serviceEnabled(this.map) || validInteger(value, 1, 255) || _('Enter an integer from 1 to 255.'); };

		o = s.taboption('advanced', form.Flag, 'hop_estimation', _('Estimate route hop count'));
		o.default = '1';
		o.rmempty = false;

		o = s.taboption('advanced', form.Value, 'dynamic_ttl_pct', _('Dynamic TTL increase (%)'),
			_('0 disables dynamic adjustment. Values 1–99 require hop estimation.'));
		o.default = '0';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			if (!validInteger(value, 0, 99)) return _('Enter an integer from 0 to 99.');
			if (Number(value) > 0 && optionValue(this.map, 'hop_estimation', sectionId, '1') !== '1')
				return _('Dynamic TTL requires hop estimation.');
			return true;
		};

		o = s.taboption('advanced', form.Flag, 'log_connections', _('Detailed connection logging'),
			_('Writes connection details to OpenWrt logd. Leave disabled unless troubleshooting.'));
		o.default = '0';
		o.rmempty = false;

		o = s.taboption('advanced', form.Value, 'queue_num', _('NFQUEUE number'),
			_('Must not be used by another NFQUEUE application. This package also prevents collision with enabled FakeSIP.'));
		o.default = '8970';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			if (!validInteger(value, 1, 65535)) return _('Enter an integer from 1 to 65535.');
			if (optionValue(this.map, 'enabled', sectionId, '0') === '1' &&
				uci.get('fakesip', 'main', 'enabled') === '1' && Number(value) === Number(uci.get('fakesip', 'main', 'queue_num')))
				return _('This queue is already assigned to enabled FakeSIP.');
			return true;
		};

		o = s.taboption('advanced', form.Value, 'fwmark', _('Bypass fwmark'),
			_('Expert setting. Check for overlap with mwan3, policy routing, VPN, QoS, and other packet-mark users before changing it.'));
		o.default = '0x8000';
		o.rmempty = false;
		o.validate = function(sectionId, value) { return !serviceEnabled(this.map) || validU32(value) || _('Enter a non-zero 32-bit decimal or hexadecimal value.'); };

		o = s.taboption('advanced', form.Value, 'fwmask', _('Fwmark mask'));
		o.default = '0x8000';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			var mark = optionValue(this.map, 'fwmark', sectionId, '0x8000');
			return markFitsMask(mark, value) || _('The mark and mask must be non-zero 32-bit values, and every mark bit must be included in the mask.');
		};

		var p = m.section(form.GridSection, 'payload', _('Ordered decoy payloads'),
			_('Enabled rows are passed to FakeHTTP in this exact order and rotated globally. Duplicate rows are intentionally preserved, so repeating a row gives it more weight.'));
		p.anonymous = true;
		p.addremove = true;
		p.sortable = true;

		o = p.option(form.Flag, 'enabled', _('Enabled'));
		o.default = '1';
		o.rmempty = false;

		o = p.option(form.ListValue, 'type', _('Type'));
		o.value('http', _('HTTP Host'));
		o.value('https', _('HTTPS SNI'));
		o.value('binary', _('Binary payload'));
		o.default = 'http';
		o.rmempty = false;

		o = p.option(form.Value, 'host', _('Hostname'));
		o.depends('type', 'http');
		o.depends('type', 'https');
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			if (optionValue(this.map, 'enabled', sectionId, '1') !== '1') return true;
			return validHostname(value) || _('Enter a bare hostname with labels no longer than 63 characters.');
		};

		o = p.option(form.ListValue, 'file', _('Uploaded file'));
		o.depends('type', 'binary');
		o.value('', _('Upload a .bin file above, then reload this list'));
		payloadFiles.forEach(function(file) { o.value(file.path, '%s (%d bytes)'.format(file.name, file.size)); });
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			if (optionValue(this.map, 'enabled', sectionId, '1') !== '1') return true;
			for (var i = 0; i < payloadFiles.length; i++)
				if (payloadFiles[i].path === value) return true;
			return _('Select a validated file from the payload library.');
		};

		return m.render().then(function(mapNode) {
			var logNode = E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Service log')),
				E('div', { 'class': 'cbi-section-descr' }, _('Recent FakeHTTP records from OpenWrt logd; no separate temporary log file is created.')),
				E('textarea', { 'id': 'fakehttp-logview', 'readonly': 'readonly', 'wrap': 'off',
					'style': 'width:100%;height:260px;font-family:monospace;font-size:12px;resize:vertical;' }, [ _('Loading…') ])
			]);
			poll.add(function() { return Promise.all([ refreshLog(), refreshStatus() ]); }, 5);
			refreshLog();
			return E('div', {}, [ renderUploader(), mapNode, logNode ]);
		});
	}
});
