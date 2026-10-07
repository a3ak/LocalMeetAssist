(() => {
  const ns = globalThis.LMA ||= {};
  ns.SocketClient = class {
    constructor({ settings, clientId, socketFactory = url => new WebSocket(url), onMessage, onStatus, onForbidden, onUnsupported, now = () => Date.now(),
      timers = globalThis }) {
      Object.assign(this, { settings, clientId, socketFactory, onMessage, onStatus, onForbidden, onUnsupported, timers, now });
      this.socket = null;
      this.generation = 0;
      this.retry = 0;
      this.pending = new Map();
      this.waiters = [];
      this.incoming = Promise.resolve();
      this.blocked = false;
      this.stopped = false;
      this.status = 'offline';
    }
    get open() { return this.socket?.readyState === 1; }
    get ready() { return this.open && this.hasConfig && this.hasState; }
    statusChanged(status) {
      this.status = status;
      Promise.resolve(this.onStatus(status)).catch(() => {});
    }
    clearTimers() {
      for (const key of ['connectTimer', 'handshakeTimer', 'retryTimer', 'silenceTimer']) {
        if (this[key]) this.timers.clearTimeout(this[key]);
        this[key] = null;
      }
      for (const item of this.pending.values()) this.timers.clearTimeout(item.timer);
      this.pending.clear();
    }
    connect() {
      if (this.blocked || this.stopped || this.socket) return;
      if (this.retryTimer) this.timers.clearTimeout(this.retryTimer);
      this.retryTimer = null;
      const generation = ++this.generation;
      this.hasConfig = false;
      this.hasState = false;
      this.seq = 0;
      this.lastTabsSeq = 0;
      this.pollSeconds ||= 20; // Keep the negotiated rhythm during reconnect; N is server-only and may be as small as 2.
      this.statusChanged('connecting');
      let socket;
      try { socket = this.socketFactory(`ws://127.0.0.1:${this.settings.port}/api/v1/ws`); }
      catch { this.statusChanged('offline'); this.scheduleRetry(); return; }
      this.socket = socket;
      const current = () => this.generation === generation && this.socket === socket;
      this.connectTimer = this.timers.setTimeout(() => { if (current()) this.fail('offline'); }, this.handshakeBudget());
      socket.onopen = () => {
        if (!current()) return;
        this.timers.clearTimeout(this.connectTimer);
        this.connectTimer = null;
        // Never log this message, the URL, incoming payloads or exceptions from send().
        if (!this.send({ type: 'hello', token: this.settings.token, client_id: this.clientId, protocol_version: 1 })) return;
        this.handshakeTimer = this.timers.setTimeout(() => { if (current() && !this.ready) this.fail('protocol_error'); }, this.handshakeBudget());
      };
      socket.onmessage = event => {
        this.incoming = this.incoming.then(async () => {
          if (!current()) return;
          let message;
          try {
            if (typeof event.data !== 'string' || event.data.length > 262144) throw new Error('protocol');
            message = ns.validateMessage(JSON.parse(event.data));
          } catch { this.fail('protocol_error'); return; }
          if (message.type === 'error') {
            if (message.error === 'forbidden') await this.forbidden();
            else if (message.error === 'unsupported_protocol') await this.unsupported();
            else this.fail('protocol_error');
            return;
          }
          if (!this.hasConfig && message.type !== 'config') { this.fail('protocol_error'); return; }
          if (message.type === 'config') {
            this.hasConfig = true;
            this.pollSeconds = message.poll_interval_seconds;
          }
          if (message.type === 'ack') {
            const entry = this.pending.get(message.seq);
            if (entry) { this.timers.clearTimeout(entry.timer); this.pending.delete(message.seq); }
          }
          if (message.type === 'state') this.hasState = true;
          await this.onMessage(message);
          if (!current()) return;
          this.armSilenceWatchdog(generation);
          if (this.ready) {
            this.retry = 0;
            this.recoveryUntil = null;
            this.timers.clearTimeout(this.handshakeTimer);
            this.handshakeTimer = null;
            this.statusChanged('online');
            for (const waiter of this.waiters.splice(0)) waiter.resolve();
          }
        }).catch(() => { if (current()) this.fail('protocol_error'); });
      };
      socket.onerror = () => {}; // onclose carries the actionable result, without secret-bearing strings.
      socket.onclose = async event => {
        if (!current()) return;
        if (event.code === 1008) { await this.forbidden(); return; }
        if (event.code === 1002) { await this.unsupported(); return; }
        this.startRecoveryBudget(true);
        this.socket = null;
        this.clearTimers();
        this.hasConfig = false;
        this.hasState = false;
        this.statusChanged('offline');
        this.scheduleRetry();
      };
    }
    armSilenceWatchdog(generation) {
      if (this.silenceTimer) this.timers.clearTimeout(this.silenceTimer);
      this.lastServerAt = this.now();
      // Local transport recovery only. The server alone decides whether to stop recording.
      this.silenceTimer = this.timers.setTimeout(() => {
        if (this.generation === generation) this.fail('offline');
      }, (this.pollSeconds * 3 + 5) * 1000);
    }
    scheduleRetry() {
      if (this.blocked || this.stopped || this.retryTimer) return;
      const recovering = this.recoveryUntil && this.now() < this.recoveryUntil;
      const delay = Math.min(recovering ? 1 : 30, 2 ** Math.min(this.retry++, 5)) * 1000;
      this.retryTimer = this.timers.setTimeout(() => { this.retryTimer = null; this.connect(); }, delay);
    }
    send(message) {
      if (!this.open) return false;
      try { this.socket.send(JSON.stringify(message)); return true; }
      catch { this.fail('offline'); return false; }
    }
    sendTabs(urls, command = null) {
      if (!this.open || !this.hasConfig || this.pending.size >= 64) return null;
      const seq = ++this.seq;
      const timer = this.timers.setTimeout(() => { if (this.pending.has(seq)) this.fail('protocol_error'); }, this.pollSeconds * 2000 + 5000);
      this.pending.set(seq, { timer });
      if (!this.send({ type: 'tabs', urls, seq, ...(command ? { command_id: command.id } : {}) })) return null;
      this.lastTabsSeq = seq;
      return seq;
    }
    async ensureReady() {
      if (this.ready) return;
      if (this.blocked || this.stopped) throw new Error('unavailable');
      this.connect();
      await new Promise((resolve, reject) => {
        const waiter = { resolve: () => { this.timers.clearTimeout(timer); resolve(); } };
        const timer = this.timers.setTimeout(() => {
          this.waiters = this.waiters.filter(w => w !== waiter);
          reject(new Error('unavailable'));
        }, 6000);
        this.waiters.push(waiter);
      });
    }
    detachSocket() {
      const socket = this.socket;
      ++this.generation;
      this.socket = null;
      this.hasConfig = false;
      this.hasState = false;
      this.clearTimers();
      if (socket && socket.readyState < 2) try { socket.close(1000); } catch { /* Already closing. */ }
    }
    idleClose() { this.detachSocket(); this.statusChanged('idle_connection'); }
    startRecoveryBudget(closed = false) {
      if (this.recoveryUntil || !this.hasConfig) return;
      // Silent connection: at least (2+2)*poll from the last message. Explicit socket loss: at least 2*poll grace.
      this.recoveryUntil = closed ? this.now()+this.pollSeconds*2000 : (this.lastServerAt??this.now())+this.pollSeconds*4000;
    }
    handshakeBudget() {
      if (!this.recoveryUntil || this.now() >= this.recoveryUntil) return 5000;
      // Leave one second of margin; localhost failures must not consume the minimum server window in long handshakes.
      return Math.max(250,Math.min(5000,(this.recoveryUntil-this.now()-1000)/2));
    }
    fail(status) { this.startRecoveryBudget(); this.detachSocket(); this.statusChanged(status); this.scheduleRetry(); }
    async forbidden() {
      this.blocked = true;
      this.detachSocket();
      await this.onForbidden();
      this.statusChanged('forbidden');
    }
    async unsupported() {
      this.blocked = true; this.detachSocket();
      await this.onUnsupported?.(); this.statusChanged('unsupported_protocol');
    }
    stop() { this.stopped = true; this.detachSocket(); }
  };
})();
