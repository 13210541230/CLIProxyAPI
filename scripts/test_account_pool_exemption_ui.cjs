const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, '../plugins/enterprise-access-audit/go/internal/management/ui/index.html'), 'utf8');
for (const match of html.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/g)) new Function(match[1]);
const match = html.match(/const apBorrowText = \(authId\) => \{[\s\S]*?\n      \};/);
assert.ok(match, 'borrow diagnostic formatter must exist');
let enabled = false;
let limit = 5;
let member = true;
let snapshot = {borrowReady: true, borrowable: true, averageActive: 1.2};
const context = vm.createContext({
  state: {ap: {policy: {members: [{authId: 'test', poolId: 'pool', enabled: true}]}}},
  apSchedulerState: () => ({runtime: enabled}),
  apPools: () => [{id: 'pool', enabled: member}],
  apLimitFor: () => ({limit}),
  apStateFor: () => snapshot,
});
vm.runInContext(match[0] + '\nthis.borrowText = apBorrowText;', context);
assert.equal(context.borrowText('test'), '账号池未启用');
enabled = true;
assert.equal(context.borrowText('test'), '可借用 · 均值 1.2');
snapshot = {borrowReady: true, borrowable: false, averageActive: 3};
assert.equal(context.borrowText('test'), '暂不可借 · 均值 3.0');
snapshot = {borrowReady: false};
assert.equal(context.borrowText('test'), '观察中（60 秒）');
limit = 0;
assert.equal(context.borrowText('test'), '无可借用余量');
member = false;
assert.equal(context.borrowText('test'), '不适用');
assert.equal(context.borrowText('not-a-member'), '不适用');
console.log('PASS: embedded JS syntax and executed borrowing diagnostic states');
