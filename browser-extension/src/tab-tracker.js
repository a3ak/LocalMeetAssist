(() => {
  const ns = globalThis.LMA ||= {};
  const matches = (url, expressions) => typeof url === 'string' && expressions.some(re => re.test(url));
  const validId = id => Number.isSafeInteger(id) && id >= 0;
  ns.TabTracker = class {
    constructor(api, now = () => Date.now()) { this.api = api; this.now = now; this.entries = new Map(); this.dirty = false; this.sequence = 0; }
    async init() {
      const stored = (await this.api.sessionGet('tabHistory')).tabHistory;
      const saved = new Map((stored?.version === 1 && Array.isArray(stored.entries) ? stored.entries : [])
        .filter(entry => validId(entry.id) && typeof entry.url === 'string'
          && (entry.enteredAt === null || Number.isFinite(entry.enteredAt))).map(entry => [entry.id, entry]));
      for (const tab of await this.api.queryTabs()) {
        if (!validId(tab.id)) continue;
        const old = saved.get(tab.id), url = tab.url || '';
        // No observed navigation time means an existing page is old, even if it is the active tab.
        this.entries.set(tab.id, old?.url === url ? { id: tab.id, url, enteredAt: old.enteredAt, admitted: old.admitted === true, reason: old.reason, sequence: old.sequence || 0 }
          : { id: tab.id, url, enteredAt: null, admitted: false });
      }
      this.sequence = Math.max(0,...[...this.entries.values()].map(entry=>entry.sequence||0));
      this.dirty = true;
      await this.persist();
    }
    observe(event, expressions = []) {
      if (!event || !validId(event.id)) return;
      if (event.kind === 'removed') { this.dirty = this.entries.delete(event.id) || this.dirty; return; }
      if (!['created', 'navigation'].includes(event.kind)) return;
      const old = this.entries.get(event.id), url = typeof event.url === 'string' ? event.url : '';
      // Duplicate tabs/navigation events and F5 of the same URL cannot refresh an old page.
      if (event.kind !== 'created' && old?.url === url) return;
      this.entries.set(event.id, { id: event.id, url, enteredAt: Number.isFinite(event.at) ? event.at : this.now(), admitted: false, sequence: ++this.sequence });
      this.dirty = true;
    }
    collect(tabs, expressions, settings, enabled) {
      const now = this.now(), live = new Set(), urls = new Set();
      let pendingCount = 0, ignoredCount = 0, nextDeadline = null;
      for (const tab of tabs) {
        if (!validId(tab.id)) continue;
        live.add(tab.id);
        const url = tab.url || '';
        let entry = this.entries.get(tab.id);
        if (!entry || entry.url !== url) {
          // This is a new observation after initialization; startup pages were seeded in init().
          this.observe({ kind: entry ? 'navigation' : 'created', id: tab.id, url, at: now }, expressions);
          entry = this.entries.get(tab.id);
        }
        if (!enabled || ns.isControlUrl?.(url) || !matches(url, expressions)) {
          if (entry.admitted) { entry.admitted = false; this.dirty = true; }
          continue;
        }
        if (entry.admitted) { urls.add(url); continue; }
        if (entry.enteredAt === null || now - entry.enteredAt > settings.maxAgeSeconds * 1000) { ignoredCount++; continue; }
        const deadline = entry.enteredAt + settings.detectionDelaySeconds * 1000;
        if (deadline > now) {
          pendingCount++;
          nextDeadline = nextDeadline === null ? deadline : Math.min(deadline, nextDeadline);
        } else {
          entry.admitted = true; this.dirty = true; urls.add(url);
        }
      }
      for (const id of this.entries.keys()) if (!live.has(id)) { this.entries.delete(id); this.dirty = true; }
      return { urls: [...urls].sort(), pendingCount, ignoredCount, nextDeadline };
    }
    invalidate(exceptId = null, reason = 'Устаревшая') {
      for (const entry of this.entries.values()) {
        if (entry.id === exceptId) continue;
        entry.enteredAt = null; entry.admitted = false; entry.reason = reason;
      }
      this.dirty = true;
    }
    async persist() {
      if (!this.dirty) return;
      await this.api.sessionSet({ tabHistory: { version: 1, entries: [...this.entries.values()].map(entry => ({ ...entry })) } });
      this.dirty = false;
    }
  };
})();
