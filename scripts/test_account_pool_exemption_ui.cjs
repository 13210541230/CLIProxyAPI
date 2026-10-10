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
assert.equal(context.borrowText('test'), '余量充足 · 均值 1.2');
snapshot = {borrowReady: true, borrowable: false, averageActive: 3};
assert.equal(context.borrowText('test'), '暂无余量 · 均值 3.0');
snapshot = {borrowReady: false};
assert.equal(context.borrowText('test'), '观察中（60 秒）');
limit = 0;
assert.equal(context.borrowText('test'), '无可用余量');
member = false;
assert.equal(context.borrowText('test'), '不适用');
assert.equal(context.borrowText('not-a-member'), '不适用');
assert.doesNotMatch(html, /跨池|豁免|data-ap-cross-pool|apSetCrossPoolExemption/);
const buildMatch = html.match(/const apBuild = \(mutate\) => \{[^\n]+\};/);
assert.ok(buildMatch, 'full snapshot builder must exist');
const fullHash = 'a'.repeat(64);
context.state.ap.policy = {version: 4, pools: [], members: [], bindings: [{apiKeyHash: fullHash, poolId: 'home', crossPoolExempt: true}]};
vm.runInContext(buildMatch[0] + '\nthis.buildPolicy = apBuild;', context);
const next = context.buildPolicy((p) => { p.pools.push({id: 'new', enabled: true}); });
assert.equal(next.bindings[0].crossPoolExempt, true, 'ordinary policy edits must preserve backend exemption');
assert.equal(next.bindings[0].apiKeyHash, fullHash);
assert.equal(next.version, 5);
assert.equal(context.state.ap.policy.version, 4, 'builder must not mutate current policy');
console.log('PASS: embedded JS, generic capacity diagnostics, no visible exemption controls, preserved backend flags');
