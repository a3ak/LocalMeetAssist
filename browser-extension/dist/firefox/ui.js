(() => {
  const labels = {
    not_configured: 'Нужна настройка', connecting: 'Подключение…', online: 'Приложение подключено',
    offline: 'Приложение недоступно', idle_connection: 'Ожидание встречи',
    forbidden: 'Токен отклонён', protocol_error: 'Несовместимый ответ приложения', unsupported_protocol: 'Протокол не поддерживается'
  };
  const states = { idle: 'Запись не запущена', recording: 'Идёт запись встречи',
    awaiting_confirmation: 'Обнаружена страница встречи. Начать запись?' };
  globalThis.LMA.ui = {
    labels, states,
    showStatus(data) {
      const port = document.getElementById('port-warning');
      if (port) {
        port.hidden = data.status !== 'online' || data.listenPort === null || data.listenPort === undefined || (data.listenPort !== 0 && data.listenPort === data.effectivePort);
        port.textContent = data.listenPort === 0 ? 'Порт приложения не закреплён. После его перезапуска соединение может быть потеряно.'
          : `Новый порт ${data.listenPort} вступит в силу после перезапуска приложения. Сейчас используется ${data.effectivePort}. Затем обновите порт расширения.`;
      }
      const notification = document.getElementById('notification-status');
      if (notification) {
        notification.hidden = !['denied', 'failed'].includes(data.notificationStatus);
        notification.textContent = data.notificationStatus === 'denied'
          ? 'Системные уведомления запрещены. Ответьте через значок LM; разрешение можно проверить в настройках.'
          : 'Браузер не смог создать уведомление. Ответьте через значок LM; проверка доступна в настройках.';
      }
      const icon = document.getElementById('brand-icon');
      if (icon) {
        const recording = data.status === 'online' && data.recordingActive === true;
        icon.src = recording ? 'icons/recording/128.png' : 'icons/128.png';
      }
      const mode = document.getElementById('recording-mode');
      if (mode) {
        mode.hidden = !data.mode || !['online', 'idle_connection'].includes(data.status);
        mode.textContent = data.mode === 'notify' ? 'Режим: спрашивать перед записью' : 'Режим: начинать запись автоматически';
      }
      const status = document.getElementById('connection-status');
      const detail = document.getElementById('connection-detail');
      status.textContent = labels[data.status] || 'Нет связи с расширением';
      status.dataset.status = data.status;
      detail.textContent = data.status === 'forbidden'
        ? 'Токен отклонён. Перегенерируйте токен в LocalMeetAssist → Интеграции → Браузер и сохраните его в настройках.'
        : data.status === 'not_configured' ? 'Укажите порт и plugin-токен локального приложения.'
        : data.status === 'offline' ? 'Запустите LocalMeetAssist и проверьте порт. Переподключение выполняется автоматически.'
        : data.status === 'unsupported_protocol' ? 'Приложение отклонило протокол 1. Проверьте версию LocalMeetAssist; затем измените порт или токен в настройках.'
        : data.status === 'protocol_error' ? 'Несовместимый ответ LocalMeetAssist. Требуется спецификация 1.0, протокол 1.'
        : data.compatible === false ? 'Нужен сервер по спецификации 1.0: протокол 1, служебные URL и состояние записи.'
        : data.enabled === false ? 'Интеграция выключена в LocalMeetAssist.'
        : data.recordingActive === null ? 'Состояние записи неизвестно. Ожидаем ответ приложения.'
        : data.recordingActive === true ? (data.origin ? 'Идёт запись по странице встречи' : 'Идёт запись в приложении')
        : (data.owner ? states[data.state] : ['awaiting_confirmation'].includes(data.state)
          ? 'Сессией управляет другой браузер или профиль.' : states[data.state]) || 'Ожидание состояния приложения';
    },
    async request(message) {
      try { return await globalThis.LMA.browserApi.sendMessage(message); }
      catch { return { ok: false, error: 'Фоновый процесс недоступен. Перезагрузите расширение.' }; }
    }
  };
})();
