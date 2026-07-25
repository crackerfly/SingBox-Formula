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
		addEventListener() {},
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
	}
	value(key, label) { this.keylist.push(key); this.vallist.push(label); }
	depends(a, b) { this.deps.push([a, b]); }
}

class StubSection {
	constructor(type, sectionType, title, description) {
		this.sectiontype = sectionType;
		this.title = title;
		this.description = description;
		this.options = [];
	}
	option(type, name, title, description, extra) {
		const o = new StubOption(type, name, title, description, extra);
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
		const s = new StubSection(kind, b, c, d);
		this.sections.push(s);
		return s;
	}
	render() { return Promise.resolve(makeElement('div')); }
	save() { return Promise.resolve(); }
}

function installGlobals(rpcResponses) {
	renderedMaps = [];
	const createdNodes = [];
	const runtime = {
		applyCalls: [],
		notifications: [],
		statuses: [],
		listeners: Object.create(null)
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
	global.fs = {
		read() { return Promise.resolve(''); },
		read_direct() { return Promise.resolve(''); },
		write() { return Promise.resolve(); },
		stat() { return Promise.resolve({ type: 'file', size: 0 }); },
		list() { return Promise.resolve([]); },
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
	const fakehttpBypass = option('momo_bypass_fakehttp');
	const fakesipBypass = option('momo_bypass_fakesip');
	const check = (condition, description) => condition ? ok(description) : fail(description);

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

// tuning_status 返回什么, 直接决定 customlogo.js 里那几个分支走哪条。
// 三种形态都跑一遍: 完整响应、后端不可用(null)、字段缺失。
const RPC_SHAPES = [
	['full response', {
		tuning_status: {
			live: { tcp_fastopen: '3', default_qdisc: 'cake',
			        congestion_control: 'bbr', tcp_max_syn_backlog: '512' },
			available_congestion_control: 'reno cubic bbr',
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
	await exerciseOverviewRenderingSafety();
	await exerciseCustomLogoApplyContracts();

	console.log(`${checks} checks, ${failures} failures`);
	process.exit(failures ? 1 : 0);
}

main().catch((error) => {
	console.error(error);
	process.exit(1);
});
