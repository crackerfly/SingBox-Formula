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
const BUDGET_CASES = path.join(ROOT, 'tests/subscription/budget-cases.tsv');

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

function normalizeList(value) {
	if (Array.isArray(value))
		return value.slice();
	if (value == null || value === '')
		return [];
	return [ String(value) ];
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
	cfgvalue(sectionId) {
		return uci.get(this.map.config, sectionId, this.option);
	}
	formvalue(sectionId) {
		const type = this.type && this.type._formTypeName;
		if (type === 'DynamicList')
			return this._renderedElement ? this._renderedElement.getValue() : [];
		return this._renderedElement ? this._renderedElement.value : '';
	}
	write(sectionId, value) {
		const stored = Array.isArray(value) ? value.slice() : value;
		uci.set(this.map.config, sectionId, this.option, stored);
		const runtime = this.map._runtime;
		if (runtime) {
			runtime.formValueChanges.push({ option: this.option, value: stored });
			const changes = uci._changes[this.map.config] || (uci._changes[this.map.config] = []);
			changes.push([ 'set', sectionId, this.option, stored ]);
		}
	}
	remove(sectionId) {
		uci.unset(this.map.config, sectionId, this.option);
		const runtime = this.map._runtime;
		if (runtime) {
			const changes = uci._changes[this.map.config] || (uci._changes[this.map.config] = []);
			changes.push([ 'delete', sectionId, this.option ]);
		}
	}
	parse(sectionId) {
		const type = this.type && this.type._formTypeName;
		const value = this.formvalue(sectionId);
		let result = true;
		/*
		 * Match stock LuCI's form.DynamicList -> ui.DynamicList boundary:
		 * getValidator() is attached only to the optional "add item" text
		 * input. Existing hidden list items are not passed through it again
		 * by AbstractValue.parse().
		 */
		if (this.validate && type !== 'DynamicList')
			result = this.validate(sectionId, value);
		if (result !== true)
			return Promise.reject(new Error(String(result)));
		if (type === 'DynamicList') {
			if (value.length)
				this.write(sectionId, value);
			else
				this.remove(sectionId);
			return Promise.resolve(value.slice());
		}
		return Promise.resolve(value);
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
	lookupOption(name, sectionId) {
		const matches = this.sections.reduce((found, section) =>
			found.concat(section.options.filter((option) => option.option === name)), []);
		if (sectionId != null) {
			const option = matches.find((candidate) => candidate.section_id === sectionId);
			return option ? [ option, sectionId ] : null;
		}
		return matches;
	}
	render() {
		this.renderCalls = (this.renderCalls || 0) + 1;
		const root = makeElement('div');
		const runtime = this._runtime;
		this.sections.forEach((section) => section.options.forEach((option) => {
			const type = option.type && option.type._formTypeName;
			if (type !== 'ListValue' && type !== 'Value' && type !== 'TextValue' &&
			    type !== 'DynamicList' && type !== 'Flag')
				return;
			const recordChange = function(value) {
				runtime.formValueChanges.push({ option: option.option, value: value });
				uci._data[`${this.config}.${option.section_id}.${option.option}`] = value;
				const changes = uci._changes[this.config] || (uci._changes[this.config] = []);
				changes.push([ 'set', option.section_id, option.option, value ]);
			}.bind(this);
			if (type === 'Value' || type === 'Flag') {
				const input = makeElement('input', {
					'id': `cbid.${this.config}.${option.section_id}.${option.option}`,
					'value': uci.get(this.config, option.section_id, option.option) || option.default || ''
				});
				input.addEventListener('input', function() { recordChange(input.value); });
				option._renderedElement = input;
				runtime.formInputs[option.option] = input;
				root.appendChild(input);
				return;
			}
			if (type === 'TextValue') {
				const value = option.cfgvalue(option.section_id);
				const textarea = makeElement('textarea', {
					'id': `cbid.${this.config}.${option.section_id}.${option.option}`,
					'value': value == null ? (option.default || '') : value
				});
				textarea.addEventListener('input', function() { recordChange(textarea.value); });
				option._renderedElement = textarea;
				runtime.textValues[option.option] = textarea;
				root.appendChild(textarea);
				return;
			}
			if (type === 'DynamicList') {
				const list = makeElement('div', {
					'id': `cbid.${this.config}.${option.section_id}.${option.option}`
				});
				list.inputs = [];
				list.setValue = function(value) {
					const values = normalizeList(value);
					while (list.firstChild)
						list.removeChild(list.firstChild);
					const accepted = values.filter((item, index) =>
						(runtime.luciBranch !== '24.10' && option.allowduplicates) ||
						values.indexOf(item) === index);
					list.inputs = accepted.map((item, index) => {
						const input = makeElement('input', {
							'id': `${list.attributes.id}.${index}`,
							'value': item
						});
						input.value = item;
						list.appendChild(input);
						return input;
					});
				};
				list.getValue = function() {
					return list.inputs.map((input) => input.value);
				};
				list.setValue(uci.get(this.config, option.section_id, option.option));
				option._renderedElement = list;
				runtime.dynamicLists[option.option] = list;
				root.appendChild(list);
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
				value: value, label: option.vallist[index]
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
		const options = this.sections.reduce((all, section) =>
			all.concat(section.options), []);
		const values = Object.create(null);

		options.forEach((option) => {
			if (option._renderedElement)
				values[option.option] = option.formvalue(sectionId);
			else
				values[option.option] = uci.get(this.config, sectionId, option.option);
		});

		options.forEach((option) => {
			if (!option.deps.length)
				return;
			const active = option.deps.some((dep) =>
				String(values[dep[0]] == null ? '' : values[dep[0]]) === String(dep[1]));
			/*
			 * Stock LuCI AbstractValue.parse() removes an inactive option
			 * unless the view explicitly marks it retain=true.
			 */
			if (!active && !option.retain)
				option.remove(sectionId);
		});

		options.forEach((option) => {
			const type = option.type && option.type._formTypeName;
			if (type === 'ListValue' && option._renderedElement)
				option.write(sectionId, option.formvalue(sectionId));
		});
		return Promise.resolve();
	}
	save() { return Promise.resolve(); }
}

function installGlobals(rpcResponses, options) {
	options = options || {};
	renderedMaps = [];
	const createdNodes = [];
	const runtime = {
		applyCalls: [],
		notifications: [],
		statuses: [],
		listeners: Object.create(null),
		listValues: Object.create(null),
		listValueChanges: [],
		luciBranch: options.luciBranch || '25.12',
		dynamicLists: Object.create(null),
		subscriptionWrites: [],
		textValues: Object.create(null),
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
			if (config === 'liquid_formula' && option === 'subscription_url')
				runtime.subscriptionWrites.push({
					option: option,
					value: Array.isArray(value) ? value.slice() : value
				});
		},
		unset(config, section, option) {
			delete this._data[`${config}.${section}.${option}`];
			if (config === 'liquid_formula' && option === 'subscription_url')
				runtime.subscriptionWrites.push({ option: option, value: null });
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

async function exerciseSubscriptionTextValueContracts(luciBranch) {
	const runtime = installGlobals({ status: {}, list_templates: { templates: [] } }, {
		luciBranch: luciBranch
	});
	const sourceA = 'https://first.example/sub?token=alpha&region=東京';
	const sourceB = "https://second.example/O'Brien?encoded=%27&name=O%27Brien";
	const initial = [ sourceA, sourceB, sourceA ];
	const eight = [
		sourceA,
		sourceB,
		'http://third.example/sub?x=3&label=三',
		'https://fourth.example/sub?x=4',
		'https://fifth.example/sub?x=5',
		'https://sixth.example/sub?x=6',
		'https://seventh.example/sub?x=7',
		sourceA
	];
	uci._data['liquid_formula.main.enabled'] = '1';
	uci._data['liquid_formula.main.subscription_url'] = initial.slice();

	let mod;
	try {
		mod = new Function(fs.readFileSync(path.join(VIEW_DIR, 'overview.js'), 'utf8'))();
		const data = await mod.load();
		await mod.render(data);
	} catch (error) {
		fail(`overview subscription fixture renders with LuCI ${luciBranch} semantics`, error);
		return;
	}

	const map = renderedMaps.find((candidate) => candidate.config === 'liquid_formula');
	const options = map ? map.sections.flatMap((section) => section.options) : [];
	const subscriptionUrls = options.find((candidate) => candidate.option === 'subscription_url');
	const textWidget = runtime.textValues.subscription_url;
	const enabledWidget = runtime.formInputs.enabled;
	const check = (condition, description) => condition ? ok(description) : fail(description);
	const sameList = (left, right) => Array.isArray(left) && Array.isArray(right) &&
		left.length === right.length && left.every((value, index) => value === right[index]);

	check(subscriptionUrls && subscriptionUrls.type &&
		subscriptionUrls.type._formTypeName === 'TextValue' && textWidget,
		`LuCI ${luciBranch} renders subscription URLs as one stable TextValue`);
	if (!subscriptionUrls || !textWidget || !enabledWidget)
		return;

	check(textWidget.value === initial.join('\n'),
		`LuCI ${luciBranch} initially renders [A, B, A] in exact order`);

	const parseText = async (text, liveEnabled, savedEnabled) => {
		uci._data['liquid_formula.main.enabled'] = savedEnabled ? '1' : '0';
		enabledWidget.value = liveEnabled ? '1' : '0';
		textWidget.value = text;
		const writesBefore = runtime.subscriptionWrites.length;
		let accepted = true;
		try {
			await subscriptionUrls.parse('main');
		} catch (error) {
			accepted = false;
		}
		return {
			accepted: accepted,
			writes: runtime.subscriptionWrites.slice(writesBefore),
			stored: uci._data['liquid_formula.main.subscription_url']
		};
	};

	runtime.formInputs.boot_delay.value = '123';
	const unchanged = await parseText(initial.join('\n'), true, true);
	check(unchanged.accepted && unchanged.writes.length === 1 &&
		sameList(unchanged.writes[0].value, initial) && sameList(unchanged.stored, initial),
		`LuCI ${luciBranch} unrelated form saves preserve existing duplicate URLs`);

	const pending = [ sourceB, sourceA, sourceB ];
	const pendingResult = await parseText(pending.join('\n'), true, true);
	check(pendingResult.accepted && pendingResult.writes.length === 1 &&
		sameList(pendingResult.writes[0].value, pending) && sameList(pendingResult.stored, pending),
		`LuCI ${luciBranch} directly saves pending duplicate URL occurrences`);

	const reordered = [ sourceB, sourceB, sourceA ];
	const reorderedResult = await parseText(reordered.join('\n'), true, true);
	check(reorderedResult.accepted && reorderedResult.writes.length === 1 &&
		sameList(reorderedResult.writes[0].value, reordered) &&
		sameList(reorderedResult.stored, reordered),
		`LuCI ${luciBranch} preserves an explicit duplicate URL reorder`);

	const acceptedEight = await parseText(eight.join('\n'), true, true);
	check(acceptedEight.accepted && acceptedEight.writes.length === 1 &&
		sameList(acceptedEight.writes[0].value, eight) && sameList(acceptedEight.stored, eight),
		`LuCI ${luciBranch} atomically accepts eight ordered URL lines`);

	const rejectsWithoutWriting = async (text, description) => {
		const result = await parseText(text, true, true);
		check(!result.accepted && result.writes.length === 0 && sameList(result.stored, eight),
			`LuCI ${luciBranch} ${description}`);
	};
	await rejectsWithoutWriting(eight.concat('https://ninth.example/sub').join('\n'),
		'atomically rejects a ninth URL line');
	await rejectsWithoutWriting(sourceA + '\n\n' + sourceB,
		'atomically rejects an empty URL line');
	await rejectsWithoutWriting(sourceA + '\n',
		'atomically rejects a trailing empty URL line');
	await rejectsWithoutWriting(sourceA + '\u0001',
		'atomically rejects URL control characters');
	await rejectsWithoutWriting('http://?query',
		'atomically rejects an empty URL authority');
	await rejectsWithoutWriting('https:///path',
		'atomically rejects a URL without a hostname');
	await rejectsWithoutWriting('http://:80/sub',
		'atomically rejects an empty hostname with a port');
	await rejectsWithoutWriting('http://user@:80/sub',
		'atomically rejects userinfo followed by an empty hostname');
	await rejectsWithoutWriting('https://exa%6Dple.com/sub',
		'atomically rejects an ASCII percent escape in the hostname');
	await rejectsWithoutWriting('https://user[bad]@provider.example/sub',
		'atomically rejects invalid userinfo characters');
	await rejectsWithoutWriting('https://provider|invalid.example/sub',
		'atomically rejects invalid hostname characters');
	await rejectsWithoutWriting('https://[x:y]/sub',
		'atomically rejects non-hexadecimal bracketed host groups');
	await rejectsWithoutWriting('https://[::::]/sub',
		'atomically rejects malformed IPv6 compression');
	await rejectsWithoutWriting('https://[2001::db8::1]/sub',
		'atomically rejects repeated IPv6 compression');
	await rejectsWithoutWriting('https://provider.example/raw space',
		'atomically rejects raw URL spaces');
	await rejectsWithoutWriting('https://provider.example/sub ',
		'atomically rejects trailing URL spaces');
	await rejectsWithoutWriting('https://provider.example/%zz',
		'atomically rejects malformed percent escapes');
	await rejectsWithoutWriting('https://provider.example/sub\u007f',
		'atomically rejects URL DEL bytes');
	await rejectsWithoutWriting('ftp://provider.example/sub',
		'atomically rejects a non-HTTP(S) URL');

	const rejectedEnabledEmpty = await parseText('', true, false);
	check(!rejectedEnabledEmpty.accepted && rejectedEnabledEmpty.writes.length === 0 &&
		sameList(rejectedEnabledEmpty.stored, eight),
		`LuCI ${luciBranch} uses the live enabled field to reject zero URLs`);

	const acceptedDisabledEmpty = await parseText('', false, true);
	check(acceptedDisabledEmpty.accepted && acceptedDisabledEmpty.writes.length === 1 &&
		acceptedDisabledEmpty.writes[0].value === null && acceptedDisabledEmpty.stored === undefined,
		`LuCI ${luciBranch} uses the live disabled field to unset zero URLs`);

	const validBoundaryURLs = [
		'HTTPS://provider.example/sub',
		'https://user:pass@provider.example/sub',
		'https://[2001:db8::1]/sub',
		'https://[::ffff:192.0.2.1]/sub',
		'https://[fe80::1%25eth0]/sub',
		'https://provider.example:0/sub',
		'https://provider.example:65536/sub',
		'https://provider.example/sub#fragment',
		'https://provider.example/sub?opaque=%zz',
		'https://provider.example/escaped%20space'
	];
	for (const validURL of validBoundaryURLs) {
		const accepted = await parseText(validURL, true, true);
		check(accepted.accepted && accepted.writes.length === 1 &&
			sameList(accepted.writes[0].value, [ validURL ]) &&
			sameList(accepted.stored, [ validURL ]),
			`LuCI ${luciBranch} accepts and preserves backend-valid URL ${validURL}`);
	}
}

async function exerciseOverviewContracts() {
	const runtime = installGlobals({ status: {}, list_templates: { templates: [] } });
	uci._data['liquid_formula.main.enabled'] = '1';
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
	const userAgent = option('user_agent');
	const subscriptionTimeout = option('subscription_timeout');
	const refreshInterval = option('refresh_interval');
	const fakehttpBypass = option('momo_bypass_fakehttp');
	const fakesipBypass = option('momo_bypass_fakesip');
	const check = (condition, description) => condition ? ok(description) : fail(description);
	check(userAgent && userAgent.type._formTypeName === 'Value' &&
		subscriptionTimeout && subscriptionTimeout.type._formTypeName === 'Value' &&
		refreshInterval && refreshInterval.type._formTypeName === 'Value',
		'overview keeps User-Agent, timeout, and refresh interval as one global scalar each');

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
	check(mod.actionWaitSeconds('refresh') === 630,
		'refresh wait covers refresh, lock acquisition, and synchronization');
	check(mod.actionWaitSeconds('check') === 1200 && mod.actionWaitSeconds('update') === 1200,
		'check and update cover startup, refresh, lock acquisition, synchronization, and fetch');
	mod._enabledTemplateCount = 10;
	uci._data['liquid_formula.main.subscription_timeout'] = '600';
	check(mod.actionWaitSeconds('update') === 33900,
		'frontend wait includes every enabled template without the obsolete cap');
	uci._data['liquid_formula.main.subscription_timeout'] = 'invalid';
	let invalidBudgetRejected = false;
	try {
		mod.actionWaitSeconds('check');
	} catch (error) {
		invalidBudgetRejected = true;
	}
	check(invalidBudgetRejected,
		'invalid timeout data prevents UI dispatch instead of using a fallback budget');

	const budgetCases = fs.readFileSync(BUDGET_CASES, 'utf8').split(/\r?\n/)
		.filter((line) => line && line[0] !== '#')
		.map((line) => {
			const fields = line.split('\t');
			return {
				name: fields[0],
				sources: Number(fields[1]),
				timeout: fields[2],
				enabled: Number(fields[3]),
				refresh: fields[5],
				apply: fields[6]
			};
		});
	for (const budgetCase of budgetCases) {
		uci._data['liquid_formula.main.subscription_url'] =
			Array.from({ length: budgetCase.sources },
				() => 'https://duplicate.example/sub');
		uci._data['liquid_formula.main.subscription_timeout'] = budgetCase.timeout;
		mod._enabledTemplateCount = budgetCase.enabled;
		for (const action of [ 'refresh', 'update' ]) {
			const expected = action === 'refresh' ? budgetCase.refresh : budgetCase.apply;
			let actual = 'invalid';
			try {
				actual = String(mod.actionWaitSeconds(action));
			} catch (error) {
				actual = 'invalid';
			}
			check(actual === expected,
				`frontend budget fixture ${budgetCase.name}/${action} matches ${expected}`);
		}
	}

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

async function exerciseTemplateRefreshContracts() {
	const initial = [
		{ id: 'alpha', enabled: true, name: 'Alpha', file: 'alpha.json', size: 1, mtime: '1' },
		{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' },
		{ id: 'retired', enabled: false, name: 'Retired', file: 'retired.json', size: 3, mtime: '3' }
	];
	const responses = [
		{ templates: initial },
		{ templates: [
			{ id: 'alpha', enabled: true, name: 'Alpha', file: 'alpha.json', size: 1, mtime: '1' },
			{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' },
			{ id: 'gamma', enabled: true, name: 'Gamma', file: 'gamma.json', size: 4, mtime: '4' },
			{ id: 'retired', enabled: false, name: 'Retired', file: 'retired.json', size: 3, mtime: '3' }
		] },
		{ templates: [
			{ id: 'alpha', enabled: true, name: 'Alpha renamed', file: 'alpha.json', size: 1, mtime: '5' },
			{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' },
			{ id: 'gamma', enabled: true, name: 'Gamma', file: 'gamma.json', size: 4, mtime: '4' }
		] },
		{ templates: [
			{ id: 'alpha', enabled: true, name: 'Alpha renamed', file: 'alpha.json', size: 1, mtime: '5' },
			{ id: 'beta', enabled: true, name: 'Beta', file: 'beta.json', size: 2, mtime: '2' }
		] },
		{ templates: [
			{ id: 'alpha', enabled: true, name: 'Alpha renamed', file: 'alpha.json', size: 1, mtime: '5' },
			{ id: 'beta', enabled: false, name: 'Beta', file: 'beta.json', size: 2, mtime: '6' }
		] },
		new Error('template list transport failed')
	];
	let listCalls = 0;
	const writeCalls = [];
	const runtime = installGlobals({
		status: {},
		list_templates() {
			listCalls++;
			const response = responses.shift();
			return response instanceof Error ? Promise.reject(response) : response;
		},
		write_template(id, name, file, noNode, enabled, content) {
			writeCalls.push({ id, name, file, noNode, enabled, content });
			return { ok: true, phase: 'complete', id: id, file: file };
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
	const defaultTemplate = map.sections.flatMap((section) => section.options)
		.find((option) => option.option === 'default_template');
	const select = runtime.listValues.default_template;
	const port = runtime.formInputs.port;
	check(select && select.value === 'alpha' &&
		listChoices(select).map((choice) => choice.value).join(',') === 'alpha,beta',
		'the rendered default-template ListValue excludes disabled choices initially');

	// Make a genuine rendered Overview field dirty. Refreshing templates must not
	// replace this live control or its UCI delta.
	port.value = '9816';
	port.dispatchEvent({ type: 'input' });
	const dirtyBeforeRefresh = JSON.stringify(uci._changes);
	const formEventsBeforeRefresh = runtime.formValueChanges.length;
	const mapsBeforeRefresh = mapRenderState();

	mod.uploadTemplate({ target: { files: [ { name: 'gamma.json', content: '{}' } ] } });
	await mod.saveTemplate();
	check(writeCalls.length === 1 && writeCalls[0].id === 'gamma' && writeCalls[0].enabled === true &&
		listCalls === 2 && templateRowIds().join(',') === 'alpha,beta,gamma,retired' &&
		listChoices(select).map((choice) => choice.value).join(',') === 'alpha,beta,gamma',
		'a successful uploaded enabled template save immediately adds it to the table and dropdown');
	check(select.value === 'alpha' && port.value === '9816' &&
		uci._data['liquid_formula.main.port'] === '9816' && runtime.listValueChanges.length === 0 &&
		JSON.stringify(uci._changes) === dirtyBeforeRefresh &&
		runtime.formValueChanges.length === formEventsBeforeRefresh && mapRenderState() === mapsBeforeRefresh,
		'uploading a template preserves the selected value and rendered dirty input without a full render or UCI changes');

	const renameDelta = JSON.stringify(uci._changes);
	const renameEvents = runtime.formValueChanges.length;
	const renameListEvents = runtime.listValueChanges.length;
	const renameMaps = mapRenderState();
	await mod.reloadTemplateList();
	check(listChoices(select).some((choice) => choice.value === 'alpha' && choice.label === 'Alpha renamed') &&
		templateRowIds().join(',') === 'alpha,beta,gamma',
		'a renamed template is immediately reflected in both rendered structures');
	check(port.value === '9816' && uci._data['liquid_formula.main.port'] === '9816' &&
		JSON.stringify(uci._changes) === renameDelta && runtime.formValueChanges.length === renameEvents &&
		runtime.listValueChanges.length === renameListEvents &&
		mapRenderState() === renameMaps,
		'renaming a template preserves the live dirty input without a synthetic delta or full render');

	const deleteDelta = JSON.stringify(uci._changes);
	const deleteEvents = runtime.formValueChanges.length;
	const deleteListEvents = runtime.listValueChanges.length;
	const deleteMaps = mapRenderState();
	await mod.reloadTemplateList();
	check(!listChoices(select).some((choice) => choice.value === 'gamma') &&
		!templateRowIds().includes('gamma'),
		'a deleted template is immediately removed from both rendered structures');
	check(port.value === '9816' && uci._data['liquid_formula.main.port'] === '9816' &&
		JSON.stringify(uci._changes) === deleteDelta && runtime.formValueChanges.length === deleteEvents &&
		runtime.listValueChanges.length === deleteListEvents &&
		mapRenderState() === deleteMaps,
		'deleting a template preserves the live dirty input without a synthetic delta or replacement map');

	select.value = 'beta';
	select.dispatchEvent({ type: 'change' });
	const dirtyUnavailableSelection = JSON.stringify(uci._changes);
	const unavailableEvents = runtime.formValueChanges.length;
	const unavailableListEvents = runtime.listValueChanges.length;
	const unavailableMaps = mapRenderState();
	const unavailableValidationCalls = defaultTemplate.validationCalls.length;
	await mod.reloadTemplateList();
	const betaChoice = listChoices(select).find((choice) => choice.value === 'beta');
	const unavailableParseRejected = await defaultTemplate.parse('main').then(
		() => false, () => true);
	check(select.value === 'beta' && betaChoice && betaChoice.disabled && betaChoice.hidden &&
		listChoices(select).filter((choice) => !choice.disabled).map((choice) => choice.value).join(',') === 'alpha' &&
		templateRowIds().join(',') === 'alpha,beta' && port.value === '9816' &&
		uci._data['liquid_formula.main.port'] === '9816' &&
		JSON.stringify(uci._changes) === dirtyUnavailableSelection &&
		runtime.formValueChanges.length === unavailableEvents &&
		runtime.listValueChanges.length === unavailableListEvents && mapRenderState() === unavailableMaps,
		'disabling an unsaved selection preserves it only as a hidden unavailable choice while updating the table');
	check(defaultTemplate && typeof defaultTemplate.validate === 'function' &&
		defaultTemplate.validate('main', 'beta') !== true &&
		defaultTemplate.validate('main', 'alpha') === true &&
		defaultTemplate.keylist.join(',') === 'alpha' &&
		defaultTemplate.vallist.join(',') === 'Alpha renamed' &&
		defaultTemplate.validationCalls.length === unavailableValidationCalls + 1 &&
		defaultTemplate.validationCalls[defaultTemplate.validationCalls.length - 1] === 'main' &&
		defaultTemplate.validState !== true &&
		unavailableParseRejected &&
		JSON.stringify(uci._changes) === dirtyUnavailableSelection,
		'an unavailable default-template selection is revalidated while the ListValue model keeps enabled choices only');

	const rowsBeforeFailedRefresh = templateRowIds().join(',');
	const choicesBeforeFailedRefresh = JSON.stringify(listChoices(select));
	const selectedBeforeFailedRefresh = select.value;
	const listChangesBeforeFailedRefresh = runtime.listValueChanges.length;
	const formEventsBeforeFailedRefresh = runtime.formValueChanges.length;
	const dirtyBeforeFailedRefresh = JSON.stringify(uci._changes);
	const mapsBeforeFailedRefresh = mapRenderState();
	document.getElementById('sbsc_tpl_id').value = 'alpha';
	document.getElementById('sbsc_tpl_name').value = 'Alpha renamed';
	document.getElementById('sbsc_tpl_file').value = 'alpha.json';
	document.getElementById('sbsc_tpl_content').value = '{}';
	await mod.saveTemplate();
	const saveStatus = document.getElementById('sbsc_tpl_save_status');
	check(templateRowIds().join(',') === rowsBeforeFailedRefresh &&
		JSON.stringify(listChoices(select)) === choicesBeforeFailedRefresh &&
		select.value === selectedBeforeFailedRefresh &&
		runtime.listValueChanges.length === listChangesBeforeFailedRefresh &&
		runtime.formValueChanges.length === formEventsBeforeFailedRefresh &&
		port.value === '9816' && uci._data['liquid_formula.main.port'] === '9816' &&
		JSON.stringify(uci._changes) === dirtyBeforeFailedRefresh &&
		mapRenderState() === mapsBeforeFailedRefresh &&
		saveStatus && /saved.*refresh/i.test(saveStatus.textContent) && saveStatus.style.color === '#b00',
		'a completed save with a rejected refresh leaves both structures untouched and reports a distinct refresh warning');
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
		config_error: payload,
		subscription: {
			schema: 1,
			overall_state: 'degraded',
			config_match: true,
			active_generation: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			total_sources: 2,
			fresh_count: 1,
			fallback_indices: [ 2 ],
			sources: [
			{ index: 1, result: 'fresh', fetch_code: 'ok', format: 'singbox-json',
			  accepted: 4, skipped: 0, warnings: [] },
			{ index: 2, result: 'fallback', fetch_code: 'http_status', format: 'clash-yaml',
			  accepted: 3, skipped: 1, warnings: [ {
				  code: 'invalid_field', node_index: 7, type: 'vmess', field: 'port'
			  } ] }
			],
			last_attempt: null
		}
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
	const subscriptionRoot = findNodes(statusRoot, (node) =>
		node.attributes && node.attributes.id === 'sbf_subscription_status')[0];
	const subscriptionTable = subscriptionRoot && findNodes(subscriptionRoot,
		(node) => node.tagName === 'TABLE')[0];
	const subscriptionRows = subscriptionTable && findNodes(subscriptionTable,
		(node) => node.tagName === 'TR' && node.classList.contains('cbi-section-table-row'));
	const degradedWarning = subscriptionRoot && findNodes(subscriptionRoot,
		(node) => node.classList && node.classList.contains('alert-message') &&
			node.classList.contains('warning'))[0];
	check(subscriptionRoot && degradedWarning &&
		/degraded|cached/i.test(degradedWarning.textContent) &&
		degradedWarning.textContent.includes('2'),
		'degraded subscription state renders as a source-indexed warning');
	check(subscriptionTable && hasClasses(subscriptionTable, [ 'table', 'cbi-section-table' ]) &&
		subscriptionRows.length === 2 &&
		subscriptionRows.every((row) => hasClasses(row, [ 'tr', 'cbi-section-table-row' ])) &&
		subscriptionRoot.textContent.includes('clash-yaml') &&
		subscriptionRoot.textContent.includes('3') && subscriptionRoot.textContent.includes('1'),
		'subscription status renders responsive per-source format and count rows');
	check(subscriptionRoot && subscriptionRoot.textContent.includes('invalid_field') &&
		subscriptionRoot.textContent.includes('vmess/port') &&
		findNodes(subscriptionRoot, (node) =>
			node.textContent.includes('invalid_field')).every((node) =>
			!String(node.innerHTML || '').includes('invalid_field')),
		'subscription diagnostics render only as literal text nodes');

	if (typeof mod.renderSubscriptionStatus !== 'function') {
		fail('complete subscription failure renders preservation details');
		fail('subscription status rejects unlisted diagnostic values and private fields');
	} else {
		const failedRoot = mod.renderSubscriptionStatus({
			overall_state: 'failed', config_match: true,
			active_generation: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			total_sources: 2, fresh_count: 1, fallback_indices: [ 2 ], sources: [],
			last_attempt: {
				state: 'failed', total_sources: 2, failure_stage: 'source_fetch',
				code: 'source_unavailable', fetch_code: 'http_status',
				source_index: 2, preserved: true
			}
		});
		check(failedRoot && /failed/i.test(failedRoot.textContent) &&
			/source_fetch/.test(failedRoot.textContent) &&
			failedRoot.textContent.includes('2') && /preserv/i.test(failedRoot.textContent),
			'complete subscription failure renders preservation details');

		const sensitive = 'https://private.example/sub?token=raw-secret';
		const privateRoot = mod.renderSubscriptionStatus({
			overall_state: 'failed', config_match: true,
			active_generation: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			total_sources: 1, fresh_count: 0, fallback_indices: [],
			url: sensitive, config_digest: sensitive, raw_error: sensitive,
			sources: [ {
				index: 1, result: sensitive, fetch_code: sensitive,
				format: sensitive, accepted: 0, skipped: 1, warnings: [ {
					code: sensitive, node_index: 1, type: sensitive, field: sensitive
				} ],
				node_name: sensitive, object_sha256: sensitive
			} ],
			last_attempt: {
				state: 'failed', total_sources: 1,
				failure_stage: sensitive, code: sensitive,
				fetch_code: sensitive, source_index: 1, preserved: true,
				raw_error: sensitive
			}
		});
		check(privateRoot && !privateRoot.textContent.includes(sensitive) &&
			!privateRoot.textContent.includes('raw-secret'),
			'subscription status rejects unlisted diagnostic values and private fields');
	}

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
			fixture.runtime.listValues.interface_mode.value = 'auto';
			await fixture.map.parse();
			check(JSON.stringify(uci.get(item.service, 'main', 'interface')) ===
			      JSON.stringify(fixture.manual),
				`${item.service} stock map.parse preserves manual devices while auto mode hides them`);
			fixture.runtime.listValues.interface_mode.value = 'selected';
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
	await exerciseSubscriptionTextValueContracts('24.10');
	await exerciseSubscriptionTextValueContracts('25.12');
	await exerciseOverviewContracts();
	await exerciseTemplateRefreshContracts();
	await exerciseOverviewRenderingSafety();
	await exerciseCustomLogoApplyContracts();
	await exerciseDpiWanModeContracts();

	console.log(`${checks} checks, ${failures} failures`);
	process.exit(failures ? 1 : 0);
}

main().catch((error) => {
	console.error(error);
	process.exit(1);
});
