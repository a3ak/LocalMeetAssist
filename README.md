# LocalMeetAssist

Local-first recording, transcription, speaker diarization, and meeting archive for Windows, macOS, and Linux.

[Русский](#русский) · [English](#english)

> **Development preview — 0.1.0.** LocalMeetAssist is under active development. Backward compatibility of configuration and data formats is not guaranteed yet.

![LocalMeetAssist calendar in the dark theme](docs/images/calendar-dark.png)

<p align="center"><sub>Current dark Web UI with demonstration data / Актуальная тёмная тема Web UI, демонстрационные данные</sub></p>

---

## Русский

### Что такое LocalMeetAssist

LocalMeetAssist записывает микрофон и системный звук в отдельные дорожки, создаёт смешанный поток, локально распознаёт речь и разделяет удалённых участников по голосам. Встречи, транскрипты, спикеры, протоколы и аудиофайлы связаны единым UID и доступны в локальном календаре.

Браузер используется только как интерфейс. Аудио захватывает само Go-приложение:

- Windows — WASAPI;
- macOS — CoreAudio + ScreenCaptureKit;
- Linux — PulseAudio/PipeWire либо ALSA.

Для захвата и локального инференса не требуются FFmpeg, Python, GigaAMGUI, `whisper-cli` или `sherpa-onnx`. Нативные библиотеки ONNX Runtime и whisper.cpp загружаются приложением напрямую.

### Возможности

- одновременная запись микрофона и системного звука в отдельные PCM16 WAV;
- создание `mixed.wav` и защита от акустического эха/дублирования реплик;
- сохранение WAV блоками на диск, а не целиком в оперативной памяти;
- локальная транскрибация через GigaAM v3 или Whisper;
- Silero VAD для выделения речи и устойчивой обработки длинных записей;
- диаризация через PyAnnote Segmentation + WeSpeaker + встроенную кластеризацию;
- назначение количества участников для конкретной встречи;
- переименование, объединение и перенос фрагментов спикеров в Web UI;
- локальный календарь, полнотекстовый поиск и фильтры по встрече, спикерам, содержанию и датам;
- импорт WAV, Opus и MP3 с автоматическим приведением к PCM16 mono 16 кГц;
- повтор отдельного этапа, отмена обработки и прогресс по каждому этапу;
- опциональное сжатие аудио в Opus после успешного завершения пайплайна;
- OpenAI-совместимая саммаризация с настраиваемым системным промтом и TLS;
- менеджер скачивания, проверки и переключения моделей;
- системный трей, горячие клавиши и индикация активной записи;
- уровни логирования, ротация файлов и диагностика из интерфейса;
- восстановление встреч после копирования каталога `data/meetings` из старой установки.

### Интерфейс

| Календарь и поиск | Карточка встречи |
| --- | --- |
| ![Calendar](docs/images/calendar-dark.png) | ![Meeting details](docs/images/meeting-details-dark.png) |

![LocalMeetAssist model manager](docs/images/models-dark.png)

Скриншоты созданы на демонстрационных данных и не содержат пользовательских встреч.

### Локальные модели

Модели и нативные runtime не входят в репозиторий. Их можно скачать и применить из раздела **«Модели»**. Загружается только выбранное пользователем; все модели распознавания одновременно не нужны.

| Назначение | Модель | Размер | Комментарий |
| --- | --- | ---: | --- |
| Русская транскрибация | GigaAM v3 E2E RNNT INT8 | ~227 МБ | Рекомендуемый вариант для русской речи, пунктуации и нормализации |
| Мультиязычная транскрибация | Whisper small Q5_1 | ~181 МБ | Самый быстрый вариант для слабых CPU |
| Мультиязычная транскрибация | Whisper medium Q5_0 | ~514 МБ | Баланс точности и скорости |
| Мультиязычная транскрибация | Whisper large-v3-turbo Q5_0 | ~574 МБ | Максимальное качество среди предлагаемых Whisper |
| Детектор речи | Silero VAD 6.2 ONNX | ~2,3 МБ | Удаляет тишину из обработки, помогает против повторов |
| Сегментация спикеров | PyAnnote Segmentation 3.0 ONNX | ~6 МБ | Определяет интервалы активности голосов |
| Голосовые признаки | WeSpeaker ResNet34-LM VoxCeleb | ~27 МБ | Сравнивает голосовые фрагменты перед кластеризацией |
| Выполнение ONNX | ONNX Runtime 1.23.2 | зависит от ОС | Нативная библиотека для GigaAM, VAD и диаризации |
| Выполнение Whisper | whisper.cpp runtime | зависит от ОС | Загружается только при выборе Whisper |

Полный каталог перечисленных моделей занимает около **1,6 ГБ**, плюс нативные runtime. Для обычной установки достаточно одной ASR-модели, VAD и двух моделей диаризации.

### Требования к компьютеру

Ниже приведены консервативные оценки для локальной записи, транскрибации и диаризации без модели саммаризации. Реальная скорость зависит от длительности встречи, числа потоков и выбранной ASR-модели.

| Профиль | CPU | RAM | Свободное место | Ожидаемый сценарий |
| --- | --- | ---: | ---: | --- |
| Минимальный | 4 современных ядра, x86-64 или ARM64 | 8 ГБ | 3 ГБ + место для записей | GigaAM или Whisper small; обработка может идти медленнее реального времени |
| Рекомендуемый | 8 современных ядер; Apple Silicon либо x86-64 с AVX2 | 16 ГБ | 5 ГБ + место для записей | GigaAM/Whisper medium, VAD и диаризация встреч продолжительностью 1–2 часа |
| Комфортный | 12+ ядер | 24–32 ГБ | SSD, 10+ ГБ + архив | Whisper large-v3-turbo, длинные встречи и параллельная работа с другими приложениями |

Дискретная GPU не обязательна. Текущие GigaAM, VAD и диаризация работают через CPU provider ONNX Runtime. Возможное ускорение whisper.cpp зависит от конкретного runtime и платформы и не является обязательным условием.

Исходный PCM16 mono 16 кГц занимает примерно **115 МБ на час для одной дорожки**. При записи микрофона, системного звука и mixed временно потребуется около **345 МБ на час**; Opus заметно уменьшает итоговый архив.

### Рекомендуемые модели для саммаризации

LocalMeetAssist пока не запускает LLM самостоятельно. Он обращается к любому серверу с OpenAI-совместимым `/v1/chat/completions`: llama.cpp, Ollama, LM Studio, vLLM или удалённому API. Память ниже указана ориентировочно и включает запас под контекст; длинный транскрипт увеличивает KV-cache.

| Модель | Подходящая конфигурация | Ориентир по памяти | Когда выбирать |
| --- | --- | ---: | --- |
| [Qwen3.5-9B-GGUF](https://huggingface.co/unsloth/Qwen3.5-9B-GGUF) `UD-Q4_K_XL` | 16 ГБ RAM или 8–12 ГБ VRAM | модель ~5,7 ГБ; желательно 10–14 ГБ доступной памяти | Рекомендуемый вариант по умолчанию: русский язык, хорошее следование структуре протокола |
| [Gemma 3 12B IT](https://huggingface.co/google/gemma-3-12b-it) Q4 | 16–24 ГБ RAM/VRAM | модель ~7–8 ГБ; желательно 12–18 ГБ | Сильное многоязычное резюмирование и большой контекст; требуется принять лицензию Gemma |
| [Mistral Small 3.2 24B Instruct](https://huggingface.co/mistralai/Mistral-Small-3.2-24B-Instruct-2506) Q4_K_M | 32 ГБ RAM или 16–24 ГБ VRAM | модель ~14,3 ГБ; желательно 22–28 ГБ | Более точные решения, задачи и формулировки на мощной рабочей станции |
| [Qwen3.5-35B-A3B-GGUF](https://huggingface.co/unsloth/Qwen3.5-35B-A3B-GGUF) `UD-Q4_K_XL` | 32 ГБ RAM или 24 ГБ VRAM | файл ~22,2 ГБ; желательно 28–32 ГБ | Качественная локальная саммаризация при наличии достаточной памяти |

Практический старт для llama.cpp:

```bash
llama-server -hf unsloth/Qwen3.5-9B-GGUF:UD-Q4_K_XL \
  --host 127.0.0.1 --port 8080 --ctx-size 32768
```

В настройках LocalMeetAssist укажите:

```text
URL API: http://127.0.0.1:8080/v1
Модель: unsloth/Qwen3.5-9B-GGUF:UD-Q4_K_XL
Токен: пусто, если локальный сервер не требует авторизации
```

Для полной часовой встречи 32K контекста может не хватить. В таком случае увеличьте `--ctx-size` в пределах доступной памяти либо используйте сервер, который поддерживает большой контекст.

### Сборка и запуск из исходников

Требуются **Go 1.24+**, C-компилятор и CGO.

```bash
cd meeting-assistant
cp config.example.toml config.toml
CGO_ENABLED=1 go run ./cmd/localmeetassist -config ./config.toml
```

После запуска адрес Web UI будет напечатан в консоли. По умолчанию используется случайный свободный порт на `127.0.0.1`.

#### macOS

Установите Command Line Tools:

```bash
xcode-select --install
```

Система запросит доступ к микрофону и записи системного аудио. Браузер не запрашивает выбор экрана, окна или вкладки.

#### Windows

Для `go run` нужен MinGW-w64 с GCC в `PATH`. Захват микрофона и системного звука выполняется через WASAPI.

#### Linux

Пример зависимостей для Debian/Ubuntu:

```bash
sudo apt install build-essential pkg-config libx11-dev libayatana-appindicator3-dev
```

Для PipeWire нужен PulseAudio-совместимый monitor-источник (`pipewire-pulse`). Конкретный набор пакетов зависит от дистрибутива и рабочего окружения.

### Первый запуск

1. Откройте **«Модели»** и скачайте ONNX Runtime.
2. Выберите и примените одну ASR-модель: GigaAM для русской речи или Whisper для мультиязычных встреч.
3. Скачайте Silero VAD.
4. Для диаризации скачайте PyAnnote Segmentation и WeSpeaker.
5. В **«Настройках»** включите только те этапы, которые должны выполняться автоматически.
6. Проверьте устройства, затем начните короткую тестовую запись.

Отключение автоматического этапа не запрещает запустить его вручную из карточки встречи.

### Данные и перенос встреч

По умолчанию все данные находятся в `./data`:

```text
data/
├── database/meetings.db
├── logs/localmeetassist.log
└── meetings/YYYY/MM/<meeting-uid>/
```

Чтобы перенести встречи из старой версии, остановите LocalMeetAssist, скопируйте каталоги встреч в новый `data/meetings`, затем запустите приложение. При старте оно сверит файловое дерево с bbolt, восстановит встречи, артефакты, транскрипты, саммари и спикеров. Если остался только Opus или MP3, LocalMeetAssist может восстановить рабочий WAV при повторной обработке.

### Приватность и сеть

- Запись, транскрибация, VAD и диаризация выполняются локально.
- Web UI слушает `127.0.0.1` и использует локальный session token для изменяющих запросов.
- Сеть нужна для загрузки моделей и для саммаризации, если выбран удалённый API.
- При локальном OpenAI-совместимом сервере весь основной пайплайн может оставаться на компьютере пользователя.

### Проверка

```bash
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go vet ./...
```

Дополнительная документация: [архитектура](docs/ARCHITECTURE.md), [API](docs/API.md), [тестирование](docs/TESTING.md).

### Лицензия

Код LocalMeetAssist распространяется по лицензии [MIT](LICENSE). Модели и нативные runtime имеют собственные лицензии; проверяйте условия перед распространением готовой сборки.

---

## English

### What is LocalMeetAssist?

LocalMeetAssist records the microphone and system audio into separate tracks, creates a mixed track, transcribes speech locally, and separates remote participants by voice. Meetings, transcripts, speakers, summaries, and audio artifacts share one UID and are available from a local calendar.

The browser is only the user interface. Audio capture runs in the Go application itself:

- Windows — WASAPI;
- macOS — CoreAudio + ScreenCaptureKit;
- Linux — PulseAudio/PipeWire or ALSA.

Capture and local inference do not require FFmpeg, Python, GigaAMGUI, `whisper-cli`, or `sherpa-onnx`. LocalMeetAssist loads ONNX Runtime and whisper.cpp native libraries directly.

### Highlights

- simultaneous microphone and system-audio capture into separate PCM16 WAV tracks;
- `mixed.wav` generation and acoustic echo/text-duplication protection;
- block-based disk recording instead of buffering an entire meeting in RAM;
- local GigaAM v3 or Whisper transcription;
- Silero VAD for speech filtering and stable long-recording processing;
- PyAnnote Segmentation + WeSpeaker diarization with built-in clustering;
- per-meeting participant count;
- speaker renaming, merging, and fragment reassignment in the Web UI;
- local calendar and phrase/key-based meeting search;
- WAV, Opus, and MP3 import with automatic PCM16 mono 16 kHz normalization;
- per-stage retry, cancellation, and progress reporting;
- optional Opus compression after a successful pipeline;
- OpenAI-compatible summarization with a configurable system prompt and TLS;
- model download, validation, and selection manager;
- system tray, global hotkeys, and recording-state indication;
- configurable logging levels, file rotation, and built-in diagnostics;
- startup recovery after copying an older `data/meetings` tree.

### UI

| Calendar and search | Meeting details |
| --- | --- |
| ![Calendar](docs/images/calendar-dark.png) | ![Meeting details](docs/images/meeting-details-dark.png) |

![LocalMeetAssist model manager](docs/images/models-dark.png)

The screenshots use demonstration data and contain no user meetings.

### Local models

Models and native runtimes are not committed to the repository. Download and apply them from **Models**. Only the selected ASR model is required at runtime.

| Purpose | Model | Size | Notes |
| --- | --- | ---: | --- |
| Russian ASR | GigaAM v3 E2E RNNT INT8 | ~227 MB | Recommended for Russian speech, punctuation, and normalization |
| Multilingual ASR | Whisper small Q5_1 | ~181 MB | Fastest option for lower-end CPUs |
| Multilingual ASR | Whisper medium Q5_0 | ~514 MB | Accuracy/speed balance |
| Multilingual ASR | Whisper large-v3-turbo Q5_0 | ~574 MB | Highest-quality bundled Whisper option |
| Voice activity detection | Silero VAD 6.2 ONNX | ~2.3 MB | Removes silence from inference and helps reduce repetitions |
| Speaker segmentation | PyAnnote Segmentation 3.0 ONNX | ~6 MB | Detects voice activity intervals |
| Speaker embeddings | WeSpeaker ResNet34-LM VoxCeleb | ~27 MB | Compares voice fragments before clustering |
| ONNX execution | ONNX Runtime 1.23.2 | platform-dependent | Used by GigaAM, VAD, and diarization |
| Whisper execution | whisper.cpp runtime | platform-dependent | Only needed when Whisper is selected |

All listed model files total roughly **1.6 GB**, plus native runtimes. A normal installation needs one ASR model, VAD, and the two diarization models.

### Hardware requirements

These are conservative estimates for recording, ASR, VAD, and diarization, excluding the summarization LLM. Throughput depends on meeting length, thread count, and the selected ASR model.

| Profile | CPU | RAM | Free storage | Intended use |
| --- | --- | ---: | ---: | --- |
| Minimum | 4 modern cores, x86-64 or ARM64 | 8 GB | 3 GB + recordings | GigaAM or Whisper small; inference may be slower than real time |
| Recommended | 8 modern cores; Apple Silicon or x86-64 with AVX2 | 16 GB | 5 GB + recordings | GigaAM/Whisper medium, VAD, and diarization for 1–2 hour meetings |
| Comfortable | 12+ cores | 24–32 GB | SSD, 10+ GB + archive | Whisper large-v3-turbo, long meetings, and multitasking |

A discrete GPU is not required. GigaAM, VAD, and diarization currently use the ONNX Runtime CPU provider. whisper.cpp acceleration depends on the platform runtime and is not a requirement.

PCM16 mono 16 kHz uses about **115 MB per hour per track**. Microphone, system, and mixed tracks may temporarily require about **345 MB per hour**; Opus greatly reduces the final archive size.

### Recommended summarization models

LocalMeetAssist does not manage an LLM process yet. It calls any OpenAI-compatible `/v1/chat/completions` server, such as llama.cpp, Ollama, LM Studio, vLLM, or a remote API. Memory figures are approximate and include practical headroom; longer transcripts need a larger KV cache.

| Model | Suggested machine | Memory guidance | Best fit |
| --- | --- | ---: | --- |
| [Qwen3.5-9B-GGUF](https://huggingface.co/unsloth/Qwen3.5-9B-GGUF) `UD-Q4_K_XL` | 16 GB RAM or 8–12 GB VRAM | ~5.7 GB model; 10–14 GB available recommended | Default recommendation: Russian language and reliable structured summaries |
| [Gemma 3 12B IT](https://huggingface.co/google/gemma-3-12b-it) Q4 | 16–24 GB RAM/VRAM | ~7–8 GB model; 12–18 GB available recommended | Strong multilingual summaries and long context; Gemma license acceptance required |
| [Mistral Small 3.2 24B Instruct](https://huggingface.co/mistralai/Mistral-Small-3.2-24B-Instruct-2506) Q4_K_M | 32 GB RAM or 16–24 GB VRAM | ~14.3 GB model; 22–28 GB available recommended | Better decisions, action items, and wording on a workstation |
| [Qwen3.5-35B-A3B-GGUF](https://huggingface.co/unsloth/Qwen3.5-35B-A3B-GGUF) `UD-Q4_K_XL` | 32 GB RAM or 24 GB VRAM | ~22.2 GB file; 28–32 GB available recommended | Higher-quality local summaries when enough memory is available |

Quick llama.cpp example:

```bash
llama-server -hf unsloth/Qwen3.5-9B-GGUF:UD-Q4_K_XL \
  --host 127.0.0.1 --port 8080 --ctx-size 32768
```

LocalMeetAssist settings:

```text
API URL: http://127.0.0.1:8080/v1
Model: unsloth/Qwen3.5-9B-GGUF:UD-Q4_K_XL
Token: leave empty if the local server does not require authentication
```

A full one-hour transcript may exceed 32K context. Increase `--ctx-size` within available memory or use a server/model configuration that supports a larger context.

### Build and run from source

LocalMeetAssist requires **Go 1.24+**, a C compiler, and CGO.

```bash
cd meeting-assistant
cp config.example.toml config.toml
CGO_ENABLED=1 go run ./cmd/localmeetassist -config ./config.toml
```

The console prints the Web UI address. By default LocalMeetAssist binds to a random free port on `127.0.0.1`.

#### macOS

Install Command Line Tools:

```bash
xcode-select --install
```

macOS asks for microphone and system-audio recording permissions. The browser never asks you to select a screen, window, or tab.

#### Windows

`go run` requires MinGW-w64 with GCC available in `PATH`. LocalMeetAssist captures microphone and system audio through WASAPI.

#### Linux

Debian/Ubuntu example:

```bash
sudo apt install build-essential pkg-config libx11-dev libayatana-appindicator3-dev
```

PipeWire requires a PulseAudio-compatible monitor source (`pipewire-pulse`). Exact packages depend on the distribution and desktop environment.

### First run

1. Open **Models** and download ONNX Runtime.
2. Download and apply one ASR model: GigaAM for Russian or Whisper for multilingual meetings.
3. Download Silero VAD.
4. For diarization, download PyAnnote Segmentation and WeSpeaker.
5. Enable only the stages that should run automatically in **Settings**.
6. Verify audio devices and create a short test recording.

Disabling an automatic stage does not disable its manual action in a meeting card.

### Data and migration

LocalMeetAssist stores data under `./data` by default:

```text
data/
├── database/meetings.db
├── logs/localmeetassist.log
└── meetings/YYYY/MM/<meeting-uid>/
```

To move meetings from an older installation, stop LocalMeetAssist, copy meeting directories into the new `data/meetings`, and start the application. Startup reconciliation restores meetings, artifacts, transcripts, summaries, and speakers into bbolt. If only Opus or MP3 remains, reprocessing can restore a working WAV copy.

### Privacy and networking

- Recording, ASR, VAD, and diarization run locally.
- The Web UI binds to `127.0.0.1` and uses a local session token for mutating requests.
- Network access is needed to download models and when a remote summarization API is configured.
- With a local OpenAI-compatible LLM server, the complete workflow can remain on the user's machine.

### Verification

```bash
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go vet ./...
```

Additional documentation: [architecture](docs/ARCHITECTURE.md), [API](docs/API.md), and [testing](docs/TESTING.md).

### License

LocalMeetAssist source code is released under the [MIT License](LICENSE). Models and native runtimes have their own licenses; review them before redistributing a packaged build.
