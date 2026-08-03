'use strict';
//
// 真正把每个 LuCI 视图跑一遍 load() + render()。
//
// 起因: customlogo.js 曾经引用过三个从未定义的变量, 页面一打开就
// ReferenceError。当时 node --check 是过的(语法合法), 断言也是过的
// (文本都在) —— 只有真的执行渲染才会暴露。所以这里搭一层最小 LuCI 桩,
// 把视图当模块加载并调用, 让运行时错误在 CI 里就炸出来。

const path = require('path');
const fs = require('fs');

const ROOT = path.resolve(__dirname, '../..');
const VIEW_DIR = path.join(ROOT,
	'openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula');

let failures = 0;
let checks = 0;
let renderedMaps = [];

function ok(description) {
	checks++;
	console.log(`ok ${checks} - ${description}`);
}

function fail(description, error) {
	checks++;
	failures++;
	console.log(`not ok ${checks} - ${description}`);
	if (error)
		console.error('    ' + String(error.stack || error).split('\n').join('\n    '));
}

// ---------------------------------------------------------------- 桩 ----

function makeTextNode(value) {
	return {
		nodeType: 3,
		data: String(value),
		parentNode: null,
		get textContent() { return this.data; },
		set textContent(text) { this.data = String(text); }
	};
}

function isNode(value) {
	return value != null && typeof value === 'object' &&
		(value.nodeType === 1 || value.nodeType === 3);
}

function makeElement(tag, attrs, children) {
	let attributes = attrs;
	let kids = children;

	// LuCI's E() overload treats its second argument as children unless it is
	// an attribute object. Most importantly, a scalar string child is assigned
	// through innerHTML while strings inside an array are appended as text
	// nodes. Keep that distinction here so injection regressions are testable.
	if (children === undefined &&
	    (Array.isArray(attrs) || isNode(attrs) || typeof attrs !== 'object' || attrs === null)) {
		kids = attrs;
		attributes = {};
	}
	attributes = attributes || {};

	const node = {
		nodeType: 1,
		tagName: String(tag || 'div').toUpperCase(),
		attributes: Object.assign({}, attributes),
		childNodes: [],
		parentNode: null,
		innerHTML: '',
		style: {},
		value: attributes.value || '',
		checked: attributes.checked != null,
		disabled: attributes.disabled != null,
		readOnly: attributes.readonly != null,
		appendChild(child) {
			if (!isNode(child))
				throw new TypeError('appendChild expects a node');
			if (child.parentNode)
				child.parentNode.removeChild(child);
			child.parentNode = this;
			this.childNodes.push(child);
			return child;
		},
		removeChild(child) {
			const at = this.childNodes.indexOf(child);
			if (at < 0)
				throw new Error('node is not a child');
			this.childNodes.splice(at, 1);
			child.parentNode = null;
			return child;
		},
		_listeners: Object.create(null),
		addEventListener(type, listener) {
			(this._listeners[type] || (this._listeners[type] = [])).push(listener);
		},
		dispatchEvent(event) {
			const listeners = this._listeners[event.type] || [];
			event.currentTarget = this;
			listeners.slice().forEach((listener) => listener.call(this, event));
			return true;
		},
		setAttribute(name, value) {
			this.attributes[name] = value;
			if (name === 'value')
				this.value = value;
		},
		focus() {},
		select() {},
		querySelector() { return null; },
		querySelectorAll() { return []; },
		get firstChild() { return this.childNodes[0] || null; },
		get lastChild() { return this.childNodes[this.childNodes.length - 1] || null; },
		get isConnected() {
			let current = this;
			while (current) {
				if (global.document && current === global.document.body)
					return true;
				current = current.parentNode;
			}
			return false;
		},
		get textContent() {
			return this.childNodes.map((child) => child.textContent || '').join('');
		},
		set textContent(text) {
			this.innerHTML = '';
			this.childNodes.slice().forEach((child) => this.removeChild(child));
			if (text != null && String(text) !== '')
				this.appendChild(makeTextNode(text));
		}
	};

	node.classList = {
		add(...names) {
			const classes = new Set(String(node.attributes.class || '').split(/\s+/).filter(Boolean));
			names.forEach((name) => classes.add(name));
			node.attributes.class = Array.from(classes).join(' ');
		},
		remove(...names) {
			const remove = new Set(names);
			node.attributes.class = String(node.attributes.class || '').split(/\s+/)
				.filter((name) => name && !remove.has(name)).join(' ');
		},
		contains(name) {
			return String(node.attributes.class || '').split(/\s+/).includes(name);
		}
	};

	function appendArrayItem(child) {
		if (child == null)
			return;
		node.appendChild(isNode(child) ? child : makeTextNode(child));
	}

	if (Array.isArray(kids))
		kids.forEach(appendArrayItem);
	else if (isNode(kids))
		node.appendChild(kids);
	else if (kids != null)
		node.innerHTML = String(kids);

	if (global.document && typeof global.document._register === 'function')
		global.document._register(node);
	return node;
}

class StubOption {
	constructor(type, name, title, description) {
		this.type = type;
		this.option = name;
		this.title = title;
		this.description = description;
		this.keylist = [];
		this.vallist = [];
		this.deps = [];
		this.validationCalls = [];
		this.validState = true;
	}
	value(key, label) { this.keylist.push(key); this.vallist.push(label); }
	depends(a, b) { this.deps.push([a, b]); }
	triggerValidation(sectionId) {
		this.validationCalls.push(sectionId);
		this.validState = this.validate ? this.validate(sectionId,
			this._renderedElement ? this._renderedElement.value : '') : true;
	}
	parse(sectionId) {
		const value = this._renderedElement ? this._renderedElement.value : '';
		const result = this.validate ? this.validate(sectionId, value) : true;
		return result === true ? Promise.resolve(value) : Promise.reject(new Error(String(result)));
	}
}

class StubSection {
	constructor(map, type, sectionType, title, description) {
		this.map = map;
		this.sectiontype = sectionType;
		this.title = title;
		this.description = description;
		this.options = [];
	}
	option(type, name, title, description, extra) {
		const o = new StubOption(type, name, title, description, extra);
		o.map = this.map;
		o.section_id = 'main';
		this.options.push(o);
		return o;
	}
	taboption(tab, type, name, title, description) {
		return this.option(type, name, title, description);
	}
	tab() {}
}

