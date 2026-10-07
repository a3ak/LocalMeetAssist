(() => {
  const ns = globalThis.LMA ||= {};
  ns.SettingsStore = class {
    constructor(api, uuid = () => crypto.randomUUID()) { this.api = api; this.uuid = uuid; }
    async load() {
      const data = await this.api.storageGet(['settings', 'installId', 'authBlocked', 'protocolBlocked', 'protocolBlockedVersion']);
      if (data.protocolBlocked && data.protocolBlockedVersion !== 1) await this.api.storageRemove(['protocolBlocked','protocolBlockedVersion']);
      let installId = data.installId;
      if (!installId) {
        installId = this.uuid();
        await this.api.storageSet({ installId });
      }
      let settings = null;
      try { if (data.settings) settings = ns.validateSettings(data.settings.port, data.settings.token,
        data.settings.maxAgeSeconds, data.settings.detectionDelaySeconds, data.settings.stopDelaySeconds); } catch { /* Invalid local settings require user correction. */ }
      return { settings, clientId: `${this.api.id}:${installId}`, authBlocked: data.authBlocked === true, protocolBlocked: data.protocolBlocked === true && data.protocolBlockedVersion === 1 };
    }
    async save(port, token, maxAgeSeconds, detectionDelaySeconds, stopDelaySeconds) {
      const data = await this.api.storageGet('settings');
      const settings = ns.validateSettings(port, typeof token === 'string' ? token.trim() : token,
        maxAgeSeconds === undefined ? data.settings?.maxAgeSeconds : maxAgeSeconds,
        detectionDelaySeconds === undefined ? data.settings?.detectionDelaySeconds : detectionDelaySeconds,
        stopDelaySeconds === undefined ? data.settings?.stopDelaySeconds : stopDelaySeconds);
      const connectionChanged = data.settings?.port !== settings.port || data.settings?.token !== settings.token;
      const changed = connectionChanged || (data.settings?.maxAgeSeconds ?? 180) !== settings.maxAgeSeconds
        || (data.settings?.detectionDelaySeconds ?? 3) !== settings.detectionDelaySeconds
        || (data.settings?.stopDelaySeconds ?? 5) !== settings.stopDelaySeconds;
      if (connectionChanged) {
        await this.api.storageRemove(['recordingRecovery', 'protocolBlocked', 'protocolBlockedVersion']);
        await this.api.sessionSet({meetingControl:null,pendingCommand:null});
      }
      if (changed) await this.api.storageSet({ settings, ...(connectionChanged ? { authBlocked: false } : {}) });
      return changed;
    }
    async clear() {
      await this.api.storageRemove(['settings', 'authBlocked', 'protocolBlocked', 'protocolBlockedVersion', 'recordingRecovery', 'shownPrompt', 'notificationDelivery']);
      await this.api.sessionSet({meetingControl:null,pendingCommand:null});
    }
  };
})();
