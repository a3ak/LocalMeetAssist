(() => {
  const ns = globalThis.LMA;
  const point = entry => entry && ({ id: entry.id, url: entry.url, glob: entry.glob });
  ns.MeetingController = class {
    constructor(tracker, now) { this.tracker = tracker; this.now = now; this.data = { active: null, revision: null }; this.rows = []; }
    async init() {
      const saved = (await this.tracker.api.sessionGet('meetingControl')).meetingControl;
      this.hasSession = saved?.version === 1 && (saved.active === null || typeof saved.active === 'boolean');
      if (this.hasSession) this.data = saved;
      const local = (await this.tracker.api.storageGet('recordingRecovery')).recordingRecovery;
      this.recoveryPending = !this.hasSession || saved.recoveryPending === true;
      this.recovery = local;
      this.localSignature = JSON.stringify(local || null);
    }
    clearControl() {
      for (const key of ['origin','controller','candidate','stopAt','switchAt','switchStarted','switchSequence','lostAt','closedAt','stopRequested','notice','restartWaiting']) delete this.data[key];
    }
    async reset() {
      this.clearControl(); this.data = { active: null, revision: null }; this.recoveryPending = false;
      this.recovery = null; await this.persist();
    }
    async persist() {
      await this.tracker.persist(); await this.tracker.api.sessionSet({ meetingControl: { ...this.data, version: 1, recoveryPending: this.recoveryPending } });
      if (this.recoveryPending) return; // Do not discard the restart context before the first authoritative state.
      const d = this.data;
      const local = d.active && d.owned && d.origin && this.clientId ? {
        schema_version: 1, client_id: this.clientId, source_url: d.origin.url,
        source_glob: d.origin.glob, recording_revision: d.revision
      } : null;
      const signature = JSON.stringify(local);
      if (signature === this.localSignature) return;
      if (local) await this.tracker.api.storageSet({ recordingRecovery: local });
      else await this.tracker.api.storageRemove('recordingRecovery');
      this.localSignature = signature;
    }
    recording(active, revision, owner, scenario = 'recording', allowBind = true) {
      const d = this.data;
      const owned = owner && scenario === 'recording';
      const changedSession = d.active === true && revision !== d.revision;
      const ended = d.active === true && (!active || changedSession);
      if (d.active === true && (!active || changedSession)) {
        this.tracker.invalidate();
        this.clearControl();
      }
      if (d.origin && !owned) { this.tracker.invalidate(); this.clearControl(); }
      const r = this.recovery;
      let recovered = false;
      if (this.recoveryPending && r) {
        let sourceValid = false;
        try { sourceValid = !!new URL(r.source_url) && ns.compilePatterns([r.source_glob])[0].test(r.source_url); } catch { /* Corrupt local context cannot control a recording. */ }
        recovered = active && owned && r.schema_version === 1 && r.client_id === this.clientId
          && Number.isSafeInteger(r.recording_revision) && r.recording_revision === revision
          && typeof r.source_url === 'string' && r.source_url.length > 0 && r.source_url.length <= 4096
          && !ns.isControlUrl(r.source_url) && typeof r.source_glob === 'string'
          && r.source_glob.length > 0 && r.source_glob.length <= 4096 && sourceValid;
        // A rejected recovery context must never be repurposed as a new source for a running recording.
        allowBind = false;
      }
      if (recovered) {
        this.tracker.invalidate();
        d.origin = { url: r.source_url, glob: r.source_glob };
        delete d.controller;
        d.active = true; d.owned = true; this.startSwitch(); d.restartWaiting = true;
        d.notice = 'Браузер перезапущен. Выберите управляющую вкладку.';
      } else if (active && d.active !== true && d.candidate && owned && allowBind) {
        d.origin = { ...d.candidate }; d.controller = { ...d.candidate, linked: false };
        this.tracker.invalidate(d.controller.id);
        delete d.candidate;
      }
      this.recoveryPending = false; this.recovery = null;
      d.active = active; d.revision = revision; d.owned = owned;
      if (ended || (r && (!active || r.recording_revision !== revision))) d.needsEmpty = true;
      if (active && owned && !d.origin) d.notice = 'Локальный контекст встречи отсутствует. Запись можно завершить кнопкой Stop.';
    }
    decline() {
      this.tracker.invalidate(null, 'Отклонена');
      delete this.data.candidate;
    }
    observe(event) {
      const controller = this.data.controller;
      if (!event || !controller) return;
      if (event?.id === controller?.id && (event.kind === 'removed' || (!controller.linked && event.kind === 'navigation' && event.url !== controller.url))) {
        this.data.closedAt = Number.isFinite(event.at) ? event.at : this.now();
      }
    }
    startSwitch() {
      const d = this.data;
      if (!d.active || !d.owned || !d.origin) throw new Error('Запись не привязана к странице встречи.');
      d.switchStarted = this.now(); d.switchSequence = this.tracker.sequence; d.switchAt = this.now() + 60000; delete d.stopAt;
      delete d.stopRequested;
      d.notice = 'Запись сохраняется на время переключения.';
    }
    select(tab) {
      if (!this.data.active || !this.data.origin || !this.data.switchAt || this.now() > this.data.switchAt) throw new Error('Время выбора управляющей вкладки истекло.');
      if (!Number.isSafeInteger(tab?.id)) throw new Error('Активная вкладка недоступна.');
      this.data.controller = { id: tab.id, url: tab.url || '', glob: this.data.origin.glob, linked: true };
      delete this.data.switchAt; delete this.data.stopAt; delete this.data.lostAt; delete this.data.closedAt; delete this.data.stopRequested; delete this.data.restartWaiting;
      this.data.notice = 'Управление передано. Исходный URL встречи сохранён.';
    }
    collect(tabs, config, expressions, settings) {
      const base = this.tracker.collect(tabs, expressions, settings, config.enabled);
      const now = this.now(), d = this.data;
      const rows = tabs.flatMap(tab => {
        if (ns.isControlUrl(tab.url)) return [];
        const index = expressions.findIndex(re => re.test(tab.url || ''));
        if (index < 0) return [];
        const entry = this.tracker.entries.get(tab.id);
        return [{ ...entry, title: tab.title || '', glob: config.patterns[index] }];
      });
      this.rows = rows;
      let urls = [], stopDue = false, deadline = base.nextDeadline;
      const choose = rows.filter(row => row.admitted).sort((a,b) => a.enteredAt-b.enteredAt || a.id-b.id);
      if (d.active && d.owned && d.origin && config.enabled) {
        const controller = d.controller;
        const live = tabs.find(tab => tab.id === controller?.id);
        if (live && controller.linked) controller.url = live.url || '';
        let valid = !!live && (controller.linked || live.url === controller.url);
        if (!valid && !d.lostAt) {
          d.lostAt = d.closedAt ?? now;
          const navigated = live && live.url !== controller.url ? live.id : null;
          this.tracker.invalidate(navigated);
          if (!d.switchAt) d.stopAt = d.lostAt + settings.stopDelaySeconds * 1000;
        }
        // Renewals are admitted immediately: the initial detection delay applies only to a new recording.
        const after = d.switchAt ? d.switchStarted : d.lostAt;
        const until = d.switchAt || d.stopAt;
        const renew = !d.stopRequested && until !== undefined && rows.map(row=>({...row,...this.tracker.entries.get(row.id)})).find(row => row.glob === d.origin.glob
          && row.enteredAt !== null && row.enteredAt >= after && row.enteredAt <= until
          && (!d.switchAt || (row.sequence || 0) > d.switchSequence)
          && now - row.enteredAt <= settings.maxAgeSeconds * 1000
          && (row.id !== controller?.id || row.url !== controller?.url));
        if (renew) {
          d.controller = { ...point(renew), linked: false };
          this.tracker.entries.get(renew.id).admitted = true; this.tracker.dirty = true;
          this.tracker.invalidate(renew.id);
          d.notice = 'Запись продолжена: новая вкладка по тому же GLOB открыта повторно.';
          delete d.stopAt; delete d.switchAt; delete d.lostAt; delete d.closedAt; delete d.stopRequested; delete d.restartWaiting; valid = true;
        }
        if (d.switchAt && now >= d.switchAt) {
          const expiredAt = d.switchAt;
          delete d.switchAt;
          delete d.restartWaiting;
          d.notice = 'Ожидание переключения истекло.';
          if (!valid) d.stopAt = expiredAt + settings.stopDelaySeconds * 1000;
        }
        if (!valid && !d.switchAt && d.stopAt === undefined) d.stopAt = now + settings.stopDelaySeconds * 1000;
        if (d.stopAt !== undefined && now >= d.stopAt) stopDue = true;
        // Retain the initial URL during the stop delay and switching, independent of the current controller's URL.
        urls = [d.origin.url];
        for (const at of [d.switchAt,d.stopAt]) if (at !== undefined && at > now) deadline = deadline === null ? at : Math.min(deadline,at);
      } else if (!d.active && config.enabled) {
        const candidate = choose.find(row => row.id === d.candidate?.id && row.url === d.candidate.url) || choose[0];
        d.candidate = point(candidate);
        if (candidate) urls = [candidate.url];
      }
      const freshRows = rows.map(row => ({ ...row, ...this.tracker.entries.get(row.id) }));
      this.rows = freshRows;
      return { ...base, urls, stopDue, nextDeadline: deadline, rows: freshRows };
    }
  };
})();
