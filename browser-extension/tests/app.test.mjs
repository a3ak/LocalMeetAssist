import { test } from 'node:test';
import assert from 'node:assert/strict';
import { core, fakeApi, setup, config, handshake, settle, tabsMessages, serverState } from './helpers.mjs';
const meeting = 'https://meet.google.com/abc-defg-hij';
test('hello first; never sends a snapshot until config, seq is per connection', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: meeting }] }) });
  assert.deepEqual(ctx.socket.sent.map(m => m.type), ['hello']);
  assert(!ctx.socket.url.includes('lma_pl'));
  await handshake(ctx);
  assert.equal(tabsMessages(ctx.socket)[0].seq, 1);
  assert.deepEqual(tabsMessages(ctx.socket)[0].urls, [meeting]);
  ctx.app.client.stop();
});
test('active state preceding ack prevents short-connection closure', async () => {
  const ctx = await setup();
  await handshake(ctx);
  ctx.socket.message(serverState({ type: 'state', state: 'recording', owner: true }));
  ctx.socket.message({ type: 'ack', seq: 1 });
  await settle();
  assert.equal(ctx.socket.closeCount, 0);
  ctx.app.client.stop();
});
test('idle connection stays open to receive recording state from the application', async () => {
  const ctx = await setup();
  await handshake(ctx);
  assert.equal(ctx.socket.closeCount, 0);
  ctx.socket.message({ type: 'ack', seq: 1 });
  await settle();
  assert.equal(ctx.socket.closeCount, 0);
  assert.equal(ctx.app.status, 'online');
});
test('SPA navigation to nonmatching URL sends empty, ask resends with next seq', async () => {
  const api = fakeApi({ tabs: [{ url: meeting }] });
  const ctx = await setup({ api });
  await handshake(ctx);
  api.tabs = [{ url: 'https://example.test/' }];
  await ctx.app.tabEvent();
  ctx.socket.message({ type: 'ask', what: 'tabs', owner: ctx.app.state.owner, recording_active:ctx.app.control.data.active,recording_revision:ctx.app.control.data.revision });
  await settle();
  assert(tabsMessages(ctx.socket).every((m,i)=>m.seq===i+1));
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, []);
  ctx.app.client.stop();
});
test('config changes recalculate already-open tabs without navigation', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: 'https://zoom.us/wc/test' }] }) });
  await handshake(ctx);
  ctx.socket.message(config({ patterns: ['*zoom.us/*'] }));
  await settle();
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, ['https://zoom.us/wc/test']);
  ctx.app.client.stop();
});
test('a tab opening while idle handshake is in flight keeps socket open', async () => {
  const api = fakeApi();
  const ctx = await setup({ api });
  await handshake(ctx);
  api.tabs = [{ url: meeting }];
  ctx.socket.message({ type: 'ack', seq: 1 });
  await settle();
  assert.equal(ctx.socket.closeCount, 0);
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, [meeting]);
  ctx.app.client.stop();
});
test('notification is delivered once per prompt, including worker recovery', async () => {
  const api = fakeApi({ tabs: [{ url: meeting }] });
  const ctx = await setup({ api });
  const state = { state: 'awaiting_confirmation', owner: true, prompt_id: 'p-one' };
  await handshake(ctx, config({ mode: 'notify' }), state);
  assert.equal(api.deliveries.length, 1);
  assert.equal(api.deliveries[0].options.buttons.length, 2);
  await ctx.app.reload();
  const socket = ctx.sockets.at(-1); socket.open();
  await handshake({ ...ctx, socket }, config({ mode: 'notify' }), state);
  assert.equal(api.deliveries.length, 1);
  assert.equal(ctx.app.publicStatus().canAnswer, true);
  ctx.app.client.stop();
});
test('Firefox notification omits unsupported fields, owner false shows no prompt', async () => {
  const api = fakeApi({ firefox: true, tabs: [{ url: meeting }] });
  const ctx = await setup({ api });
  await handshake(ctx, config({ mode: 'notify' }), { state: 'idle', owner: false });
  assert.equal(api.deliveries.length, 0);
  assert.equal(ctx.app.publicStatus().promptId, null);
  ctx.socket.message(serverState({ type: 'state', state: 'awaiting_confirmation', owner: true, prompt_id: 'mine' }));
  await settle();
  assert(!('buttons' in api.deliveries[0].options));
  assert(!('requireInteraction' in api.deliveries[0].options));
  ctx.app.client.stop();
});
test('stale confirmation rejected; current confirmation includes only prompt_id', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: meeting }] }) });
  await handshake(ctx, config({ mode: 'notify' }), { state: 'awaiting_confirmation', owner: true, prompt_id: 'p1' });
  assert.equal((await ctx.app.answer('confirm', 'old')).ok, false);
  assert.equal((await ctx.app.answer('confirm', 'p1')).ok, true);
  assert.deepEqual(ctx.socket.sent.at(-1), { type: 'confirm', prompt_id: 'p1' });
  assert.equal(ctx.api.store.shownPrompt, 'p1');
  ctx.app.client.stop();
});
test('late click after closing the last tab sends empty snapshot and no confirm', async () => {
  const api = fakeApi({ tabs: [{ url: meeting }] });
  const ctx = await setup({ api });
  await handshake(ctx, config({ mode: 'notify' }), { state: 'awaiting_confirmation', owner: true, prompt_id: 'p1' });
  api.tabs = [];
  assert.equal((await ctx.app.answer('confirm', 'p1')).ok, false);
  assert(!ctx.socket.sent.some(m => m.type === 'confirm'));
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls, []);
  ctx.app.client.stop();
});
test('ending a prompt clears notification without a legacy blocked state', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: meeting }] }) });
  await handshake(ctx, config({ mode: 'notify' }), { state: 'awaiting_confirmation', owner: true, prompt_id: 'p1' });
  ctx.socket.message(serverState({ type: 'state', state: 'idle', owner: false }));
  ctx.socket.message({ type: 'ask', what: 'tabs', owner: ctx.app.state.owner, recording_active:ctx.app.control.data.active,recording_revision:ctx.app.control.data.revision });
  await settle();
  assert.equal(Object.keys(ctx.api.notifications).length, 0);
  assert(!ctx.socket.sent.some(m => m.type === 'confirm'));
  ctx.app.client.stop();
});
test('forbidden stops retries and persists across background restart', async () => {
  const ctx = await setup();
  ctx.socket.message({ type: 'error', error: 'forbidden' });
  await settle();
  assert.equal(ctx.api.store.authBlocked, true);
  await ctx.app.alarm();
  await ctx.app.tabEvent();
  await ctx.timers.advance(60000);
  assert.equal(ctx.sockets.length, 1);
  await ctx.app.reload();
  assert.equal(ctx.app.status, 'forbidden');
  assert.equal(ctx.sockets.length, 1);
});
test('close 1008 also halts retries', async () => {
  const ctx = await setup();
  ctx.socket.close(1008);
  await settle();
  assert.equal(ctx.api.store.authBlocked, true);
  assert.equal(ctx.app.status, 'forbidden');
});
test('changing settings isolates old socket callbacks and clears auth block', async () => {
  const ctx = await setup();
  const old = ctx.socket;
  ctx.api.store.authBlocked = true;
  assert.equal(await ctx.app.store.save(52500, 'lma_pl_replacement_test'), true);
  await ctx.app.reload();
  old.message({ type: 'error', error: 'forbidden' });
  old.onclose?.({ code: 1008 });
  await settle();
  assert.equal(ctx.api.store.authBlocked, false);
  assert.equal(ctx.sockets.at(-1).url, 'ws://127.0.0.1:52500/api/v1/ws');
  ctx.app.client.stop();
});
test('saving unchanged settings cannot bypass forbidden', async () => {
  const api = fakeApi({ data: { authBlocked: true } });
  const store = new core.SettingsStore(api, () => 'id');
  assert.equal(await store.save(52469, api.store.settings.token), false);
  assert.equal(api.store.authBlocked, true);
});
test('malformed JSON recovers without leaking payload into public status', async () => {
  const ctx = await setup();
  ctx.socket.message('{"token":"secret-test"');
  await settle();
  assert.equal(ctx.app.status, 'protocol_error');
  assert(!JSON.stringify(ctx.app.publicStatus()).includes('secret-test'));
  ctx.app.client.stop();
});
test('lost ack triggers transport recovery, no recording commands are synthesized', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: meeting }] }) });
  await handshake(ctx, config(), { state: 'recording', owner: true });
  await ctx.timers.advance(46000);
  assert(ctx.sockets.length >= 2);
  assert(ctx.socket.sent.every(m => ['hello', 'tabs'].includes(m.type)));
  ctx.app.client.stop();
});
test('reconnection sends a fresh snapshot, install id remains stable', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: meeting }] }) });
  await handshake(ctx, config(), { state: 'recording', owner: true });
  const id = ctx.app.clientId;
  ctx.socket.close();
  await settle();
  await ctx.app.alarm();
  const socket = ctx.sockets.at(-1); socket.open();
  assert.deepEqual(socket.sent.map(m => m.type), ['hello']);
  await handshake({ ...ctx, socket }, config(), { state: 'recording', owner: true });
  assert.equal(ctx.app.clientId, id);
  assert.equal(tabsMessages(socket)[0].seq, 1);
  ctx.app.client.stop();
});
test('public status exposes matching URLs only to extension UI, never the token', async () => {
  const ctx = await setup({ api: fakeApi({ tabs: [{ url: meeting }] }) });
  await handshake(ctx);
  const status = JSON.stringify(ctx.app.publicStatus());
  assert(!status.includes('lma_pl_'));
  assert(status.includes(meeting));
  ctx.app.client.stop();
});
