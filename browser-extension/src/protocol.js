(() => {
  const ns = globalThis.LMA ||= {};
  ns.STATES = new Set(['idle', 'recording', 'awaiting_confirmation']);
  ns.ACTIVE_STATES = new Set(['recording', 'awaiting_confirmation']);
  ns.CONTROL_URLS = { start: 'https://localmeetassist.local/record_manual', stop: 'https://localmeetassist.local/stop_manual' };
  ns.isControlUrl = url => { try { return new URL(url).hostname.toLowerCase() === 'localmeetassist.local'; } catch { return false; } };
  ns.validateSettings = (port, token, maxAgeSeconds = 180, detectionDelaySeconds = 3, stopDelaySeconds = 5) => {
    if (!/^\d{1,5}$/.test(String(port)) || Number(port) < 1 || Number(port) > 65535) {
      throw new Error('Порт должен быть целым числом от 1 до 65535.');
    }
    if (typeof token !== 'string' || !/^lma_pl_[^\s]{1,2048}$/.test(token)) {
      throw new Error('Введите plugin-токен LocalMeetAssist, начинающийся с lma_pl_.');
    }
    if (!/^\d{1,3}$/.test(String(maxAgeSeconds)) || Number(maxAgeSeconds) < 30 || Number(maxAgeSeconds) > 300) {
      throw new Error('Возраст страницы должен быть целым числом от 30 до 300 секунд.');
    }
    if (!/^\d{1,2}$/.test(String(detectionDelaySeconds)) || Number(detectionDelaySeconds) < 0 || Number(detectionDelaySeconds) > 30) {
      throw new Error('Задержка должна быть целым числом от 0 до 30 секунд.');
    }
    if (!/^\d{1,3}$/.test(String(stopDelaySeconds)) || Number(stopDelaySeconds) < 0 || Number(stopDelaySeconds) > 300) {
      throw new Error('Задержка остановки должна быть целым числом от 0 до 300 секунд.');
    }
    return { port: Number(port), token, maxAgeSeconds: Number(maxAgeSeconds), detectionDelaySeconds: Number(detectionDelaySeconds), stopDelaySeconds: Number(stopDelaySeconds) };
  };
  ns.validateMessage = data => {
    if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('protocol');
    switch (data.type) {
      case 'config':
        if ((data.protocol_version !== undefined && (!Number.isSafeInteger(data.protocol_version) || data.protocol_version < 1))
          || (data.capabilities !== undefined && (!Array.isArray(data.capabilities) || data.capabilities.some(value => typeof value !== 'string')))) throw new Error('protocol');
        if (typeof data.enabled !== 'boolean' || !['auto', 'notify'].includes(data.mode)
          || !Array.isArray(data.patterns) || data.patterns.length > 1000
          || data.patterns.some(p => typeof p !== 'string' || p.length > 4096)
          || !Number.isFinite(data.poll_interval_seconds) || data.poll_interval_seconds < 10 || data.poll_interval_seconds > 25) throw new Error('protocol');
        if (data.protocol_version === 1 && (!Number.isSafeInteger(data.listen_port) || data.listen_port < 0 || data.listen_port > 65535
          || !Number.isSafeInteger(data.effective_port) || data.effective_port < 1 || data.effective_port > 65535)) throw new Error('protocol');
        break;
      case 'state':
        if (!ns.STATES.has(data.state) || typeof data.owner !== 'boolean'
          || (data.state === 'awaiting_confirmation' && (!data.owner || typeof data.prompt_id !== 'string' || !data.prompt_id || data.prompt_id.length > 256))) throw new Error('protocol');
        break;
      case 'ask': if (data.what !== 'tabs' || typeof data.owner !== 'boolean') throw new Error('protocol'); break;
      case 'ack':
        if (!Number.isSafeInteger(data.seq) || data.seq < 1
          || (data.command_id !== undefined && (typeof data.command_id !== 'string' || !['applied','noop','rejected'].includes(data.command_result)))) throw new Error('protocol');
        break;
      case 'error': if (typeof data.error !== 'string') throw new Error('protocol'); break;
      default: throw new Error('protocol');
    }
    if (data.type === 'state' || data.type === 'ask') {
      if (typeof data.recording_active !== 'boolean') throw new Error('protocol');
      if (!Number.isSafeInteger(data.recording_revision) || data.recording_revision < 0) throw new Error('protocol');
    }
    return data;
  };
})();
