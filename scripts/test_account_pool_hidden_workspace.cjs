const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const html = fs.readFileSync(path.join(__dirname, '../plugins/enterprise-access-audit/go/internal/management/ui/index.html'), 'utf8');
const source = html.match(/<script[^>]*>([\s\S]*?)<\/script>/)[1];
new Function(source);
const extract = (name) => {
  const found = source.match(new RegExp(`const ${name} = [^\\n]*=> \\{[\\s\\S]*?\\n      \\};`));
  assert.ok(found, name);
  return found[0];
};
const fullHash = 'a'.repeat(64), otherHash = 'b'.repeat(64);
let publishes = 0;
const state = {ap: {policy: {version: 1, pools: [{id: 'home', name: 'Home', enabled: true}], members: [{authId: 'a', poolId: 'home', enabled: true}], bindings: [{apiKeyHash: fullHash, poolId: 'home'}, {apiKeyHash: otherHash, poolId: 'home', crossPoolExempt: true}]}, status: {version: 1}, busy: false, exemptionQuery: '', apError: ''}, metadataError: ''};
const context = vm.createContext({
  state, exemptionWorkspace: true,
  normalizeKeyHash: (value) => String(value).trim().toLowerCase(),
  escapeHTML: (value) => String(value ?? '').replace(/[&<>"]/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c])),
  maskHash: value => value.slice(0, 8) + '…' + value.slice(-4),
  apMetaForHash: hash => hash === fullHash ? {username: '<User>', department: 'Dev', email: 'a@synthetic.test'} : null,
  apUserLabel: meta => meta || {username: '未关联用户', department: '未关联部门', email: '未填写邮箱'},
  apPools: () => state.ap.policy.pools,
  showToast: () => {}, render: () => {},
  apPublish: async policy => { publishes++; state.ap.policy = policy; },
});
const build = source.match(/const apBuild = \(mutate\) => \{[^\n]+\};/)[0];
vm.runInContext([build, extract('apSetExemption'), extract('renderExemptionWorkspace'), 'this.qa={set:apSetExemption,render:renderExemptionWorkspace};'].join('\n'), context);
(async () => {
  const initial = context.qa.render();
  assert.match(initial, /data-exemption-toggle/);
  assert.match(initial, /&lt;User&gt;/);
  assert.doesNotMatch(initial, /<User>/);
  assert.match(initial, /未关联用户/);
  assert.match(initial, new RegExp(`data-hash="${fullHash}"`));
  await context.qa.set(fullHash, true);
  assert.equal(state.ap.policy.version, 2);
  assert.equal(state.ap.policy.bindings[0].crossPoolExempt, true);
  assert.equal(state.ap.policy.bindings[0].apiKeyHash, fullHash);
  assert.equal(state.ap.policy.bindings[1].crossPoolExempt, true);
  assert.equal(state.ap.policy.members[0].authId, 'a');
  await context.qa.set(fullHash, false);
  assert.equal(state.ap.policy.bindings[0].crossPoolExempt, undefined);
  assert.equal(state.ap.policy.bindings[1].crossPoolExempt, true);
  state.ap.busy = true;
  await context.qa.set(fullHash, true);
  assert.equal(publishes, 2);
  state.ap.busy = false;
  context.exemptionWorkspace = false;
  await context.qa.set(fullHash, true);
  assert.equal(publishes, 2, 'normal view must not trigger hidden actions');
  context.exemptionWorkspace = true;
  await context.qa.set('not-bound', true);
  assert.equal(publishes, 2, 'hidden workspace must not synthesize bindings');
  assert.equal(state.ap.policy.bindings.length, 2);
  state.ap.exemptionQuery = 'no match';
  assert.match(context.qa.render(), /data-exemption-row[^>]* hidden/);
  assert.doesNotMatch(context.qa.render(), /data-exemption-empty hidden/);
  // Use the real full-snapshot publisher to prove rejection leaves policy intact.
  const old = state.ap.policy;
  const app = {innerHTML: ''};
  const toast = {classList: {add() {}, remove() {}}};
  context.$ = selector => selector === '#toast' ? toast : app;
  context.AP = '/v0/management/enterprise-access-audit/account-pool';
  context.callHost = async () => { throw new Error('conflicting policy version'); };
  context.apLoad = async () => {};
  vm.runInContext(extract('apPublish') + '\nthis.apPublish=apPublish;', context);
  await context.qa.set(fullHash, true);
  assert.equal(state.ap.policy, old, 'failed save must not mutate saved policy');
  assert.equal(state.ap.busy, false);
  assert.equal(state.ap.apError, 'conflicting policy version');
  console.log('PASS: hidden full-hash rows/search/escaping, exact toggle ownership, normal-view guard, rejection rollback');
})().catch(error => { console.error(error); process.exitCode = 1; });