class StubMap {
	constructor(config, title, description) {
		this.config = config;
		this.title = title;
		this.description = description;
		this.sections = [];
		renderedMaps.push(this);
	}
	section(kind, a, b, c, d) {
		const s = new StubSection(this, kind, b, c, d);
		this.sections.push(s);
		return s;
	}
	lookupOption(name) {
		return this.sections.reduce((matches, section) =>
			matches.concat(section.options.filter((option) => option.option === name)), []);
	}
	render() {
		this.renderCalls = (this.renderCalls || 0) + 1;
		const root = makeElement('div');
		const runtime = this._runtime;
		this.sections.forEach((section) => section.options.forEach((option) => {
			const type = option.type && option.type._formTypeName;
			if (type !== 'ListValue' && type !== 'Value')
				return;
			const recordChange = function(value) {
				runtime.formValueChanges.push({ option: option.option, value });
				uci._data[`${this.config}.${option.section_id}.${option.option}`] = value;
				const changes = uci._changes[this.config] || (uci._changes[this.config] = []);
				changes.push([ 'set', option.section_id, option.option, value ]);
			}.bind(this);
			if (type === 'Value') {
				const input = makeElement('input', {
					'id': `cbid.${this.config}.${option.section_id}.${option.option}`,
					'value': uci.get(this.config, option.section_id, option.option) || option.default || ''
				});
				input.addEventListener('input', function() { recordChange(input.value); });
				runtime.formInputs[option.option] = input;
				root.appendChild(input);
				return;
			}
			const select = makeElement('select', {
				'id': `cbid.${this.config}.${option.section_id}.${option.option}`
			});
			select.options = [];
			select.replaceOptions = function(entries) {
				while (select.firstChild)
					select.removeChild(select.firstChild);
				select.options = entries.map((entry) => {
					const choice = makeElement('option', {
						'value': entry.value,
						'disabled': entry.disabled ? 'disabled' : null,
						'hidden': entry.hidden ? 'hidden' : null
					}, [ entry.label ]);
					choice.value = entry.value;
					choice.disabled = !!entry.disabled;
					choice.hidden = !!entry.hidden;
					select.appendChild(choice);
					return choice;
				});
			};
			select.replaceOptions(option.keylist.map((value, index) => ({
				value, label: option.vallist[index]
			})));
			select.value = uci.get(this.config, option.section_id, option.option) || option.keylist[0] || '';
			select.addEventListener('change', function() {
				runtime.listValueChanges.push({ option: option.option, value: select.value });
				recordChange(select.value);
			});
			option._renderedElement = select;
			runtime.listValues[option.option] = select;
			root.appendChild(select);
		}));
		return Promise.resolve(root);
	}
	parse() {
		const sectionId = 'main';
		this.sections.forEach((section) => section.options.forEach((option) => {
			if (!option.deps.length)
				return;
			const active = option.deps.some((dep) =>
				String(uci.get(this.config, sectionId, dep[0]) || '') === String(dep[1]));
			if (!active && !option.retain)
				uci.unset(this.config, sectionId, option.option);
		}));
		return Promise.resolve();
	}
	save() { return Promise.resolve(); }
}

function installGlobals(rpcResponses) {
	renderedMaps = [];
	const createdNodes = [];
	const runtime = {
		applyCalls: [],
		notifications: [],
		statuses: [],
		listeners: Object.create(null),
		listValues: Object.create(null),
		listValueChanges: [],
		formInputs: Object.create(null),
		formValueChanges: [],
		fsReads: [],
		fsLists: []
	};
	global.document = {
		body: null,
		_created: createdNodes,
		_register(node) { createdNodes.push(node); },
		createTextNode: makeTextNode,
		createElement(tag) { return makeElement(tag); },
		execCommand() { return true; },
		addEventListener(type, listener) {
			(runtime.listeners[type] || (runtime.listeners[type] = new Set())).add(listener);
		},
		removeEventListener(type, listener) {
			const listeners = runtime.listeners[type];
			if (listeners)
				listeners.delete(listener);
		},
		dispatchEvent(event) {
			const listeners = runtime.listeners[event.type];
			if (listeners)
				Array.from(listeners).forEach((listener) => listener.call(this, event));
			return true;
		},
		getElementById(id) {
			function find(node) {
				if (!node)
					return null;
				if (node.nodeType === 1 && node.attributes.id === id)
					return node;
				for (const child of node.childNodes || []) {
					const match = find(child);
					if (match)
						return match;
				}
				return null;
			}
			return find(this.body);
		}
	};
	document.body = makeElement('body');
	global.window = {
		location: { href: 'http://router/cgi-bin/luci/admin/system/liquid-formula/customlogo' },
		setTimeout,
		clearTimeout
	};
	global.CustomEvent = class {
		constructor(type, options) {
			this.type = type;
			this.detail = options && options.detail;
		}
	};
	Object.defineProperty(global, 'navigator', {
		value: {},
		writable: true,
		configurable: true
	});
	// LuCI 在 String.prototype 上挂了 format(), 视图里大量使用。
	if (!String.prototype.format) {
		Object.defineProperty(String.prototype, 'format', {
			value: function() {
				const args = arguments;
				let index = 0;
				return this.replace(/%[sdj%]/g, (token) =>
					token === '%%' ? '%' : String(args[index++]));
			},
			writable: true, configurable: true
		});
	}

	global.L = {
		env: {
			cgi_base: '/cgi-bin',
			requestpath: [],
			sessionid: 'x',
			apply_display: 1,
			rpctimeout: 20
		},
		bind(fn, self, ...bound) { return fn.bind(self, ...bound); },
		resolveDefault(promise, fallback) {
			return Promise.resolve(promise).catch(() => fallback);
		},
		hasSystemFeature() { return true; },
		error(e) { throw e; },
		raise(e) { throw new Error(e); },
		isObject(v) { return v !== null && typeof v === 'object'; },
		toArray(v) { return Array.isArray(v) ? v : (v == null || v === '' ? [] : [v]); },
		Class: { extend(spec) { return spec; } }
	};
	global.E = makeElement;
	global._ = (s) => String(s);

	global.view = {
		extend(spec) {
			const base = {
				load() { return Promise.resolve(); },
				render() { return Promise.resolve(makeElement('div')); },
				handleSave() { return Promise.resolve(); },
				handleSaveApply() { return Promise.resolve(); },
				super(method, args) {
					return Promise.resolve();
				}
			};
			return Object.assign(base, spec);
		}
	};

	// LuCI 的 form.* 是可 extend 的类, 视图会用 form.Value.extend({...})
	// 定义自己的控件, 所以桩必须提供同样的形状。
	function formClass(name) {
		class Widget extends StubOption {
			constructor(...args) { super(name, ...args); }
		}
		Widget._formTypeName = name;
		Widget.extensions = [];
		Widget.extend = function(spec) {
			class Derived extends Widget {}
			Object.assign(Derived.prototype, spec || {});
			Derived.extend = Widget.extend;
			Widget.extensions.push(Derived);
			return Derived;
		};
		return Widget;
	}

	global.form = {
		Map: StubMap,
		JSONMap: StubMap,
		NamedSection: formClass('NamedSection'),
		TypedSection: formClass('TypedSection'),
		SectionValue: formClass('SectionValue'),
		TableSection: formClass('TableSection'),
		GridSection: formClass('GridSection'),
		Flag: formClass('Flag'),
		Value: formClass('Value'),
		ListValue: formClass('ListValue'),
		DummyValue: formClass('DummyValue'),
		Button: formClass('Button'),
		TextValue: formClass('TextValue'),
		HiddenValue: formClass('HiddenValue'),
		MultiValue: formClass('MultiValue'),
		DynamicList: formClass('DynamicList'),
		FileUpload: formClass('FileUpload')
	};

	global.uci = {
		_data: {},
		_changes: {},
		load() { return Promise.resolve(); },
		get(config, section, option) {
			if (option === undefined) return undefined;
			return (this._data[`${config}.${section}.${option}`]);
		},
		set(config, section, option, value) {
			this._data[`${config}.${section}.${option}`] = value;
		},
		unset(config, section, option) {
			delete this._data[`${config}.${section}.${option}`];
		},
		sections() { return []; },
		save() { return Promise.resolve(); },
		changes() { return Promise.resolve(this._changes); }
	};

	global.rpc = {
		declare(spec) {
			return function(...args) {
				const canned = rpcResponses[spec.method];
				return Promise.resolve().then(() =>
					typeof canned === 'function'
						? canned.apply(null, args)
						: (canned === undefined ? {} : canned));
			};
		}
	};
	const OriginalMap = global.form.Map;
	global.form.Map = class RuntimeMap extends OriginalMap {
		constructor(...args) {
			super(...args);
			this._runtime = runtime;
		}
	};
	global.form.JSONMap = global.form.Map;

	global.ui = {
		_notifications: runtime.notifications,
		addNotification(title, content, level) {
			runtime.notifications.push({ title, content, level });
		},
		showModal() {},
		hideModal() {},
		createHandlerFn(ctx, fn) { return fn; },
		changes: {
			apply(checked) {
				runtime.applyCalls.push(checked);
				return Promise.resolve();
			},
			displayStatus(level, content) {
				runtime.statuses.push({ level, content });
			}
		},
		Combobox: class {},
		Textfield: class {
			constructor(value, options) {
				this.value = value;
				this.options = options || {};
			}
			render() {
				const input = makeElement('input', {
					value: this.value,
					disabled: this.options.disabled ? '' : null,
					readonly: this.options.readonly ? '' : null
				});
				const frame = makeElement('div', {}, [ input ]);
				frame.querySelector = (selector) => selector === 'input' ? input : null;
				this.input = input;
				return frame;
			}
			setValue(value) {
				this.value = value;
				if (this.input)
					this.input.value = value;
			}
		}
	};

	global.request = { get() { return Promise.resolve({ status: 200, text: () => '' }); },
	                   post() { return Promise.resolve({ status: 200 }); } };
	global.FileReader = class {
		readAsText(file) {
			this.onload({ target: { result: file.content } });
		}
	};
	global.confirm = function() { return true; };
	global.fs = {
		read(file) {
			runtime.fsReads.push(file);
			return Promise.resolve('');
		},
		read_direct() { return Promise.resolve(''); },
		write() { return Promise.resolve(); },
		stat() { return Promise.resolve({ type: 'file', size: 0 }); },
		list(directory) {
			runtime.fsLists.push(directory);
			return Promise.resolve([]);
		},
		exec() { return Promise.resolve({ code: 0, stdout: '', stderr: '' }); },
		exec_direct() { return Promise.resolve(''); },
		remove() { return Promise.resolve(); },
		trash() { return Promise.resolve(); }
	};
	global.poll = { add() {}, remove() {} };
	global.dom = { content() {}, parse() { return makeElement('div'); } };
	return runtime;
}

