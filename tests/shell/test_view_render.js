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

function makeElement(tag, attrs, children) {
	const node = {
		tagName: String(tag || 'div').toUpperCase(),
		attributes: attrs && typeof attrs === 'object' ? attrs : {},
		childNodes: [],
		innerHTML: '',
		style: {},
		classList: { add() {}, remove() {}, contains() { return false; } },
		appendChild(child) { this.childNodes.push(child); return child; },
		addEventListener() {},
		setAttribute() {},
		querySelector() { return null; },
		querySelectorAll() { return []; }
	};
	const kids = (children !== undefined) ? children
		: (Array.isArray(attrs) || typeof attrs === 'string' ? attrs : undefined);
	if (Array.isArray(kids))
		kids.forEach(function(k) { if (k) node.childNodes.push(k); });
	else if (kids)
		node.childNodes.push(kids);
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
		env: { cgi_base: '/cgi-bin', requestpath: [], sessionid: 'x' },
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
		Widget.extend = function(spec) {
			class Derived extends Widget {}
			Object.assign(Derived.prototype, spec || {});
			Derived.extend = Widget.extend;
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
		save() { return Promise.resolve(); }
	};

	global.rpc = {
		declare(spec) {
			return function() {
				const canned = rpcResponses[spec.method];
				return Promise.resolve(canned === undefined ? {} : canned);
			};
		}
	};

	global.ui = {
		addNotification() {},
		showModal() {},
		hideModal() {},
		createHandlerFn(ctx, fn) { return fn; },
		changes: { apply() { return Promise.resolve(); } },
		Combobox: class {},
		Textfield: class {}
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

	console.log(`${checks} checks, ${failures} failures`);
	process.exit(failures ? 1 : 0);
}

main().catch((error) => {
	console.error(error);
	process.exit(1);
});
