'use strict';
'require view';
'require form';
'require uci';
'require fs';
'require rpc';
'require poll';

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

function validInterface(value) {
	return /^[A-Za-z0-9_.-]{1,15}$/.test(String(value || ''));
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

function validInteger(value, minimum, maximum) {
	return /^(?:0|[1-9]\d*)$/.test(String(value || '')) && Number(value) >= minimum && Number(value) <= maximum;
}

function validPortSpec(value) {
	value = String(value || '');
	if (!value || !/^[0-9,-]+$/.test(value) || value[0] === ',' || value[value.length - 1] === ',' || value.indexOf(',,') !== -1)
		return false;
	var items = value.split(',');
	for (var i = 0; i < items.length; i++) {
		var range = items[i].split('-');
		if (range.length === 1) {
			if (!validInteger(range[0], 1, 65535)) return false;
		} else if (range.length === 2) {
			if (!validInteger(range[0], 1, 65535) || !validInteger(range[1], 1, 65535) || Number(range[0]) > Number(range[1]))
				return false;
		} else {
			return false;
		}
	}
	return true;
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

function validSipUri(value) {
	value = String(value || '');
	if (!/^sip:[A-Za-z0-9_.!~*+&=%-]+@[A-Za-z0-9.-]+(?::\d{1,5})?$/.test(value) || value.length > 255)
		return false;
	var authority = value.substring(value.indexOf('@') + 1);
	var parts = authority.split(':');
	return parts.length <= 2 && validHostname(parts[0]) && (parts.length === 1 || validInteger(parts[1], 1, 65535));
}

function validU32(value) {
	value = String(value || '');
	if (!/^(?:0[xX][0-9A-Fa-f]{1,8}|[1-9]\d{0,9})$/.test(value))
		return false;
	var number = Number(value);
	return Number.isInteger(number) && number > 0 && number <= 4294967295;
}

function markFitsMask(mark, mask) {
	if (!validU32(mark) || !validU32(mask)) return false;
	var m = Number(mark) >>> 0, x = Number(mask) >>> 0;
	return (((m & x) >>> 0) === m);
}

function normalizeList(value) {
	if (Array.isArray(value))
		return value.filter(function(item) { return item != null && String(item) !== ''; }).map(String);
	if (value == null || String(value) === '') return [];
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
	return L.resolveDefault(fs.exec_direct('/sbin/logread', [ '-e', 'fakesip' ]), '').then(function(text) {
		var lines = String(text || '').replace(/\s+$/, '').split(/\n/).filter(Boolean).slice(-200);
		var node = document.getElementById('fakesip-logview');
		if (node) {
			node.value = lines.join('\n') || _('No FakeSIP records are currently available in logd.');
			node.scrollTop = node.scrollHeight;
		}
	});
}

function refreshStatus() {
	return serviceRunning('fakesip').then(function(running) {
		var node = document.getElementById('fakesip-status');
		if (node) {
			node.textContent = running ? _('Running') : _('Stopped');
			node.style.color = running ? '#2d8a34' : '#a33';
		}
	});
}

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('fakesip'),
			uci.load('fakehttp'),
			listInterfaces(),
			detectWanDevice(),
			serviceRunning('fakesip')
		]);
	},

	render: function(data) {
		var interfaces = data[2] || [];
		var wanDevice = data[3] || '';
		var running = data[4] || false;
		var m = new form.Map('fakesip', _('FakeSIP'),
			_('Router-wide UDP DPI obfuscation using the TaoistFuchen-maintained FakeSIP 0.9.5. It adds a SIP-looking decoy to early UDP packets; it is not a VPN, proxy, or encryption layer.'));
		var s = m.section(form.NamedSection, 'main', 'fakesip', _('Service settings'));
		s.addremove = false;
		s.anonymous = true;
		s.tab('basic', _('Basic settings'));
		s.tab('advanced', _('Advanced settings'));

		var o = s.taboption('basic', form.DummyValue, '_status', _('Service status'));
		o.rawhtml = true;
		o.cfgvalue = function() {
			return '<span id="fakesip-status" style="font-weight:bold;color:' + (running ? '#2d8a34' : '#a33') + '">' +
				(running ? _('Running') : _('Stopped')) + '</span>';
		};

		o = s.taboption('basic', form.Flag, 'enabled', _('Enable FakeSIP'));
		o.rmempty = false;

		o = s.taboption('basic', form.Value, 'boot_delay',
			_('Boot auto-start delay (seconds)'),
			_('Only delays automatic service startup during router boot. Enabling, disabling, or applying settings from this page takes effect immediately.'));
		o.default = '40';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			return validInteger(value, 0, 600) ||
				_('Enter an integer from 0 to 600 without leading zeros. Use 0 for no delay.');
		};

		o = s.taboption('basic', form.MultiValue, 'interface', _('Internet-facing devices'),
			_('Select actual Linux devices, not LuCI network names. For PPPoE this is commonly pppoe-wan. FakeSIP never enables an unrestricted all-device mode.'));
		interfaces.forEach(function(name) { o.value(name); });
		if (wanDevice && interfaces.indexOf(wanDevice) !== -1) o.default = [ wanDevice ];
		o.rmempty = true;
		o.validate = function(sectionId, value) {
			var values = normalizeList(value);
			if (optionValue(this.map, 'enabled', sectionId, '0') !== '1')
				return true;
			if (values.length === 0)
				return _('Select at least one Internet-facing device.');
			for (var i = 0; i < values.length; i++)
				if (!validInterface(values[i]) || interfaces.indexOf(values[i]) === -1)
					return _('Select only devices currently present on this router.');
			return true;
		};

		o = s.taboption('basic', form.ListValue, 'direction', _('Traffic direction'));
		o.value('outbound', _('Outbound only (recommended)'));
		o.value('both', _('Inbound and outbound'));
		o.value('inbound', _('Inbound only'));
		o.default = 'outbound';
		o.rmempty = false;

		o = s.taboption('basic', form.ListValue, 'family', _('Address family'),
			_('Dual stack is the default. The chosen WAN devices must have working IPv6 connectivity for IPv6 processing. IPv6 extension headers are not parsed; only UDP directly after the base IPv6 header is processed.'));
		o.value('dual', _('IPv4 and IPv6 (recommended)'));
		o.value('ipv4', _('IPv4 only'));
		o.value('ipv6', _('IPv6 only'));
		o.default = 'dual';
		o.rmempty = false;

		o = s.taboption('basic', form.ListValue, 'port_mode', _('UDP port filter'),
			_('Excluding DNS prevents FakeSIP from modifying ordinary DNS traffic.'));
		o.value('exclude', _('All ports except the listed ports (recommended)'));
		o.value('include', _('Only the listed ports'));
		o.value('all', _('All UDP ports'));
		o.default = 'exclude';
		o.rmempty = false;

		o = s.taboption('basic', form.Value, 'ports', _('Ports and ranges'));
		o.placeholder = '53,443,6000-7000';
		o.depends('port_mode', 'exclude');
		o.depends('port_mode', 'include');
		o.rmempty = false;
		o.validate = function(sectionId, value) { return !serviceEnabled(this.map) || validPortSpec(value) || _('Use comma-separated ports or ascending ranges without spaces, for example 53,443,6000-7000.'); };

		o = s.taboption('basic', form.ListValue, 'payload_mode', _('SIP identity'));
		o.value('auto', _('Generate automatically (recommended)'));
		o.value('custom_uri', _('Use a custom SIP URI'));
		o.default = 'auto';
		o.rmempty = false;

		o = s.taboption('basic', form.Value, 'sip_uri', _('Custom SIP URI'));
		o.placeholder = 'sip:user@example.com';
		o.depends('payload_mode', 'custom_uri');
		o.rmempty = false;
		o.validate = function(sectionId, value) { return !serviceEnabled(this.map) || validSipUri(value) || _('Use sip:user@host with an optional port; whitespace, headers, and IPv6 literals are not accepted.'); };

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
			_('Must not be used by another NFQUEUE application. This package also prevents collision with enabled FakeHTTP.'));
		o.default = '8971';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			if (!validInteger(value, 1, 65535)) return _('Enter an integer from 1 to 65535.');
			if (optionValue(this.map, 'enabled', sectionId, '0') === '1' &&
				uci.get('fakehttp', 'main', 'enabled') === '1' && Number(value) === Number(uci.get('fakehttp', 'main', 'queue_num')))
				return _('This queue is already assigned to enabled FakeHTTP.');
			return true;
		};

		o = s.taboption('advanced', form.Value, 'fwmark', _('Bypass fwmark'),
			_('Expert setting. Check for overlap with mwan3, policy routing, VPN, QoS, and other packet-mark users before changing it.'));
		o.default = '0x10000';
		o.rmempty = false;
		o.validate = function(sectionId, value) { return !serviceEnabled(this.map) || validU32(value) || _('Enter a non-zero 32-bit decimal or hexadecimal value.'); };

		o = s.taboption('advanced', form.Value, 'fwmask', _('Fwmark mask'));
		o.default = '0x10000';
		o.rmempty = false;
		o.validate = function(sectionId, value) {
			if (!serviceEnabled(this.map)) return true;
			var mark = optionValue(this.map, 'fwmark', sectionId, '0x10000');
			return markFitsMask(mark, value) || _('The mark and mask must be non-zero 32-bit values, and every mark bit must be included in the mask.');
		};

		return m.render().then(function(mapNode) {
			var logNode = E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Service log')),
				E('div', { 'class': 'cbi-section-descr' }, _('Recent FakeSIP records from OpenWrt logd; no separate temporary log file is created.')),
				E('textarea', { 'id': 'fakesip-logview', 'readonly': 'readonly', 'wrap': 'off',
					'style': 'width:100%;height:260px;font-family:monospace;font-size:12px;resize:vertical;' }, [ _('Loading…') ])
			]);
			poll.add(function() { return Promise.all([ refreshLog(), refreshStatus() ]); }, 5);
			refreshLog();
			return E('div', {}, [ mapNode, logNode ]);
		});
	}
});
