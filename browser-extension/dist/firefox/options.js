(() => {
  const { browserApi: api, ui } = globalThis.LMA;
  const form = document.getElementById('settings-form');
  const token = document.getElementById('token');
  const port = document.getElementById('port');
  const maxAge = document.getElementById('max-age');
  const delay = document.getElementById('detection-delay');
  const stopDelay = document.getElementById('stop-delay');
  const feedback = document.getElementById('feedback');
  const show = (text, error = false) => { feedback.textContent = text; feedback.dataset.error = String(error); };
  async function refresh() { const data = await ui.request({ kind: 'status' }); if (data.ok) ui.showStatus(data); }
  api.storageGet('settings').then(data => {
    port.value = data.settings?.port || ''; token.value = data.settings?.token || '';
    maxAge.value = data.settings?.maxAgeSeconds ?? 180; delay.value = data.settings?.detectionDelaySeconds ?? 3;
    stopDelay.value = data.settings?.stopDelaySeconds ?? 5;
  });
  document.getElementById('version').textContent = 'Расширение ' + api.version;
  form.addEventListener('submit', async event => {
    event.preventDefault();
    const button = form.querySelector('button[type=submit]');
    button.disabled = true;
    const result = await ui.request({ kind: 'save-settings', port: port.value, token: token.value.trim(),
      maxAgeSeconds: maxAge.value, detectionDelaySeconds: delay.value, stopDelaySeconds: stopDelay.value });
    show(result.ok ? result.changed ? 'Настройки сохранены.' : 'Настройки не изменились.' : result.error, !result.ok);
    button.disabled = false;
    await refresh();
  });
  document.getElementById('reveal').addEventListener('click', event => {
    const visible = token.type === 'password';
    token.type = visible ? 'text' : 'password';
    event.currentTarget.textContent = visible ? 'Скрыть' : 'Показать';
    event.currentTarget.setAttribute('aria-pressed', String(visible));
  });
  document.getElementById('clear').addEventListener('click', async () => {
    const result = await ui.request({ kind: 'clear-settings' });
    if (result.ok) { token.value = ''; port.value = ''; maxAge.value = 180; delay.value = 3; stopDelay.value = 5; show('Настройки удалены. Запись по странице завершит приложение после потери связи; ручная запись продолжится.'); }
    else show(result.error, true);
    await refresh();
  });
  document.getElementById('reconnect').addEventListener('click', async () => {
    const result = await ui.request({ kind: 'reconnect' });
    show(result.ok ? 'Проверяем соединение…' : result.error, !result.ok);
    await refresh();
  });
  void refresh();
  document.getElementById('test-notification').addEventListener('click', async event => {
    const button = event.currentTarget, output = document.getElementById('notification-test-result');
    button.disabled = true;
    const result = await ui.request({ kind: 'test-notification' });
    output.textContent = result.ok ? 'Уведомление передано ОС. Если баннер не появился, проверьте разрешение уведомлений для браузера и режим «Не беспокоить».' : result.error;
    output.dataset.error = String(!result.ok);
    button.disabled = false;
  });
  const timer = setInterval(refresh, 1000);
  addEventListener('pagehide', () => clearInterval(timer), { once: true });
})();