function findNodes(root, predicate) {
	const matches = [];
	function visit(node) {
		if (!node)
			return;
		if (predicate(node))
			matches.push(node);
		for (const child of node.childNodes || [])
			visit(child);
	}
	visit(root);
	return matches;
}

function hasClasses(node, names) {
	return names.every((name) => node.classList && node.classList.contains(name));
}

function isLiteralText(node, value) {
	return node && node.innerHTML === '' &&
		findNodes(node, (child) => child.nodeType === 3 && child.data.includes(value)).length > 0;
}

async function exerciseOverviewContracts() {
	installGlobals({ status: {}, list_templates: { templates: [] } });
	uci._data['fakehttp.main.fwmark'] = '0x2200';
	uci._data['fakehttp.main.fwmask'] = '0x2f00';
	uci._data['fakesip.main.fwmark'] = '131072';
	uci._data['fakesip.main.fwmask'] = '196608';
	uci._data['momo.proxy.bypass_fwmark'] = [ '0x40/0x40' ];

	let mod;
	try {
		mod = new Function(fs.readFileSync(path.join(VIEW_DIR, 'overview.js'), 'utf8'))();
		const data = await mod.load();
		await mod.render(data);
	} catch (error) {
		fail('overview contract fixture renders', error);
		return;
	}

	const map = renderedMaps.find((candidate) => candidate.config === 'liquid_formula');
	const options = map ? map.sections.flatMap((section) => section.options) : [];
	const option = (name) => options.find((candidate) => candidate.option === name);
	const output = option('output_config');
	const templateBase = option('template_base_url');
	const subscription = option('subscription_url');
	const userAgent = option('user_agent');
	const fakehttpBypass = option('momo_bypass_fakehttp');
	const fakesipBypass = option('momo_bypass_fakesip');
	const check = (condition, description) => condition ? ok(description) : fail(description);

	const invalidSubscriptionURLs = [
		'ftp://provider.example/sub', 'http://?query', 'https:///path',
		'http://:80/sub', 'http://user@:80/sub', 'https://exa%6Dple.com/sub',
		'https://user[bad]@provider.example/sub', 'https://provider|invalid.example/sub',
		'https://[x:y]/sub', 'https://[::::]/sub', 'https://[2001::db8::1]/sub',
		'https://provider.example/raw space', 'https://provider.example/sub ',
		'https://provider.example/%zz', 'https://provider.example/sub\u007f'
	];
	for (const invalidURL of invalidSubscriptionURLs)
		check(subscription && subscription.validate('main', invalidURL) !== true,
			`overview rejects invalid scalar subscription URL ${invalidURL}`);

	const validSubscriptionURLs = [
		'HTTPS://provider.example/sub', 'https://user:pass@provider.example/sub',
		'https://[2001:db8::1]/sub', 'https://[::ffff:192.0.2.1]/sub',
		'https://[fe80::1%25eth0]/sub', 'https://provider.example:0/sub',
		'https://provider.example:65536/sub', 'https://provider.example/sub#fragment',
		'https://provider.example/sub?opaque=%zz', 'https://provider.example/escaped%20space'
	];
	for (const validURL of validSubscriptionURLs)
		check(subscription && subscription.validate('main', validURL) === true,
			`overview accepts backend-valid scalar subscription URL ${validURL}`);
	uci._data['liquid_formula.main.enabled'] = '0';
	check(subscription && subscription.validate('main', '') === true,
		'overview allows an empty scalar subscription URL while disabled');
	uci._data['liquid_formula.main.enabled'] = '1';
	check(subscription && subscription.validate('main', '') !== true,
		'overview requires a scalar subscription URL while enabled');

	check(userAgent && userAgent.type === form.Value,
		'user-agent remains a customisable Value control');
	check(userAgent && userAgent.default === 'v2rayN/7.24.4',
		'user-agent fresh default is current v2rayN');
	check(userAgent && JSON.stringify(userAgent.keylist) === JSON.stringify([
		'v2rayN/7.24.4', 'v2rayNG/2.2.6', 'sing-box 1.13.15',
		'SFI/1.13.15 (sing-box 1.13.15)', 'SFA/1.13.15 (sing-box 1.13.15)',
		'SFM/1.13.15 (sing-box 1.13.15)', 'Karing/1.2.23.2605'
	]), 'user-agent exposes only the seven maintained presets in order');
	check(userAgent && userAgent.validate('main', 'ProviderCustom/99.1') === true,
		'user-agent accepts a provider-specific custom value');

	check(output && output.validate('main', '/etc/momo/profiles/router.json') === true,
		'overview accepts an allowed JSON output path');
	check(output && output.validate('main', '/tmp/profile.json') !== true,
		'overview rejects an output path outside the backend allowlist');
	check(output && output.validate('main', '/etc/momo/profiles/router.txt') !== true,
		'overview rejects a non-JSON output path');
	check(templateBase && templateBase.validate('main', 'http://localhost:65535/templates') === true,
		'overview accepts the largest backend-valid loopback port');
	check(templateBase && templateBase.validate('main', 'http://localhost:0/templates') !== true,
		'overview rejects loopback port zero');
	check(templateBase && templateBase.validate('main', 'http://localhost:65536/templates') !== true,
		'overview rejects an oversized loopback port');
	check(templateBase && templateBase.validate('main', 'http://localhost:080/templates') !== true,
		'overview rejects a non-canonical loopback port');
	check(mod.actionWaitSeconds('refresh') === 150,
		'refresh wait matches one default request budget plus RPC overhead');
	check(mod.actionWaitSeconds('check') === 540 && mod.actionWaitSeconds('update') === 540,
		'check and update wait for startup, refresh, and fetch budgets');
	mod._enabledTemplateCount = 10;
	uci._data['liquid_formula.main.subscription_timeout'] = '600';
	check(mod.actionWaitSeconds('update') === 11160,
		'frontend wait reaches the same capped maximum as the RPC watchdog');
	uci._data['liquid_formula.main.subscription_timeout'] = 'invalid';
	check(mod.actionWaitSeconds('check') === 11160,
		'invalid timeout data fails safe to the RPC fallback budget');

	check(fakehttpBypass && fakehttpBypass.title.includes('0x2200/0x2f00'),
		'FakeHTTP bypass label uses its live UCI mark and mask');
	check(fakesipBypass && fakesipBypass.title.includes('131072/196608'),
		'FakeSIP bypass label uses its live UCI mark and mask');
	if (fakehttpBypass)
		fakehttpBypass.write.call(fakehttpBypass, 'main', '1');
	if (fakesipBypass)
		fakesipBypass.write.call(fakesipBypass, 'main', '1');
	const bypass = uci._data['momo.proxy.bypass_fwmark'] || [];
	check(bypass.includes('0x2200/0x2f00'),
		'FakeHTTP custom mark and mask are written to momo');
	check(bypass.includes('131072/196608'),
		'FakeSIP custom mark and mask are written to momo');
}

