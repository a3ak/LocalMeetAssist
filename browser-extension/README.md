# LocalMeetAssist Browser Extensions 1.4.0

Расширения Chrome/Edge и Firefox для локального LocalMeetAssist. Одна кодовая база WebExtensions,
две сборки Manifest V3, готовые файлы установки и проект для GitHub. Расширение не записывает аудио.

**Сервер: опубликованная спецификация 1.0, protocol_version 1.** Совместимость описана в
[SERVER_CHANGES.md](docs/SERVER_CHANGES.md), полный контракт — [BROWSER_PLUGIN.md](docs/BROWSER_PLUGIN.md).
Сервер приложения в архив не входит. При несовместимом контракте действия отключены и показано сообщение о версии протокола.

## Установка

Node.js для установки не нужен.

- Chrome/Edge: распаковать проект, открыть `chrome://extensions` / `edge://extensions`, включить режим разработчика,
  выбрать «Загрузить распакованное расширение» → `dist/chrome`.
- Firefox: `about:debugging#/runtime/this-firefox` → «Загрузить временное дополнение» → `dist/firefox/manifest.json`.
  Временная установка действует до перезапуска; постоянная требует подписи Mozilla.
- Открыть настройки расширения, сохранить закреплённый порт приложения и plugin-токен `lma_pl_…`.
- Открыть новую вкладку встречи. Уже открытые страницы при первом запуске считаются устаревшими.

Подробная инструкция и обновление: [INSTALL.md](INSTALL.md).

## Возможности

- Кружок ручного старта или квадрат завершения любой текущей записи; работают через служебные URL в `tabs`.
- Глобус `www` открывает `http://127.0.0.1:<настроенный порт>/`.
- Глобальный статус записи от приложения в событиях и heartbeat; без связи состояние неизвестно.
- Исходный URL/GLOB встречи, управляющая вкладка и передача управления на любую активную вкладку.
- Ожидание переключения на 60 секунд с обновляемым отсчётом.
- После полного перезапуска браузера: восстановление той же записи через ожидание управления 60 с, если сервер ещё сохраняет её и владельца.
- Потеря плагина: сервер останавливает браузерную запись через своё окно; ручные записи продолжаются.
- Новая страница того же GLOB до истечения задержки остановки продолжает ту же запись.
- Старые дубликаты не подхватывают управление. F5 сохраняет состояние; новый URL — новый вариант.
- Отказ исключает текущие открытые варианты, новая страница снова вызывает вопрос после задержки.
- Завершение записи в приложении или плагине делает текущие подходящие страницы устаревшими.
- Второй экран popup: URL по GLOB, отметки источника и управляющей вкладки, закрытие одной/всех подходящих вкладок.
- Локальные настройки: возраст страницы 30–300 с (180), задержка обнаружения 0–30 с (3), задержка остановки 0–300 с (5).
- Задержка остановки управляет только собственной браузерной записью; ручная запись приложения не завершается при уходе со страницы.
- LM-иконка приложения, красный индикатор записи, жёлтый `?`, native-уведомление и светлая/тёмная тема.
- Токен в `storage.local`, временная история в `storage.session`, нет аудиодоступа, аналитики и удалённых сервисов.

## Требования

Chrome/Edge 140+, Firefox 140+, LocalMeetAssist с WebSocket protocol 1 по спецификации 1.0 на закреплённом loopback-порту.
Node.js 22+ (рекомендуется 24) нужен только для разработки/сборки; npm-зависимостей нет.
HTTPS-only/WSS-only слушатель этой версии не соответствует: используется `ws://127.0.0.1:<port>/api/v1/ws`.

## Разработка

```sh
npm run build
npm run check
npm test
npm run package
```

Последняя команда делает все проверки и создаёт:

- `artifacts/localmeetassist-chrome-1.4.0.zip`;
- `artifacts/localmeetassist-firefox-1.4.0.zip`;
- `artifacts/LocalMeetAssist_Browser_Extensions_1.4.0.zip`.

В общем ZIP: `src/`, `dist/chrome`, `dist/firefox`, browser ZIP в `packages/`, manifests, tests, scripts, docs,
MIT LICENSE и GitHub Actions. Реальные токены, `.git`, `node_modules`, `artifacts` туда не попадают.
Редактировать `src/`, затем пересобирать `dist/`. Сборка без внешних зависимостей и минификации.

## GitHub

Содержимое распакованной папки `localmeetassist-browser-extension` — корень репозитория.
Загрузите её файлы в новый репозиторий либо:

```sh
git init
git add .
git commit -m "LocalMeetAssist browser extensions 1.4.0"
git branch -M main
git remote add origin <URL_ВАШЕГО_РЕПОЗИТОРИЯ>
git push -u origin main
```

CI проверяет проект и сохраняет ZIP; автоматически не публикует расширения в магазинах.
Не публикуйте настоящие токены или параметры приглашений из URL.

## Документация

- [Спецификация 1.0](docs/BROWSER_PLUGIN.md) и [совместимость с сервером](docs/SERVER_CHANGES.md).
- [Обнаружение и управление вкладками](docs/DETECTION.md), [протокол](docs/PROTOCOL.md).
- [Тестовый стенд и ручная приёмка](docs/TESTING.md), [результаты проверки](docs/VALIDATION.md).
- [Приватность](PRIVACY.md), [MIT](LICENSE).

## English

LocalMeetAssist 1.4.0 companion extensions for Chrome/Edge and Firefox. Shared dependency-free WebExtensions
codebase, separate MV3 builds. Requires published server specification 1.0 (wire protocol 1); see `docs/SERVER_CHANGES.md`.
Manual start/stop use reserved URLs in authenticated `tabs` messages. The server publishes global recording state;
the extension tracks fresh meeting pages, a controlling tab and a 60-second transfer window. It never captures audio.
Ready builds: `dist/chrome`, `dist/firefox`; packaged browser ZIPs: `packages/`.
A full browser restart restores the same owned recording into a 60-second transfer window. Restored pages remain stale.
The server stops a missing browser-owned recording after its reconnect grace period; manual recordings remain active.
For installation see `INSTALL.md`; permanent Firefox installation requires Mozilla signing.
Run `npm run package` on Node.js 22+ to build, verify, test and package. MIT license.

Stop delay is a local extension setting: integer 0–300 seconds, default 5. It applies only to the owned browser recording.
Server config does not supply this setting. Reconnect uses the minimum supported server N=2; see docs/PROTOCOL.md.
