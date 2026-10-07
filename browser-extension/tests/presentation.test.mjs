import test from 'node:test';
import assert from 'node:assert/strict';
import { setup, handshake, config, settle, serverState } from './helpers.mjs';

test('notify explains the question and stays unrecorded until the server confirms recording', async () => {
  const ctx = await setup();
  ctx.api.tabs = [{ url: 'https://meet.google.com/demo' }];
  await handshake(ctx, config({ mode: 'notify' }), { state: 'awaiting_confirmation', owner: true, prompt_id: 'question' });
  const waiting = ctx.api.badges.at(-1);
  assert.equal(waiting.text, '?');
  assert.equal(waiting.recording, false);
  assert.match(waiting.title, /Начать запись встречи\? Нажмите значок/);
  assert.equal(ctx.api.deliveries.at(-1).options.buttons[1].title, 'Не записывать');
  assert.equal((await ctx.app.answer('confirm', 'question')).ok, true);
  assert.equal(ctx.api.badges.at(-1).recording, false);
  ctx.socket.message(serverState({ state: 'recording', owner: true, recording_revision: 4 }));
  await settle();
  assert.equal(ctx.api.badges.at(-1).recording, true);
  assert.equal(ctx.api.badges.at(-1).text, '');
  assert.match(ctx.api.badges.at(-1).title, /Идёт запись встречи/);
});

test('global recording indicator includes application and other-profile recordings and becomes unknown offline', async () => {
  const ctx = await setup();
  ctx.api.tabs = [{ url: 'https://meet.google.com/demo' }];
  await handshake(ctx, config(), { state: 'recording', owner: true });
  assert.equal(ctx.api.badges.at(-1).recording, true);
  let revision=1;
  for (const [state, owner, expected] of [['recording', true, true], ['idle', false, false], ['recording', false, true]]) {
    ctx.socket.message(serverState({ state, owner, recording_revision: ++revision }));
    await settle();
    assert.equal(ctx.api.badges.at(-1).recording, expected);
  }
  assert.equal(ctx.app.publicStatus().recordingActive, true);
  ctx.socket.message(serverState({ state: 'recording', owner: true, recording_revision: 4 }));
  await settle();
  ctx.socket.close(1006);
  await settle();
  assert.equal(ctx.api.badges.at(-1).recording, false);
  assert.equal(ctx.api.badges.at(-1).text, '!');
  assert.match(ctx.api.badges.at(-1).title, /неизвестно/);
});

test('a configuration update refreshes the mode tooltip without waiting for another state', async () => {
  const ctx = await setup();
  ctx.api.tabs = [{ url: 'https://meet.google.com/demo' }];
  await handshake(ctx, config(), { state: 'idle', owner: false });
  assert.match(ctx.api.badges.at(-1).title, /Автоматическая запись/);
  ctx.socket.message(config({ mode: 'notify' }));
  await settle();
  assert.match(ctx.api.badges.at(-1).title, /Спрашивать перед записью/);
  ctx.socket.message(config({ enabled: false, mode: 'notify' }));
  await settle();
  assert.match(ctx.api.badges.at(-1).title, /Интеграция выключена/);
  assert.equal(ctx.api.badges.at(-1).text, '');
});
