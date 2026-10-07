import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { readFile } from 'node:fs/promises';

const source = name => readFile(new URL(`../src/${name}`, import.meta.url), 'utf8');

test('Chrome and Firefox compatibility wrappers apply badge text color and route timestamped main-frame navigation', async () => {
  for (const firefox of [false, true]) {
    const calls = [], handlers = {};
    const event = name => ({ addListener: handler => { handlers[name] = handler; } });
    const storageArea = { get: async () => ({}), set: async () => {}, remove: async () => {}, setAccessLevel: async () => {} };
    const api = {
      runtime: { id: 'fixture', getManifest: () => ({ version: '1.1.0' }), getURL: path => `extension://${path}`,
        ...(firefox ? { getBrowserInfo: () => ({}) } : {}) },
      action: Object.fromEntries(['setIcon', 'setTitle', 'setBadgeBackgroundColor', 'setBadgeTextColor', 'setBadgeText']
        .map(name => [name, async args => { calls.push({ name, args }); }])),
      storage: { local: storageArea, session: storageArea },
      tabs: { onCreated: event('created'), onRemoved: event('removed'), onUpdated: event('updated') },
      webNavigation: { onCommitted: event('committed'), onHistoryStateUpdated: event('history'), onReferenceFragmentUpdated: event('fragment') },
      windows: { onCreated: event('windowCreated'), onRemoved: event('windowRemoved') }
    };
    const sandbox = { [firefox ? 'browser' : 'chrome']: api }; vm.createContext(sandbox);
    vm.runInContext(await source('browser-api.js'), sandbox);
    const wrapper = sandbox.LMA.browserApi;
    await wrapper.presentAction({ text: '?', color: '#facc15', textColor: '#111827', title: 'Начать запись?', recording: false });
    assert.equal(calls.find(call => call.name === 'setBadgeTextColor').args.color, '#111827');
    assert.equal(calls.find(call => call.name === 'setBadgeBackgroundColor').args.color, '#facc15');
    const routed = []; wrapper.onTabs(value => routed.push(value));
    handlers.committed({ frameId: 1, tabId: 1, url: 'https://iframe.invalid/' }); assert.equal(routed.length, 0);
    handlers.created({ id: 1, url: 'https://meet.google.com/new' });
    handlers.history({ frameId: 0, tabId: 1, url: 'https://meet.google.com/next' });
    assert.equal(routed[0].kind, 'created'); assert.equal(routed[1].kind, 'navigation'); assert(Number.isFinite(routed[1].at));
    assert.equal(routed[1].id, 1); assert.equal(routed[1].url, 'https://meet.google.com/next');
  }
});

test('options migrate old settings to 180/3, save the edited time fields and test notifications separately', async () => {
  const html = await source('options.html');
  const nodes = Object.fromEntries([...html.matchAll(/\bid="([^"]+)"/g)].map(match => [match[1],
    { value: '', dataset: {}, handlers: {}, addEventListener(name, handler) { this.handlers[name] = handler; } }]));
  const submit = { disabled: false }, messages = [];
  nodes['settings-form'].querySelector = () => submit;
  const sandbox = {
    document: { getElementById: id => { assert(nodes[id], `Missing HTML control: ${id}`); return nodes[id]; } },
    LMA: { browserApi: { version: '1.1.0', storageGet: async () => ({ settings: { port: 52469, token: 'lma_pl_fixture' } }) },
      ui: { showStatus() {}, request: async message => { messages.push(message); return { ok: true, changed: true }; } } },
    setInterval: () => 1, clearInterval() {}, addEventListener() {}
  };
  vm.createContext(sandbox); vm.runInContext(await source('options.js'), sandbox);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(nodes['max-age'].value, 180); assert.equal(nodes['detection-delay'].value, 3); assert.equal(nodes['stop-delay'].value,5);
  nodes['max-age'].value = '120'; nodes['detection-delay'].value = '5'; nodes['stop-delay'].value = '12';
  await nodes['settings-form'].handlers.submit({ preventDefault() {} });
  const saved = messages.find(message => message.kind === 'save-settings');
  assert.equal(saved.maxAgeSeconds, '120'); assert.equal(saved.detectionDelaySeconds, '5'); assert.equal(saved.stopDelaySeconds,'12'); assert.equal(submit.disabled, false);
  await nodes['test-notification'].handlers.click({ currentTarget: nodes['test-notification'] });
  assert(messages.some(message => message.kind === 'test-notification'));
  assert.match(nodes['notification-test-result'].textContent, /передано ОС/);
});
