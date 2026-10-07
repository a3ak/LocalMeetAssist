/* All platform API access lives here. Supported browser versions provide promises. */
(() => {
  const ns = globalThis.LMA ||= {};
  const api = globalThis.browser || globalThis.chrome;
  if (!api) return; // Allows pure core modules to be tested in Node.
  const firefox = typeof api.runtime.getBrowserInfo === 'function';
  ns.browserApi = {
    firefox,
    id: api.runtime.id,
    version: api.runtime.getManifest().version,
    url: path => api.runtime.getURL(path),
    storageGet: keys => api.storage.local.get(keys),
    storageSet: values => api.storage.local.set(values),
    storageRemove: keys => api.storage.local.remove(keys),
    sessionGet: keys => api.storage.session.get(keys),
    sessionSet: values => api.storage.session.set(values),
    storageRestrict: async () => {
      if (api.storage.local.setAccessLevel) await api.storage.local.setAccessLevel({ accessLevel: 'TRUSTED_CONTEXTS' });
      if (api.storage.session.setAccessLevel) await api.storage.session.setAccessLevel({ accessLevel: 'TRUSTED_CONTEXTS' });
    },
    queryTabs: () => api.tabs.query({}),
    activeTab: async () => (await api.tabs.query({ active: true, lastFocusedWindow: true }))[0],
    closeTabs: ids => api.tabs.remove(ids),
    openTab: url => api.tabs.create({ url }),
    createNotification: (id, options) => api.notifications.create(id, options),
    clearNotification: id => api.notifications.clear(id),
    getNotifications: () => api.notifications.getAll(),
    notificationPermission: () => api.notifications.getPermissionLevel ? api.notifications.getPermissionLevel() : Promise.resolve('unknown'),
    presentAction: async ({ text, color, textColor, title, recording }) => {
      const prefix = recording ? 'icons/recording/' : 'icons/';
      await Promise.all([
        api.action.setIcon({ path: Object.fromEntries([16, 32, 48, 64, 128].map(size => [size, `${prefix}${size}.png`])) }),
        api.action.setTitle({ title }),
        api.action.setBadgeBackgroundColor({ color }),
        api.action.setBadgeTextColor({ color: textColor }),
        api.action.setBadgeText({ text })
      ]);
    },
    ensureAlarm: async minutes => {
      const old = await api.alarms.get('lma-watchdog');
      if (!old || old.periodInMinutes !== minutes) await api.alarms.create('lma-watchdog', { periodInMinutes: minutes });
    },
    clearAlarm: () => api.alarms.clear('lma-watchdog'),
    scanAlarm: deadline => deadline === null ? api.alarms.clear('lma-tab-delay') : api.alarms.create('lma-tab-delay', { when: deadline }),
    onAlarm: listener => api.alarms.onAlarm.addListener(alarm => { if (['lma-watchdog', 'lma-tab-delay'].includes(alarm.name)) listener(); }),
    onStorage: listener => api.storage.onChanged.addListener((changes, area) => { if (area === 'local') listener(changes); }),
    onStartup: listener => api.runtime.onStartup.addListener(listener),
    onInstalled: listener => api.runtime.onInstalled.addListener(listener),
    onTabs: listener => {
      const event = (kind, id, url) => listener({ kind, id, url, at: Date.now() });
      api.tabs.onCreated.addListener(tab => event('created', tab.id, tab.url));
      api.tabs.onRemoved.addListener(id => event('removed', id));
      api.tabs.onUpdated.addListener((id, change) => { if ('url' in change) event('navigation', id, change.url); });
      const navigation = value => { if (value.frameId === 0) event('navigation', value.tabId, value.url); };
      api.webNavigation.onCommitted.addListener(navigation);
      api.webNavigation.onHistoryStateUpdated.addListener(navigation);
      api.webNavigation.onReferenceFragmentUpdated.addListener(navigation);
      api.windows.onCreated.addListener(() => event('scan'));
      api.windows.onRemoved.addListener(() => event('scan'));
    },
    onNotificationClick: listener => api.notifications.onClicked.addListener(listener),
    onNotificationButton: listener => { if (!firefox) api.notifications.onButtonClicked.addListener(listener); },
    onNotificationClosed: listener => api.notifications.onClosed.addListener(id => listener(id)),
    sendMessage: message => api.runtime.sendMessage(message),
    openOptions: () => api.runtime.openOptionsPage(),
    onMessage: handler => api.runtime.onMessage.addListener((message, sender, respond) => {
      if (sender.id !== api.runtime.id || !sender.url?.startsWith(api.runtime.getURL(''))) return false;
      Promise.resolve().then(() => handler(message)).then(respond, () => respond({ ok: false, error: 'Не удалось выполнить действие.' }));
      return true;
    })
  };
})();