function listChoices(select) {
	return (select.options || []).map((choice) => ({
		value: choice.value,
		label: choice.textContent,
		disabled: choice.disabled,
		hidden: choice.hidden
	}));
}

function templateRowIds() {
	const tbody = document.getElementById('sbsc_tpl_tbody');
	return tbody ? tbody.childNodes.map((row) => row.firstChild && row.firstChild.textContent) : [];
}

function mapRenderState() {
	return JSON.stringify({
		maps: renderedMaps.length,
		renders: renderedMaps.reduce((total, map) => total + (map.renderCalls || 0), 0)
	});
}

function deferredResponse() {
	let resolve;
	const promise = new Promise((done) => { resolve = done; });
	return { promise, resolve };
}

async function exerciseTemplateRefreshContracts() {
	const initial = [
		{ id: 'alpha', enabled: true, name: 'Alpha', file: 'alpha.json', size: 1, mtime: '1' },
		{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' },
		{ id: 'retired', enabled: false, name: 'Retired', file: 'retired.json', size: 3, mtime: '3' }
	];
	const afterCreate = [
		{ id: 'alpha', enabled: true, name: 'Alpha', file: 'alpha.json', size: 1, mtime: '1' },
		{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' },
		{ id: 'gamma', enabled: true, name: 'Gamma', file: 'gamma.json', size: 4, mtime: '4' },
		{ id: 'retired', enabled: false, name: 'Retired', file: 'retired.json', size: 3, mtime: '3' }
	];
	const afterEdit = [
		{ id: 'alpha', enabled: true, name: 'Alpha renamed', file: 'alpha.json', size: 1, mtime: '5' },
		{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' },
		{ id: 'gamma', enabled: true, name: 'Gamma', file: 'gamma.json', size: 4, mtime: '4' }
	];
	const afterDelete = [
		{ id: 'alpha', enabled: true, name: 'Alpha renamed', file: 'alpha.json', size: 1, mtime: '5' },
		{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' }
	];
	const afterDisable = [
		{ id: 'alpha', enabled: true, name: 'Alpha renamed', file: 'alpha.json', size: 1, mtime: '5' },
		{ id: 'beta', enabled: false, name: 'Beta', file: 'beta.json', size: 2, mtime: '6' }
	];
	const staleOlder = deferredResponse();
	const staleNewer = deferredResponse();
	const listResponses = [
		{ templates: initial },
		{ templates: afterCreate },
		{ templates: afterEdit },
		{ templates: afterDelete },
		{ templates: afterDisable },
		new Error('template list transport failed'),
		staleOlder.promise,
		staleNewer.promise
	];
	let listCalls = 0;
	const writeCalls = [];
	const deleteCalls = [];
	const runtime = installGlobals({
		status: {},
		list_templates() {
			listCalls++;
			const response = listResponses.shift();
			return response instanceof Error ? Promise.reject(response) : response;
		},
		read_template(id, file) {
			return { ok: true, id, file, content: '{"outbounds":[]}' };
		},
		write_template(id, name, file, noNode, enabled, content) {
			writeCalls.push({ id, name, file, noNode, enabled, content });
			return { ok: true, phase: 'complete', id, file };
		},
		delete_template(id) {
			deleteCalls.push(id);
			return { ok: true, phase: 'complete', id };
		}
	});
	const check = (condition, description) => condition ? ok(description) : fail(description);
	uci._data['liquid_formula.main.default_template'] = 'alpha';
	uci._data['liquid_formula.main.port'] = '9716';

	let mod;
	let page;
	try {
		mod = new Function(fs.readFileSync(path.join(VIEW_DIR, 'overview.js'), 'utf8'))();
		page = await mod.render(await mod.load());
		document.body.appendChild(page);
	} catch (error) {
		fail('template refresh fixture renders a live overview form', error);
		return;
	}

	const map = renderedMaps.find((candidate) => candidate.config === 'liquid_formula');
	const defaultTemplate = map && map.sections.flatMap((section) => section.options)
		.find((option) => option.option === 'default_template');
	const select = runtime.listValues.default_template;
	const port = runtime.formInputs.port;
	check(select && select.value === 'alpha' &&
		listChoices(select).map((choice) => choice.value).join(',') === 'alpha,beta',
		'the rendered default-template ListValue excludes disabled choices initially');

	// Exercise a genuine dirty Overview control. Every template operation below
	// must update only the table and ListValue, without parsing or replacing it.
	port.value = '9816';
	port.dispatchEvent({ type: 'input' });
	const dirtyBeforeCreate = JSON.stringify(uci._changes);
	const inputEventsBeforeCreate = runtime.formValueChanges.length;
	const mapsBeforeCreate = mapRenderState();

	mod.uploadTemplate({ target: { files: [ { name: 'gamma.json', content: '{}' } ] } });
	await mod.saveTemplate();
	check(writeCalls.length === 1 && writeCalls[0].id === 'gamma' && writeCalls[0].enabled === true &&
		listCalls === 2 && templateRowIds().join(',') === 'alpha,beta,gamma,retired' &&
		listChoices(select).map((choice) => choice.value).join(',') === 'alpha,beta,gamma',
		'creating an enabled template immediately refreshes the table and default dropdown');
	check(select.value === 'alpha' && port.value === '9816' &&
		uci._data['liquid_formula.main.port'] === '9816' && runtime.listValueChanges.length === 0 &&
		JSON.stringify(uci._changes) === dirtyBeforeCreate &&
		runtime.formValueChanges.length === inputEventsBeforeCreate && mapRenderState() === mapsBeforeCreate,
		'creating a template preserves the selected default and unrelated dirty input without a full render');

	await mod.loadTemplate(initial[0]);
	document.getElementById('sbsc_tpl_name').value = 'Alpha renamed';
	document.getElementById('sbsc_tpl_content').value = '{"edited":true}';
	const dirtyBeforeEdit = JSON.stringify(uci._changes);
	const listEventsBeforeEdit = runtime.listValueChanges.length;
	const inputEventsBeforeEdit = runtime.formValueChanges.length;
	const mapsBeforeEdit = mapRenderState();
	await mod.saveTemplate();
	check(writeCalls.length === 2 && writeCalls[1].id === 'alpha' &&
		writeCalls[1].name === 'Alpha renamed' &&
		listChoices(select).some((choice) => choice.value === 'alpha' && choice.label === 'Alpha renamed') &&
		templateRowIds().join(',') === 'alpha,beta,gamma',
		'editing a template immediately refreshes its table row and default-dropdown label');
	check(port.value === '9816' && JSON.stringify(uci._changes) === dirtyBeforeEdit &&
		runtime.listValueChanges.length === listEventsBeforeEdit &&
		runtime.formValueChanges.length === inputEventsBeforeEdit && mapRenderState() === mapsBeforeEdit,
		'editing a template preserves dirty form state without synthetic events or a full render');

	const dirtyBeforeDelete = JSON.stringify(uci._changes);
	const listEventsBeforeDelete = runtime.listValueChanges.length;
	const inputEventsBeforeDelete = runtime.formValueChanges.length;
	const mapsBeforeDelete = mapRenderState();
	await mod.deleteTemplate('gamma');
	check(deleteCalls.join(',') === 'gamma' &&
		!listChoices(select).some((choice) => choice.value === 'gamma') &&
		!templateRowIds().includes('gamma'),
		'deleting a template immediately removes it from the table and default dropdown');
	check(port.value === '9816' && JSON.stringify(uci._changes) === dirtyBeforeDelete &&
		runtime.listValueChanges.length === listEventsBeforeDelete &&
		runtime.formValueChanges.length === inputEventsBeforeDelete && mapRenderState() === mapsBeforeDelete,
		'deleting a template preserves dirty form state without synthetic events or a replacement map');

	select.value = 'beta';
	select.dispatchEvent({ type: 'change' });
	await mod.loadTemplate(afterDelete[1]);
	document.getElementById('sbsc_tpl_enabled').checked = false;
	const dirtyUnavailableSelection = JSON.stringify(uci._changes);
	const unavailableInputEvents = runtime.formValueChanges.length;
	const unavailableListEvents = runtime.listValueChanges.length;
	const unavailableMaps = mapRenderState();
	const unavailableValidationCalls = defaultTemplate.validationCalls.length;
	await mod.saveTemplate();
	const betaChoice = listChoices(select).find((choice) => choice.value === 'beta');
	const hasDefaultValidation = defaultTemplate && typeof defaultTemplate.validate === 'function';
	const unavailableParseRejected = await defaultTemplate.parse('main').then(() => false, () => true);
	check(writeCalls.length === 3 && writeCalls[2].id === 'beta' && writeCalls[2].enabled === false &&
		select.value === 'beta' && betaChoice && betaChoice.disabled && betaChoice.hidden &&
		listChoices(select).filter((choice) => !choice.disabled).map((choice) => choice.value).join(',') === 'alpha' &&
		templateRowIds().join(',') === 'alpha,beta' && port.value === '9816' &&
		JSON.stringify(uci._changes) === dirtyUnavailableSelection &&
		runtime.formValueChanges.length === unavailableInputEvents &&
		runtime.listValueChanges.length === unavailableListEvents && mapRenderState() === unavailableMaps,
		'disabling a dirty default preserves it only as a hidden unavailable choice while refreshing the table');
	check(hasDefaultValidation && defaultTemplate.validate('main', 'beta') !== true &&
		defaultTemplate.validate('main', 'alpha') === true &&
		defaultTemplate.keylist.join(',') === 'alpha' &&
		defaultTemplate.vallist.join(',') === 'Alpha renamed' &&
		defaultTemplate.validationCalls.length === unavailableValidationCalls + 1 &&
		defaultTemplate.validationCalls[defaultTemplate.validationCalls.length - 1] === 'main' &&
		defaultTemplate.validState !== true && unavailableParseRejected &&
		JSON.stringify(uci._changes) === dirtyUnavailableSelection,
		'an invalid refreshed default is revalidated while the ListValue model contains enabled templates only');

	const rowsBeforeRestore = templateRowIds().join(',');
	const choicesBeforeRestore = JSON.stringify(listChoices(select));
	const selectionBeforeRestore = select.value;
	const listEventsBeforeRestore = runtime.listValueChanges.length;
	const inputEventsBeforeRestore = runtime.formValueChanges.length;
	const dirtyBeforeRestore = JSON.stringify(uci._changes);
	const mapsBeforeRestore = mapRenderState();
	document.getElementById('sbsc_tpl_id').value = 'alpha';
	document.getElementById('sbsc_tpl_name').value = 'Alpha renamed';
	document.getElementById('sbsc_tpl_file').value = 'alpha.json';
	document.getElementById('sbsc_tpl_content').value = '{}';
	await mod.saveTemplate();
	const saveStatus = document.getElementById('sbsc_tpl_save_status');
	check(templateRowIds().join(',') === rowsBeforeRestore &&
		JSON.stringify(listChoices(select)) === choicesBeforeRestore &&
		select.value === selectionBeforeRestore &&
		runtime.listValueChanges.length === listEventsBeforeRestore &&
		runtime.formValueChanges.length === inputEventsBeforeRestore && port.value === '9816' &&
		JSON.stringify(uci._changes) === dirtyBeforeRestore && mapRenderState() === mapsBeforeRestore &&
		saveStatus && /saved.*refresh/i.test(saveStatus.textContent) && saveStatus.style.color === '#b00',
		'a failed post-save refresh restores both rendered structures and reports a distinct refresh warning');

	const olderRequest = mod.reloadTemplateList();
	const newerRequest = mod.reloadTemplateList();
	staleNewer.resolve({ templates: [
		{ id: 'alpha', enabled: true, name: 'Alpha latest', file: 'alpha.json', size: 1, mtime: '8' },
		{ id: 'delta', enabled: true, name: 'Delta', file: 'delta.json', size: 5, mtime: '8' }
	] });
	await newerRequest;
	check(templateRowIds().join(',') === 'alpha,delta' &&
		listChoices(select).filter((choice) => !choice.disabled).map((choice) => choice.value).join(',') === 'alpha,delta',
		'the newest template response commits while an older request remains in flight');
	staleOlder.resolve({ templates: [
		{ id: 'alpha', enabled: true, name: 'Alpha stale', file: 'alpha.json', size: 1, mtime: '7' },
		{ id: 'beta', enabled: true, name: 'Beta stale', file: 'beta.json', size: 2, mtime: '7' }
	] });
	await olderRequest;
	check(templateRowIds().join(',') === 'alpha,delta' &&
		listChoices(select).filter((choice) => !choice.disabled).map((choice) => choice.value).join(',') === 'alpha,delta' &&
		listChoices(select).some((choice) => choice.value === 'alpha' && choice.label === 'Alpha latest') &&
		port.value === '9816' && JSON.stringify(uci._changes) === dirtyBeforeRestore &&
		mapRenderState() === mapsBeforeRestore,
		'a stale template response cannot overwrite the latest atomic table and dropdown snapshot');
}

async function exerciseOverviewRenderingSafety() {
	const payload = '<img src=x onerror="globalThis.pwned=1">&unsafe';
	const status = {
		running: true,
		healthy: true,
		enabled: false,
		health: payload,
		update_log: payload,
		action_state: 'running',
		action: payload,
		config_digest: 'new-digest',
		config_error: payload
	};
	installGlobals({
		status,
		generate: { code: 0, output: payload },
		list_templates: { templates: [] }
	});
	const check = (condition, description) => condition ? ok(description) : fail(description);

	// Guard the test double itself: this is the subtle LuCI E() distinction the
	// view must obey. If this fixture ever flattens both cases into the same
	// representation, the injection checks below would become meaningless.
	const scalar = E('div', {}, payload);
	const array = E('div', {}, [ payload ]);
	check(scalar.innerHTML === payload,
		'the LuCI E() fixture treats a scalar string as innerHTML');
	check(array.innerHTML === '' && array.childNodes[0] && array.childNodes[0].nodeType === 3 &&
	      array.childNodes[0].data === payload,
		'the LuCI E() fixture treats an array string as a text node');

	let mod;
	try {
		mod = new Function(fs.readFileSync(path.join(VIEW_DIR, 'overview.js'), 'utf8'))();
	} catch (error) {
		fail('overview safety fixture evaluates', error);
		return;
	}

	const statusRoot = mod.renderStatus(status);
	document.body.appendChild(statusRoot);
	const preNodes = findNodes(statusRoot, (node) => node.tagName === 'PRE');
	check(preNodes.length === 2 && preNodes.every((node) => isLiteralText(node, payload)),
		'health and update-log payloads render as literal text');
	const actionNode = findNodes(statusRoot, (node) =>
		node.tagName === 'EM' && node.textContent.includes(payload))[0];
	check(isLiteralText(actionNode, payload),
		'the background action name renders as literal text');

	const template = {
		id: payload,
		enabled: true,
		name: payload,
		file: payload,
		size: payload,
		mtime: payload
	};
	const row = mod.buildTemplateRow(template);
	const cells = row.childNodes.filter((node) => node.tagName === 'TD');
	check(hasClasses(row, [ 'tr', 'cbi-section-table-row' ]) &&
	      cells.length === 7 &&
	      cells.every((cell) => hasClasses(cell, [ 'td', 'cbi-section-table-cell' ]) &&
		      cell.attributes['data-title']),
		'template rows use responsive LuCI row, cell, and data-title classes');
	check(cells.slice(0, 6).every((cell) => cell.innerHTML === '') &&
	      [ 0, 2, 3, 4, 5 ].every((index) => isLiteralText(cells[index], payload)),
		'template metadata renders as literal text');
	check(hasClasses(cells[6], [ 'td', 'nowrap', 'cbi-section-actions' ]) &&
	      cells[6].childNodes.length === 1 && cells[6].firstChild.tagName === 'DIV' &&
	      findNodes(cells[6].firstChild, (node) => node.tagName === 'BUTTON').length === 2,
		'template actions use the standard LuCI actions cell');

	const manager = mod.renderTemplateManager([ template ]);
	document.body.appendChild(manager);
	const table = findNodes(manager, (node) => node.tagName === 'TABLE')[0];
	const titleRow = findNodes(table, (node) =>
		node.tagName === 'TR' && node.classList.contains('cbi-section-table-titles'))[0];
	const headings = findNodes(titleRow, (node) => node.tagName === 'TH');
	const tbody = findNodes(table, (node) => node.tagName === 'TBODY')[0];
	const thead = findNodes(table, (node) => node.tagName === 'THEAD')[0];
	check(hasClasses(table, [ 'table', 'cbi-section-table' ]) &&
	      hasClasses(thead, [ 'thead', 'cbi-section-thead' ]) &&
	      hasClasses(titleRow, [ 'tr', 'cbi-section-table-titles' ]) &&
	      headings.length === 7 && headings.every((node) => node.classList.contains('th')) &&
	      headings[6].classList.contains('cbi-section-actions') &&
	      tbody && tbody.attributes.id === 'sbsc_tpl_tbody' &&
	      hasClasses(tbody, [ 'tbody', 'cbi-section-tbody' ]),
		'template manager uses the stock responsive LuCI table structure');

	const integration = mod.renderIntegration({ converted_url: payload, lan_url: '' });
	document.body.appendChild(integration);
	const copyButton = findNodes(integration, (node) =>
		node.tagName === 'BUTTON' && typeof node.attributes.click === 'function')[0];
	try {
		copyButton.attributes.click({ preventDefault() {} });
		const textarea = document._created.filter((node) => node.tagName === 'TEXTAREA' &&
			node.attributes.style && node.attributes.style.includes('left:-9999px')).pop();
		check(isLiteralText(textarea, payload),
			'the fallback clipboard textarea contains a literal text node');
	} catch (error) {
		fail('fallback clipboard path renders safely', error);
	}

	try {
		await mod.doAction('generate');
		const toastWrap = document.getElementById('sbf_toast_wrap');
		const toast = toastWrap && toastWrap.lastChild;
		check(toast && toast.tagName === 'DIV' && isLiteralText(toast, payload),
			'RPC action output in a toast renders as literal text');
	} catch (error) {
		fail('RPC action toast renders safely', error);
	}

	try {
		await mod.reconcileAfterApply('old-digest');
		const notification = ui._notifications[ui._notifications.length - 1];
		const code = notification && findNodes(notification.content,
			(node) => node.tagName === 'CODE')[0];
		check(isLiteralText(code, payload),
			'configuration errors in notifications render as literal text');
	} catch (error) {
		fail('configuration-error notification renders safely', error);
	}
}

async function exerciseCustomLogoApplyContracts() {
	const full = path.join(VIEW_DIR, 'customlogo.js');
	const source = fs.readFileSync(full, 'utf8');
	const evaluate = () => new Function(source)();
	const check = (condition, description) => condition ? ok(description) : fail(description);
	const flush = () => new Promise((resolve) => setImmediate(resolve));
	let order;
	let runtime;
	let mod;

	order = [];
	runtime = installGlobals({
		tuning_apply() {
			order.push('tuning');
			return { code: 0, output: '' };
		}
	});
	mod = evaluate();
	mod.handleSave = function() {
		order.push('save');
		return Promise.resolve();
	};
	uci._changes = {};
	ui.changes.apply = function(checked) {
		order.push('apply');
		runtime.applyCalls.push(checked);
	};
	await mod.handleSaveApply({}, '1');
	check(order.join(',') === 'save,tuning,apply',
		'customlogo runs tuning before the official no-changes apply path');
	check(runtime.applyCalls.length === 1 && runtime.applyCalls[0] === false,
		'customlogo invokes official unchecked apply exactly once without pending changes');

	order = [];
	runtime = installGlobals({
		tuning_apply() {
			order.push('tuning');
			return { code: 0, output: '' };
		}
	});
	mod = evaluate();
	mod.handleSave = function() {
		order.push('save');
		return Promise.resolve();
	};
	mod._reloadAfterTuningApply = function() {
		order.push('reload');
	};
	uci._changes = { tuning: [ [ 'set', 'main', 'enabled', '1' ] ] };
	ui.changes.apply = function(checked) {
		order.push('apply');
		runtime.applyCalls.push(checked);
	};
	await mod.handleSaveApply({}, '0');
	check(order.join(',') === 'save,apply',
		'customlogo waits for the committed UCI event before tuning pending changes');
	check(runtime.applyCalls.length === 1 && runtime.applyCalls[0] === true,
		'customlogo invokes official checked apply exactly once with pending changes');
	document.dispatchEvent(new CustomEvent('uci-applied'));
	check(L.env.apply_display >= 60,
		'customlogo extends the official post-commit reload window synchronously');
	await flush();
	check(order.join(',') === 'save,apply,tuning,reload',
		'customlogo applies committed tuning and reloads after helper success');
	check((runtime.listeners['uci-applied'] || new Set()).size === 0 &&
	      (runtime.listeners['uci-reverted'] || new Set()).size === 0,
		'customlogo removes apply and revert listeners after a commit');
	check(L.env.apply_display === 1 && L.env.rpctimeout === 20,
		'customlogo restores LuCI timing settings after helper success');
	document.dispatchEvent(new CustomEvent('uci-applied'));
	await flush();
	check(order.filter((step) => step === 'tuning').length === 1 &&
	      order.filter((step) => step === 'reload').length === 1,
		'customlogo ignores duplicate committed-UCI events');

	let applyError;
	let tuningCalls = 0;
	runtime = installGlobals({
		tuning_apply() {
			tuningCalls++;
			return { code: 0, output: '' };
		}
	});
	mod = evaluate();
	mod.handleSave = function() { return Promise.resolve(); };
	uci._changes = { tuning: [ [ 'set', 'main', 'enabled', '1' ] ] };
	ui.changes.apply = function() { throw new Error('apply failed'); };
	try {
		await mod.handleSaveApply({}, '0');
	}
	catch (error) {
		applyError = error;
	}
	check(applyError && applyError.message === 'apply failed' && tuningCalls === 0,
		'customlogo propagates an official apply startup error without running tuning');
	check((runtime.listeners['uci-applied'] || new Set()).size === 0 &&
	      (runtime.listeners['uci-reverted'] || new Set()).size === 0,
		'customlogo removes its event hooks when official apply startup throws');

	const helperFailure = '<b>helper failed</b>';
	runtime = installGlobals({
		tuning_apply: { code: 1, output: helperFailure }
	});
	mod = evaluate();
	mod.handleSave = function() { return Promise.resolve(); };
	let reloads = 0;
	mod._reloadAfterTuningApply = function() { reloads++; };
	uci._changes = { tuning: [ [ 'set', 'main', 'enabled', '1' ] ] };
	await mod.handleSaveApply({}, '0');
	document.dispatchEvent(new CustomEvent('uci-applied'));
	await flush();
	const failureStatus = runtime.statuses[runtime.statuses.length - 1];
	const failureCode = failureStatus && findNodes(failureStatus.content,
		(node) => node.tagName === 'CODE')[0];
	check(reloads === 0 && failureStatus && failureStatus.level === 'warning',
		'customlogo keeps the official status visible instead of reloading on helper failure');
	check(isLiteralText(failureCode, helperFailure),
		'customlogo renders tuning helper output as literal text');
	check(L.env.apply_display === 1 && L.env.rpctimeout === 20,
		'customlogo restores LuCI timing settings after helper failure');

	tuningCalls = 0;
	runtime = installGlobals({
		tuning_apply() {
			tuningCalls++;
			return { code: 0, output: '' };
		}
	});
	mod = evaluate();
	mod.handleSave = function() { return Promise.resolve(); };
	uci._changes = { tuning: [ [ 'set', 'main', 'enabled', '1' ] ] };
	await mod.handleSaveApply({}, '0');
	document.dispatchEvent(new CustomEvent('uci-reverted'));
	document.dispatchEvent(new CustomEvent('uci-applied'));
	await flush();
	check(tuningCalls === 0 &&
	      (runtime.listeners['uci-applied'] || new Set()).size === 0 &&
	      (runtime.listeners['uci-reverted'] || new Set()).size === 0,
		'customlogo cancels the tuning hook when checked apply is reverted');

	const UploadValue = form.Value.extensions[0];
	const upload = new UploadValue();
	Object.assign(upload, {
		map: { readonly: false },
		default: '',
		readonly: false,
		accept: '.png,image/png',
		extensions: [ 'png' ],
		uploadKind: 'logo',
		stagingPath: '/var/run/liquid-formula-upload/pending-logo',
		builtinPath: null,
		cbid() { return 'cbid.customlogo.main.logo'; },
		getValidator() { return function() { return true; }; }
	});
	const uploadRoot = upload.renderWidget('main', 0, '');
	const officialFrame = E('div', { 'class': 'cbi-value-field' }, [ uploadRoot ]);
	check(uploadRoot && !uploadRoot.classList.contains('cbi-value-field'),
		'customlogo upload widget directly returns a non-field root node');
	check(findNodes(officialFrame, (node) =>
		node.classList && node.classList.contains('cbi-value-field')).length === 1,
		'the official form frame produces exactly one cbi-value-field around the upload widget');

	const widgetStart = source.indexOf('\trenderWidget: function');
	const widgetEnd = source.indexOf('\n\tvalidate: function', widgetStart);
	const widgetSource = source.slice(widgetStart, widgetEnd);
	check(!/return E\('div', \{[^}]*['"]class['"]:\s*['"]cbi-value-field/.test(widgetSource),
		'customlogo upload widget does not nest a cbi-value-field');
	check(source.includes("E('strong', {}, [ value || '?' ])") &&
	      source.includes("E('code', {}, [ text ])"),
		'customlogo wraps dynamic live values and helper output in text-node arrays');
	check(source.includes("E('p', [\n\t\t\t\t\t_('Unsupported file name or type.") &&
	      source.includes("E('p', [\n\t\t\t\t\t_('Upload failed: %s')"),
		'customlogo wraps dynamic upload errors in text-node arrays');
}

async function renderDpiWanFixture(file, service, netdevs) {
	const runtime = installGlobals({
		list(name) {
			return {
				[name]: {
					instances: {
						[name]: {
							running: netdevs.length > 0,
							netdev: netdevs.slice()
						}
					}
				}
			};
		}
	});
	const manual = [ 'manual0', 'manual1' ];
	uci._data[`${service}.main.interface_mode`] = 'selected';
	uci._data[`${service}.main.interface`] = manual.slice();

	let mod;
	try {
		mod = new Function(fs.readFileSync(path.join(VIEW_DIR, file), 'utf8'))();
		const data = await mod.load();
		await mod.render(data);
	} catch (error) {
		fail(`${file} renders the WAN mode contract fixture`, error);
		return null;
	}

	const map = renderedMaps.find((candidate) => candidate.config === service);
	if (!map) {
		fail(`${file} renders its service map`);
		return null;
	}
	return { runtime, map, manual };
}

async function exerciseDpiWanModeContracts() {
	const check = (condition, description) => condition ? ok(description) : fail(description);
	const cases = [
		{
			file: 'fakehttp.js',
			service: 'fakehttp',
			modes: [ 'auto', 'selected', 'all' ]
		},
		{
			file: 'fakesip.js',
			service: 'fakesip',
			modes: [ 'auto', 'selected' ]
		}
	];

	for (const item of cases) {
		const fixture = await renderDpiWanFixture(item.file, item.service,
			[ 'pppoe-wan', 'eth6' ]);
		if (!fixture)
			continue;
		const mode = fixture.map.lookupOption('interface_mode')[0];
		const interfaces = fixture.map.lookupOption('interface')[0];
		const resolved = fixture.map.lookupOption('_resolved_wan')[0];

		check(mode && mode.keylist.join(',') === item.modes.join(',') &&
		      mode.default === 'auto',
			`${item.service} offers the intended auto/manual interface modes with auto as default`);
		check(interfaces && interfaces.deps.some((dep) =>
			dep[0] === 'interface_mode' && dep[1] === 'selected'),
			`${item.service} shows the manual device list only in selected mode`);
		check(interfaces && interfaces.retain === true,
			`${item.service} explicitly retains the hidden manual device list`);

		if (mode) {
			uci.set(item.service, 'main', 'interface_mode', 'auto');
			await fixture.map.parse();
			check(JSON.stringify(uci.get(item.service, 'main', 'interface')) ===
			      JSON.stringify(fixture.manual),
				`${item.service} stock map.parse preserves manual devices while auto mode hides them`);
			uci.set(item.service, 'main', 'interface_mode', 'selected');
			await fixture.map.parse();
			check(JSON.stringify(uci.get(item.service, 'main', 'interface')) ===
			      JSON.stringify(fixture.manual),
				`${item.service} switching back to selected restores the same manual values`);
		} else {
			fail(`${item.service} preserves manual values across mode changes`);
			fail(`${item.service} restores manual values when selected again`);
		}

		check(resolved && String(resolved.cfgvalue('main')).includes('pppoe-wan') &&
		      String(resolved.cfgvalue('main')).includes('eth6'),
			`${item.service} displays the current resolved runtime WAN devices`);
		check(!fixture.runtime.fsReads.includes('/proc/net/route'),
			`${item.service} never reads /proc/net/route in LuCI`);

		const unavailable = await renderDpiWanFixture(item.file, item.service, []);
		if (unavailable) {
			const unavailableOption = unavailable.map.lookupOption('_resolved_wan')[0];
			check(unavailableOption &&
			      /unavailable/i.test(String(unavailableOption.cfgvalue('main'))),
				`${item.service} clearly reports unavailable runtime WAN resolution`);
		}
	}

	const aclPath = path.join(ROOT,
		'openwrt-feed/luci-app-liquid-formula/root/usr/share/rpcd/acl.d/luci-app-liquid-formula.json');
	const acl = JSON.parse(fs.readFileSync(aclPath, 'utf8'));
	const fileGrants = acl['luci-app-liquid-formula'].read.file;
	check(!Object.prototype.hasOwnProperty.call(fileGrants, '/proc/net/route'),
		'LuCI ACL no longer grants obsolete /proc/net/route access');
}

// tuning_status 返回什么, 直接决定 customlogo.js 里那几个分支走哪条。
// 三种形态都跑一遍: 完整响应、后端不可用(null)、字段缺失。
const RPC_SHAPES = [
	['full response', {
		tuning_status: {
			live: { tcp_fastopen: '3', default_qdisc: 'cake',
			        congestion_control: 'bbr', tcp_max_syn_backlog: '512' },
			available_congestion_control: 'reno cubic bbr',
			openwrt_release: '25.12.0',
			cake_module: true, bbr_module: true, sysctl_conf_conflict: false,
			irqbalance: { installed: true, enabled: '1', running: true }
		},
		status: {}, list_templates: { templates: [] }
	}],
	['backend unavailable', { tuning_status: null, status: null, list_templates: null }],
	['missing fields', { tuning_status: {}, status: {}, list_templates: {} }],
	['modules absent', {
		tuning_status: {
			live: {}, available_congestion_control: '',
			cake_module: false, bbr_module: false, sysctl_conf_conflict: true,
			irqbalance: { installed: false, enabled: '0', running: false }
		},
		status: {}, list_templates: { templates: [] }
	}]
];

async function exerciseTuningChoiceContracts() {
	const releases = [
		['24.10 hides cake_mq', '24.10.4', false],
		['25.12 shows cake_mq', '25.12.0', true],
		['later releases show cake_mq', '26.1.0', true],
		// A bare SNAPSHOT is main-branch OpenWrt and always tracks something
		// newer than the last tagged release, so it must not be lumped in with
		// unparsable values. Keep these cases identical to the shell-side
		// coverage in test_tuning.sh.
		['main-branch SNAPSHOT shows cake_mq', 'SNAPSHOT', true],
		['lowercase snapshot shows cake_mq', 'snapshot', true],
		['25.12 branch snapshot shows cake_mq', '25.12-SNAPSHOT', true],
		['pre-25.12 branch snapshot hides cake_mq', '24.10-SNAPSHOT', false],
		['unparsable releases hide cake_mq', 'not-a-release', false],
		['missing releases hide cake_mq', '', false]
	];

	for (const [description, release, expectCakeMq] of releases) {
		installGlobals({
			tuning_status: {
				live: {}, available_congestion_control: 'reno cubic bbr',
				openwrt_release: release,
				cake_module: true, bbr_module: true, sysctl_conf_conflict: false,
				irqbalance: { installed: true, enabled: '0', running: false }
			}
		});

		let mod;
		try {
			mod = new Function(fs.readFileSync(path.join(VIEW_DIR, 'customlogo.js'), 'utf8'))();
			await mod.render(await mod.load());
		} catch (error) {
			fail(`tuning choices render for ${description}`, error);
			continue;
		}

		const map = renderedMaps.find((candidate) => candidate.config === 'customlogo');
		const options = map ? map.sections.flatMap((section) => section.options) : [];
		const option = (name) => options.find((candidate) => candidate.option === name);
		const check = (condition, message) => condition ? ok(message) : fail(message);
		const qdisc = option('tuning_default_qdisc');
		const congestion = option('tuning_congestion_control');
		const backlog = option('tuning_backlog');

		check(qdisc && qdisc.type === form.ListValue,
			`qdisc is a ListValue (${description})`);
		check(congestion && congestion.type === form.ListValue,
			`congestion control is a ListValue (${description})`);
		check(backlog && backlog.type === form.ListValue,
			`SYN backlog is a ListValue (${description})`);
		check(qdisc && JSON.stringify(qdisc.keylist) === JSON.stringify(
			expectCakeMq ? ['cake', 'cake_mq', 'fq_codel'] : ['cake', 'fq_codel']),
			`qdisc choices obey the release gate (${description})`);
		check(congestion && JSON.stringify(congestion.keylist) === JSON.stringify(['bbr', 'cubic', 'reno']),
			`congestion choices are fixed (${description})`);
		check(backlog && JSON.stringify(backlog.keylist) === JSON.stringify(['128', '512', '1024', '2048']),
			`backlog choices are fixed (${description})`);
		check(qdisc && qdisc.default === 'cake', `qdisc default is cake (${description})`);
		check(congestion && congestion.default === 'bbr', `congestion default is bbr (${description})`);
		check(backlog && backlog.default === '512', `backlog default is 512 (${description})`);
	}
}

async function exerciseView(file, shapeName, responses) {
	const full = path.join(VIEW_DIR, file);
	installGlobals(responses);

	// LuCI 视图以顶层 `return view.extend({...})` 结束。CommonJS 会丢弃
	// 顶层 return 的值, 所以把整份源码塞进一个函数体里执行来取回它。
	let mod;
	try {
		mod = new Function(fs.readFileSync(full, 'utf8'))();
	} catch (error) {
		fail(`${file} evaluates (${shapeName})`, error);
		return;
	}
	if (!mod || typeof mod.render !== 'function') {
		fail(`${file} exports a view with a render function (${shapeName})`);
		return;
	}

	let data;
	try {
		data = await mod.load();
	} catch (error) {
		fail(`${file} load() resolves (${shapeName})`, error);
		return;
	}
	ok(`${file} load() resolves (${shapeName})`);

	try {
		await mod.render(data);
		ok(`${file} render() completes without a runtime error (${shapeName})`);
	} catch (error) {
		fail(`${file} render() completes without a runtime error (${shapeName})`, error);
	}
}

async function main() {
	const views = fs.readdirSync(VIEW_DIR).filter((f) => f.endsWith('.js')).sort();
	if (views.length === 0) {
		fail('the view directory contains at least one page');
	}

	for (const view of views) {
		for (const [shapeName, responses] of RPC_SHAPES)
			await exerciseView(view, shapeName, responses);
	}
	await exerciseOverviewContracts();
	await exerciseTemplateRefreshContracts();
	await exerciseOverviewRenderingSafety();
	await exerciseCustomLogoApplyContracts();
	await exerciseTuningChoiceContracts();
	await exerciseDpiWanModeContracts();

	console.log(`${checks} checks, ${failures} failures`);
	process.exit(failures ? 1 : 0);
}

main().catch((error) => {
	console.error(error);
	process.exit(1);
});
