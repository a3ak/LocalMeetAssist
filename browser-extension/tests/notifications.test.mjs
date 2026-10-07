import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeApi, setup, handshake, config } from './helpers.mjs';
const state = { state: 'awaiting_confirmation', owner: true, prompt_id: 'test-prompt' };
const apiWithMeeting = () => fakeApi({ tabs: [{ id: 1, url: 'https://meet.google.com/test' }] });

test('waiting badge has yellow background and dark text; Chrome notification requests high priority', async () => {
  const ctx = await setup({ api: apiWithMeeting() }); await handshake(ctx, config({ mode: 'notify' }), state);
  const badge = ctx.api.badges.at(-1), notification = ctx.api.deliveries.at(-1).options;
  assert.equal(badge.text, '?'); assert.equal(badge.color, '#facc15'); assert.equal(badge.textColor, '#111827');
  assert.equal(notification.priority, 2); assert.equal(notification.requireInteraction, true); assert.equal(notification.silent, false);
});

test('notification failures stay visible after reload and expose no raw exception or URL', async () => {
  const api = apiWithMeeting();
  api.createNotification = async () => { throw new Error('lma_pl_private https://meet.google.com/secret'); };
  const ctx = await setup({ api }); await handshake(ctx, config({ mode: 'notify' }), state);
  assert.equal(ctx.app.publicStatus().notificationStatus, 'failed'); assert.equal(ctx.app.publicStatus().canAnswer, true);
  await ctx.app.reload(); const socket = ctx.sockets.at(-1); socket.open();
  await handshake({ ...ctx, socket }, config({ mode: 'notify' }), state);
  assert.equal(ctx.app.publicStatus().notificationStatus, 'failed');
  assert(!JSON.stringify(ctx.app.publicStatus()).includes('private')); assert(!JSON.stringify(ctx.app.publicStatus()).includes('secret'));
});

test('denied notifications preserve the popup question and never attempt a native notification', async () => {
  const api = apiWithMeeting(); api.notificationPermission = async () => 'denied';
  const ctx = await setup({ api }); await handshake(ctx, config({ mode: 'notify' }), state);
  assert.equal(api.deliveries.length, 0); assert.equal(ctx.app.publicStatus().notificationStatus, 'denied');
  assert.equal(ctx.app.publicStatus().canAnswer, true); assert.equal(api.badges.at(-1).color, '#facc15');
});

test('test notification never starts or declines a recording', async () => {
  const ctx = await setup(); await handshake(ctx);
  const count = ctx.socket.sent.length;
  const response = await ctx.app.uiMessage({ kind: 'test-notification' });
  assert.equal(response.ok, true); assert.equal(ctx.socket.sent.length, count);
  assert.equal(ctx.api.deliveries.at(-1).id, 'lma-test'); assert(!ctx.api.deliveries.at(-1).options.buttons);
});

test('Firefox uses only supported basic notification fields for question and test', async () => {
  const api = fakeApi({ firefox: true, tabs: [{ id: 1, url: 'https://meet.google.com/test' }] }); const ctx = await setup({ api });
  await handshake(ctx, config({ mode: 'notify' }), state);
  await ctx.app.uiMessage({ kind: 'test-notification' });
  for (const delivery of api.deliveries) assert.deepEqual(Object.keys(delivery.options).sort(), ['iconUrl', 'message', 'title', 'type']);
});
