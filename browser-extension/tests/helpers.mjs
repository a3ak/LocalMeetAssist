import '../src/matcher.js';
import '../src/protocol.js';
import '../src/storage.js';
import '../src/client.js';
import '../src/tab-tracker.js';
import '../src/meeting-controller.js';
import '../src/notifications.js';
import '../src/background.js';
export const core = globalThis.LMA;
export const settle = async () => { for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve)); };
export class FakeTimers {
  now = 0;
  next = 0;
  tasks = new Map();
  setTimeout = (fn, delay) => { const id = ++this.next; this.tasks.set(id, { fn, at: this.now + delay }); return id; };
  clearTimeout = id => this.tasks.delete(id);
  async advance(ms) {
    const end = this.now + ms;
    for (;;) {
      const next = [...this.tasks.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      this.now = next[1].at;
      this.tasks.delete(next[0]);
      next[1].fn();
      await settle();
    }
    this.now = end;
  }
}
export class FakeSocket {
  readyState = 0;
  sent = [];
  closeCount = 0;
  constructor(url) { this.url = url; }
  open() { this.readyState = 1; this.onopen?.(); }
  send(text) { if (this.readyState !== 1) throw new Error('closed'); this.sent.push(JSON.parse(text)); }
  message(value) { this.onmessage?.({ data: typeof value === 'string' ? value : JSON.stringify(value) }); }
  close(code = 1000) { this.closeCount++; this.readyState = 3; this.onclose?.({ code }); }
}
export function fakeApi({ firefox = false, data = {}, tabs = [], history = true, detectionDelaySeconds = 0 } = {}) {
  const store = { ...data, settings: { port: 52469, token: 'lma_pl_test_only_not_a_real_secret', maxAgeSeconds: 180,
    detectionDelaySeconds, ...data.settings } };
  let nextId = 0;
  const identify = values => values.map(tab => { if (!Number.isSafeInteger(tab.id)) tab.id = ++nextId; nextId = Math.max(nextId, tab.id); return tab; });
  identify(tabs);
  // Existing protocol fixtures model pages already observed in this browser session.
  // Fresh-start tests explicitly use history:false, matching a newly loaded extension.
  const session = history ? { tabHistory: { version: 1, entries: tabs.map(tab => ({ id: tab.id, url: tab.url || '',
    enteredAt: Date.now() - 10000, admitted: false })) } } : {};
  const api = {
    id: 'test-extension', version: '0.1.0', firefox, store, session, tabs, notifications: {}, deliveries: [], alarms: [], badges: [], scanDeadlines: [],
    sessionGet: async key => ({ [key]: structuredClone(session[key]) }),
    sessionSet: async values => Object.assign(session, structuredClone(values)),
    storageGet: async keys => {
      if (typeof keys === 'string') keys = [keys];
      return Object.fromEntries(keys.filter(k => k in store).map(k => [k, store[k]]));
    },
    storageSet: async values => Object.assign(store, values),
    storageRemove: async keys => { for (const key of typeof keys === 'string' ? [keys] : keys) delete store[key]; },
    queryTabs: async () => structuredClone(identify(api.tabs)),
    activeTab: async () => structuredClone(api.tabs.find(tab=>tab.active)||api.tabs[0]),
    closeTabs: async ids => { api.tabs=api.tabs.filter(tab=>!ids.includes(tab.id)); },
    openTab: async url => { const tab={id:++nextId,url};api.tabs.push(tab);return tab; },
    url: path => 'extension://test/' + path,
    createNotification: async (id, options) => { api.notifications[id] = options; api.deliveries.push({ id, options }); return id; },
    clearNotification: async id => { delete api.notifications[id]; return true; },
    getNotifications: async () => ({ ...api.notifications }),
    notificationPermission: async () => 'granted',
    presentAction: async presentation => { api.badges.push(presentation); },
    ensureAlarm: async interval => { api.alarms.push(interval); },
    clearAlarm: async () => { api.alarms.push('clear'); },
    scanAlarm: async deadline => { api.scanDeadlines.push(deadline); }
  };
  return api;
}
export const config = (changes = {}) => ({ type: 'config', protocol_version:1,capabilities:['manual_tabs','recording_state'],enabled: true, mode: 'auto', patterns: ['*meet.google.com/*'],
  poll_interval_seconds: 20, listen_port:52469,effective_port:52469, ...changes });
export async function setup({ api = fakeApi(), ...options } = {}) {
  const sockets = [];
  const timers = new FakeTimers();
  const started = Date.now(); let nextUuid = 0;
  const app = new core.ExtensionApp(api, { socketFactory: url => { const socket = new FakeSocket(url); sockets.push(socket); return socket; },
    timers, now: () => started + timers.now, uuid: () => 'test-' + (++nextUuid), ...options });
  await app.reload();
  const socket = sockets.at(-1);
  if (socket) socket.open();
  await settle();
  return { app, socket, sockets, timers, api };
}
export async function handshake(ctx, cfg = config(), state = { state: 'idle', owner: false }) {
  ctx.socket.message(cfg);
  ctx.socket.message(serverState(state));
  await settle();
}
export const serverState = state => ({type:'state',recording_active:['recording','stop_pending'].includes(state.state),recording_revision:['recording','stop_pending'].includes(state.state)?1:0,...state});
export const tabsMessages = socket => socket.sent.filter(m => m.type === 'tabs');
