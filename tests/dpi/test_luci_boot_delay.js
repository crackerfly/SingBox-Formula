'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '../..');
const views = [
	{
		name: 'FakeSIP',
		file: path.join(root, 'openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/fakesip.js'),
		defaultValue: '40',
		nextOption: "form.MultiValue, 'interface'"
	},
	{
		name: 'FakeHTTP',
		file: path.join(root, 'openwrt-feed/luci-app-liquid-formula/root/www/luci-static/resources/view/liquid-formula/fakehttp.js'),
		defaultValue: '60',
		nextOption: "form.ListValue, 'interface_mode'"
	}
];

const accepted = [ '0', '1', '40', '60', '600' ];
const rejected = [ '', '-1', '-0', '+1', '00', '01', '1.0', '600.0', ' 1', '1 ', '601', 'abc' ];

for (const view of views) {
	const source = fs.readFileSync(view.file, 'utf8');
	const match = source.match(/function validInteger\(value, minimum, maximum\) \{[\s\S]*?\n\}/);
	assert(match, `${view.name}: validInteger() is missing`);
	const validInteger = Function(`return (${match[0]});`)();

	for (const value of accepted)
		assert.strictEqual(validInteger(value, 0, 600), true, `${view.name}: rejected ${value}`);
	for (const value of rejected)
		assert.strictEqual(validInteger(value, 0, 600), false, `${view.name}: accepted ${JSON.stringify(value)}`);

	const enabledAt = source.indexOf("form.Flag, 'enabled'");
	const nextAt = source.indexOf(view.nextOption, enabledAt);
	assert(enabledAt >= 0 && nextAt > enabledAt, `${view.name}: unable to locate basic option order`);
	const block = source.slice(enabledAt, nextAt);

	assert(block.includes("form.Value, 'boot_delay'"), `${view.name}: boot_delay must follow Enable`);
	assert(block.includes(`o.default = '${view.defaultValue}';`), `${view.name}: wrong boot_delay default`);
	assert(block.includes('o.rmempty = false;'), `${view.name}: boot_delay must be required`);
	assert(block.includes('validInteger(value, 0, 600)'), `${view.name}: wrong boot_delay range`);
	assert(block.includes('Only delays automatic service startup during router boot.'), `${view.name}: boot-only help is missing`);
	assert(block.includes('takes effect immediately.'), `${view.name}: manual-immediacy help is missing`);
}

console.log('LuCI boot delay tests: ok');
