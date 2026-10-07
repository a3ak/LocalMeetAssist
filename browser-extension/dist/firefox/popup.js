(() => {
  const { browserApi: api, ui } = globalThis.LMA;
  let promptId = null, sending = false, lastStatus = null, rowsSignature = '';
  const feedback = document.getElementById('feedback');
  let refreshQueue = Promise.resolve();
  function refresh() { const task = refreshQueue.then(paint); refreshQueue = task.catch(() => {}); return task; }
  async function paint() {
    const data = await ui.request({ kind: 'status' });
    if (!data.ok) { feedback.textContent = data.error; return; }
    ui.showStatus(data);
    lastStatus = data;
    promptId = data.promptId;
    document.getElementById('question').hidden = !promptId;
    document.getElementById('confirm').disabled = sending || !data.canAnswer;
    document.getElementById('decline').disabled = sending || !data.canAnswer;
    document.getElementById('matches').textContent = `Подходящих URL: ${data.openCount || 0}`;
    document.getElementById('version').textContent = 'v' + data.version;
    document.getElementById('reconnect').hidden = ['online', 'not_configured', 'forbidden', 'unsupported_protocol'].includes(data.status);
    const active = data.recordingActive === true;
    const record = document.getElementById('record-control');
    record.disabled = sending || !data.canControl;
    record.title = record.ariaLabel = active ? 'Завершить текущую запись' : 'Начать запись вручную';
    document.getElementById('record-circle').hidden = active;
    document.getElementById('record-square').hidden = !active;
    document.getElementById('open-app').disabled = !data.port;
    document.getElementById('open-app').title = `Открыть приложение · http://127.0.0.1:${data.port || 'port'}/`;
    document.getElementById('source').hidden = !active || !data.origin;
    document.getElementById('source-url').textContent = data.origin?.url || '';
    document.getElementById('source-glob').textContent = data.origin?.glob || '';
    document.getElementById('controller').hidden = !data.controller || (data.controller.id === data.origin?.id && data.controller.url === data.origin?.url);
    document.getElementById('controller-url').textContent = data.controller?.url || '';
    const waiting = !!data.switchAt;
    const left = waiting ? Math.max(0, Math.ceil((data.switchAt - Date.now()) / 1000)) : 0;
    document.getElementById('wait-switch').textContent = waiting ? 'Обновить ожидание · ' + time(left) : 'Ожидать переключение · 60 с';
    document.getElementById('wait-switch').disabled = sending || !data.canControl;
    document.getElementById('switch-progress').hidden = !waiting;
    document.getElementById('switch-progress-fill').style.width = (left / 60 * 100) + '%';
    document.getElementById('select-controller-panel').hidden = !waiting;
    document.getElementById('select-controller').disabled = sending || !data.canControl;
    if (waiting) {
      const current = await ui.request({ kind: 'active-tab' });
      document.getElementById('active-tab-title').textContent = current.tab?.title || 'Активная вкладка';
      document.getElementById('active-tab-url').textContent = current.tab?.url || 'Недоступна';
      document.getElementById('select-controller').disabled ||= !current.tab;
    }
    const notice = document.getElementById('recording-notice');
    notice.hidden = !data.notice || !active;
    notice.textContent = data.restartWaiting && waiting ? `Браузер перезапущен. Выберите управляющую вкладку — осталось ${left} с` : data.notice || '';
    document.getElementById('heartbeat').textContent = data.status !== 'online' ? 'Состояние записи неизвестно'
      : data.lastHeartbeat ? `Статус записи получен ${Math.max(0, Math.floor((Date.now() - data.lastHeartbeat) / 1000))} с назад` : '';
    if (data.stopAt && active) document.getElementById('connection-detail').textContent = 'Ожидаем новую вкладку · до завершения ' + time(Math.max(0, Math.ceil((data.stopAt - Date.now()) / 1000)));
    if (data.commandPending) document.getElementById('connection-detail').textContent = 'Действие отправлено. Ожидаем подтверждение приложения.';
    if (data.commandError) { feedback.textContent = data.commandError; feedback.dataset.error = 'true'; }
    const signature = JSON.stringify([data.rows, data.controller, data.origin, active]);
    if (signature !== rowsSignature) { renderRows(data); rowsSignature = signature; }
    document.getElementById('close-all').disabled = sending || !data.rows?.length;
  }
  function time(seconds) { return String(Math.floor(seconds / 60)).padStart(2, '0') + ':' + String(seconds % 60).padStart(2, '0'); }
  function element(tag, cls, text) { const node = document.createElement(tag); node.className = cls; if (text !== undefined) node.textContent = text; return node; }
  function renderRows(data) {
    const list = document.getElementById('url-list'); list.replaceChildren();
    const groups = new Map();
    for (const row of data.rows || []) { if (!groups.has(row.glob)) groups.set(row.glob, []); groups.get(row.glob).push(row); }
    for (const [glob, rows] of groups) {
      const group = element('section', 'card url-group');
      group.append(element('p', 'field-label', 'GLOB'), element('p', 'glob-text', glob));
      for (const row of rows) {
        const entry = element('div', 'url-row'), copy = element('div', 'url-copy');
        copy.append(element('p', 'url-text', row.url));
        const tags = element('div', 'url-tags');
        if (row.source) tags.append(element('span', 'url-tag', 'Источник записи'));
        if (row.controlling) tags.append(element('span', 'url-tag', 'Управляющая'));
        if (row.reason) tags.append(element('span', 'url-tag old', row.reason));
        copy.append(tags);
        const close = element('button', 'icon-button secondary', '×'); close.type = 'button';
        close.title = 'Закрыть эту вкладку'; close.setAttribute('aria-label', 'Закрыть вкладку ' + row.url);
        close.addEventListener('click', () => action({ kind: 'close-tab', tabId: row.id }));
        entry.append(copy, close); group.append(entry);
      }
      list.append(group);
    }
    if (data.recordingActive && data.controller?.linked && !data.rows.some(row => row.id === data.controller.id)) {
      const group = element('section', 'card url-group'), entry = element('div', 'url-row');
      group.append(element('p', 'field-label', 'Управляющая вкладка · вне GLOB'));
      const url = element('p', 'url-text url-copy', data.controller.url);
      const close = element('button', 'icon-button secondary', '×'); close.type = 'button'; close.setAttribute('aria-label', 'Закрыть управляющую вкладку');
      close.addEventListener('click', () => action({ kind: 'close-tab', tabId: data.controller.id }));
      entry.append(url, close); group.append(entry); list.append(group);
    }
    document.getElementById('empty-list').hidden = !!data.rows?.length;
  }
  async function action(message) {
    if (sending) return;
    sending = true; await refresh();
    const result = await ui.request(message);
    feedback.textContent = result.ok ? '' : result.error; feedback.dataset.error = String(!result.ok);
    sending = false; await refresh();
  }
  function menu(open) {
    document.getElementById('main-view').hidden = open;
    document.getElementById('url-view').hidden = !open;
    document.getElementById('matches').setAttribute('aria-expanded', String(open));
  }
  async function answer(kind) {
    const currentPrompt = promptId;
    sending = true;
    await refresh();
    const result = await ui.request({ kind, promptId: currentPrompt });
    feedback.textContent = result.ok ? 'Ответ отправлен. Ожидаем решение приложения.' : result.error;
    feedback.dataset.error = String(!result.ok);
    sending = false;
    await refresh();
  }
  document.getElementById('confirm').addEventListener('click', () => answer('confirm'));
  document.getElementById('decline').addEventListener('click', () => answer('decline'));
  document.getElementById('options').addEventListener('click', () => api.openOptions());
  document.getElementById('reconnect').addEventListener('click', async () => { await ui.request({ kind: 'reconnect' }); await refresh(); });
  document.getElementById('record-control').addEventListener('click', () => action({ kind: lastStatus?.recordingActive ? 'manual-stop' : 'manual-start' }));
  document.getElementById('open-app').addEventListener('click', () => action({ kind: 'open-app' }));
  document.getElementById('wait-switch').addEventListener('click', () => action({ kind: 'wait-switch' }));
  document.getElementById('select-controller').addEventListener('click', () => action({ kind: 'select-controller' }));
  document.getElementById('matches').addEventListener('click', () => menu(true));
  document.getElementById('back').addEventListener('click', () => menu(false));
  document.getElementById('close-all').addEventListener('click', () => action({ kind: 'close-all' }));
  void refresh();
  const timer = setInterval(refresh, 750);
  addEventListener('pagehide', () => clearInterval(timer), { once: true });
})();
