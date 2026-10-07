import { test } from 'node:test';
import assert from 'node:assert/strict';
import { core, config } from './helpers.mjs';
test('wildcards anchor the complete URL, ignore case, escape regex characters', () => {
  const [re] = core.compilePatterns(['https://meet.google.com/a?b=1']);
  assert(re.test('HTTPS://MEET.GOOGLE.COM/a?b=1'));
  assert(!re.test('https://meetXgoogleXcom/a?b=1'));
  assert(!re.test('https://meet.google.com/a?b=1&extra=1'));
});
test('full URL wildcard semantics intentionally include query and noncanonical hosts', () => {
  const [re] = core.compilePatterns(['*zoom.us/*']);
  assert(re.test('https://us02web.zoom.us/wc/test'));
  assert(re.test('https://notzoom.us/test'));
  assert(re.test('https://example.test/?next=zoom.us/test'));
});
test('matching URLs are deduplicated; every scheme follows the same pattern semantics', () => {
  const tabs = [{ url: 'https://meet.google.com/a' }, { url: 'https://meet.google.com/a' }, { url: 'about:blank' }, {}];
  assert.deepEqual(core.matchingUrls(tabs, core.compilePatterns(['https://meet.google.com/*'])), ['https://meet.google.com/a']);
  assert.deepEqual(core.matchingUrls(tabs, core.compilePatterns(['*'])), ['about:blank', 'https://meet.google.com/a']);
});
test('settings reject noninteger ports, whitespace and non-plugin tokens', () => {
  for (const port of [0, 65536, 5.5, '5e3', 'abc']) assert.throws(() => core.validateSettings(port, 'lma_pl_test'));
  for (const token of ['wrong', 'lma_pl_', 'lma_pl_abc def']) assert.throws(() => core.validateSettings(52469, token));
  assert.deepEqual(core.validateSettings('52469', 'lma_pl_test'), { port: 52469, token: 'lma_pl_test', maxAgeSeconds: 180, detectionDelaySeconds: 3, stopDelaySeconds:5 });
});
test('protocol rejects malformed config, state, ack and unknown messages', () => {
  for (const message of [config({ poll_interval_seconds: 30 }), config({ patterns: [42] }), { type: 'state', state: 'recording' },
    { type: 'state', state: 'awaiting_confirmation', owner: true }, { type: 'ack', seq: -1 }, { type: 'mystery' }]) assert.throws(() => core.validateMessage(message));
});
