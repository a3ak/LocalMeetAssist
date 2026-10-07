import test from 'node:test';
import assert from 'node:assert/strict';
import { core, fakeApi, setup, handshake, config, settle, tabsMessages, FakeTimers, FakeSocket } from './helpers.mjs';
const meeting = 'https://meet.google.com/new-meeting';
const defaults = () => fakeApi({ history: false, detectionDelaySeconds: 3 });
async function visit(ctx, id = 1, url = meeting) {
  ctx.api.tabs.push({ id, url });
  await ctx.app.tabEvent({ kind: 'created', id, url, at: ctx.app.now() });
}

test('fresh installation ignores all existing pages and F5 cannot make them new', async () => {
  const api = fakeApi({ history: false, detectionDelaySeconds: 3, tabs: [{ id: 1, url: meeting }] });
  const ctx = await setup({ api }); await handshake(ctx);
  assert.deepEqual(tabsMessages(ctx.socket)[0].urls, []);
  assert.equal(ctx.app.publicStatus().ignoredCount, 1);
  await ctx.app.tabEvent({ kind: 'navigation', id: 1, url: meeting, at: ctx.app.now() });
  await ctx.timers.advance(3001); await ctx.app.snapshot(true);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, []);
  assert.equal(ctx.app.pendingCount, 0);
});

test('new page waits three seconds; pending detection prevents idle socket closure', async () => {
  const ctx = await setup({ api: defaults() }); await handshake(ctx);
  await visit(ctx);
  assert.equal(ctx.app.pendingCount, 1);
  ctx.socket.message({ type: 'ack', seq: 1 }); await settle();
  assert.equal(ctx.socket.closeCount, 0);
  await ctx.timers.advance(2999);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, []);
  await ctx.timers.advance(1);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
  assert.equal(ctx.app.pendingCount, 0);
});

test('closing a page during delay cancels detection without reporting a meeting', async () => {
  const ctx = await setup({ api: defaults() }); await handshake(ctx); await visit(ctx);
  await ctx.timers.advance(1000);
  ctx.api.tabs = []; await ctx.app.tabEvent({ kind: 'removed', id: 1 });
  await ctx.timers.advance(4000);
  assert(tabsMessages(ctx.socket).every(message => message.urls.length === 0));
  assert.equal(ctx.app.pendingCount, 0);
  assert.equal(ctx.api.scanDeadlines.at(-1), null);
});

test('navigation away during delay cancels detection', async () => {
  const ctx = await setup({ api: defaults() }); await handshake(ctx); await visit(ctx);
  ctx.api.tabs[0].url = 'https://example.org/';
  await ctx.app.tabEvent({ kind: 'navigation', id: 1, url: ctx.api.tabs[0].url, at: ctx.app.now() });
  await ctx.timers.advance(4000);
  assert(tabsMessages(ctx.socket).every(message => message.urls.length === 0));
});

test('duplicate navigation and F5 do not extend the three-second deadline', async () => {
  const ctx = await setup({ api: defaults() }); await handshake(ctx); await visit(ctx);
  await ctx.timers.advance(2000);
  await ctx.app.tabEvent({ kind: 'navigation', id: 1, url: meeting, at: ctx.app.now() });
  await ctx.timers.advance(1000);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
});

test('an old tab can enter a new meeting through an SPA navigation', async () => {
  const api = fakeApi({ history: false, detectionDelaySeconds: 3, tabs: [{ id: 1, url: 'https://meet.google.com/old' }] });
  const ctx = await setup({ api }); await handshake(ctx);
  api.tabs[0].url = meeting;
  await ctx.app.tabEvent({ kind: 'navigation', id: 1, url: meeting, at: ctx.app.now() });
  await ctx.timers.advance(3000);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
});

test('an observed but unadmitted page older than 180 seconds is ignored on connection', async () => {
  let clock = Date.now();
  const ctx = await setup({ api: defaults(), now: () => clock });
  await visit(ctx);
  clock += 180001;
  await handshake(ctx);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, []);
  assert.equal(ctx.app.ignoredCount, 1);
});

test('a page at the configured age boundary is accepted with zero delay', async () => {
  let clock = Date.now();
  const api = fakeApi({ history: false, detectionDelaySeconds: 0 });
  api.store.settings.maxAgeSeconds = 30;
  const ctx = await setup({ api, now: () => clock }); await visit(ctx);
  clock += 30000; await handshake(ctx);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
});

