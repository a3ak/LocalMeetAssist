(() => {
  const ns = globalThis.LMA ||= {};
  ns.notificationId = prompt => 'lma-prompt:' + encodeURIComponent(prompt);
  ns.promptFromNotification = id => {
    if (typeof id !== 'string' || !id.startsWith('lma-prompt:')) return null;
    try { return decodeURIComponent(id.slice(11)); } catch { return null; }
  };
  ns.NotificationManager = class {
    constructor(api) { this.api = api; this.queue = Promise.resolve(); this.status = null; }
    sync(state, config) {
      const operation = this.queue.then(() => this.apply(state, config));
      this.queue = operation.catch(() => {});
      return operation;
    }
    async apply(state, config) {
      const data = await this.api.storageGet(['shownPrompt', 'notificationDelivery']);
      const prompt = config?.enabled && state.owner && state.state === 'awaiting_confirmation' ? state.prompt_id : null;
      let existing = {};
      try { existing = await this.api.getNotifications(); } catch { /* Badge and popup still work. */ }
      for (const id of Object.keys(existing)) {
        if (id.startsWith('lma-prompt:') && id !== (prompt ? ns.notificationId(prompt) : null)) await this.api.clearNotification(id);
      }
      if (!prompt) {
        this.status = null; await this.api.storageRemove(['shownPrompt', 'notificationDelivery']);
        return;
      }
      if (data.shownPrompt === prompt) {
        this.status = data.notificationDelivery?.prompt === prompt ? data.notificationDelivery.status : 'unknown';
        return;
      }
      // Write before create: at-most-once across abrupt worker termination. Popup remains available if delivery fails.
      await this.api.storageSet({ shownPrompt: prompt });
      this.status = await this.deliver(ns.notificationId(prompt), this.options(false));
      await this.api.storageSet({ notificationDelivery: { prompt, status: this.status } });
    }
    options(test) {
      const options = { type: 'basic', iconUrl: this.api.url('icons/128.png'),
        title: test ? 'LocalMeetAssist — проверка уведомления' : 'Начать запись встречи? — LocalMeetAssist',
        message: test ? 'Уведомления работают. Эта проверка не запускает запись.'
          : this.api.firefox ? 'Требуется ваш ответ. Нажмите уведомление для записи или значок LM для отказа.'
          : 'Обнаружена страница встречи. Выберите «Начать запись» или «Не записывать».' };
      if (!this.api.firefox) Object.assign(options, { priority: 2, silent: false, requireInteraction: !test,
        ...(!test ? { buttons: [{ title: 'Начать запись' }, { title: 'Не записывать' }] } : {}) });
      return options;
    }
    async deliver(id, options) {
      try {
        if (await this.api.notificationPermission() === 'denied') return 'denied';
        await this.api.createNotification(id, options);
        return 'created'; // API success cannot prove that the OS displayed a banner.
      } catch { return 'failed'; } // Never expose secret-bearing exception text.
    }
    async test() {
      const status = await this.deliver('lma-test', this.options(true));
      return { ok: status === 'created', notificationStatus: status,
        ...(status === 'denied' ? { error: 'Уведомления расширения запрещены. Разрешите их в браузере и ОС.' }
          : status === 'failed' ? { error: 'Браузер не смог создать уведомление. Проверьте разрешения уведомлений.' } : {}) };
    }
  };
})();