test('admitted page stays active beyond max age and survives worker recreation', async () => {
  let clock = Date.now();
  const api = fakeApi({ tabs: [{ id: 1, url: meeting }] });
  const ctx = await setup({ api, now: () => clock });
  await handshake(ctx, config(), { state: 'recording', owner: true });
  clock += 301000; await ctx.app.snapshot(true);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
  ctx.app.client.stop();
  const recovered = await setup({ api, now: () => clock });
  await handshake(recovered, config(), { state: 'recording', owner: true });
  assert.deepEqual(tabsMessages(recovered.socket)[0].urls, [meeting]);
  assert.equal(recovered.app.ignoredCount, 0);
});

test('worker recreation resumes the remaining delay instead of starting another three seconds', async () => {
  const ctx = await setup({ api: defaults() }); await handshake(ctx); await visit(ctx);
  await ctx.timers.advance(2000); ctx.app.client.stop();
  const timers = new FakeTimers(), sockets = [];
  const app = new core.ExtensionApp(ctx.api, { timers, now: () => ctx.app.now() + timers.now,
    socketFactory: url => { const socket = new FakeSocket(url); sockets.push(socket); return socket; } });
  await app.reload(); const socket = sockets.at(-1); socket.open(); await handshake({ app, socket });
  assert.equal(app.pendingCount, 1);
  await timers.advance(999); assert.deepEqual(tabsMessages(socket).at(-1).urls, []);
  await timers.advance(1); assert.deepEqual(tabsMessages(socket).at(-1).urls, [meeting]);
});

test('a new browser session discards previous ages and admissions', async () => {
  const api = fakeApi({ tabs: [{ id: 1, url: meeting }] });
  const ctx = await setup({ api }); await handshake(ctx);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]); ctx.app.client.stop();
  delete api.session.tabHistory;
  const restart = await setup({ api }); await handshake(restart);
  assert.deepEqual(tabsMessages(restart.socket).at(-1).urls, []);
});

test('settings use 180/3 defaults and enforce integer ranges including 0 and 30 delay', () => {
  assert.equal(core.validateSettings(52469, 'lma_pl_test').detectionDelaySeconds, 3);
  assert.equal(core.validateSettings(52469, 'lma_pl_test').maxAgeSeconds, 180);
  for (const age of [29, 301, 50.5, '3e2', null]) assert.throws(() => core.validateSettings(52469, 'lma_pl_test', age, 3));
  for (const delay of [-1, 31, 2.5, null]) assert.throws(() => core.validateSettings(52469, 'lma_pl_test', 180, delay));
  for (const age of [30, 300]) for (const delay of [0, 30]) assert.equal(core.validateSettings(52469, 'lma_pl_test', age, delay).detectionDelaySeconds, delay);
});

test('changing only detection settings cannot clear a rejected token', async () => {
  const api = fakeApi({ data: { authBlocked: true } });
  const store = new core.SettingsStore(api);
  assert.equal(await store.save(52469, api.store.settings.token, 150, 5), true);
  assert.equal(api.store.authBlocked, true);
  assert.equal(api.store.settings.maxAgeSeconds, 150);
});

test('a 30-second delay rechecks at 20 seconds then admits at the original deadline', async () => {
  const api = fakeApi({ history: false, detectionDelaySeconds: 30 });
  const ctx = await setup({ api }); await handshake(ctx); await visit(ctx);
  ctx.socket.message({ type: 'ack', seq: 1 }); await settle();
  await ctx.timers.advance(20000); assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, []);
  await ctx.timers.advance(10000); assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
});

test('an old server prompt does not notify or show a question for an ignored startup page', async () => {
  const api = fakeApi({ history: false, tabs: [{ id: 1, url: meeting }] });
  const ctx = await setup({ api });
  await handshake(ctx, config({ mode: 'notify' }), { state: 'awaiting_confirmation', owner: true, prompt_id: 'recovered-old' });
  assert.equal(api.deliveries.length, 0);
  assert.equal(ctx.app.publicStatus().promptId, null);
  assert.equal(ctx.app.publicStatus().canAnswer, false);
  assert.equal(api.badges.at(-1).text, '');
});
