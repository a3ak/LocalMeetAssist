package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/pipeline"
)

type settingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type settingField struct {
	Key          string          `json:"key"`
	Label        string          `json:"label"`
	Description  string          `json:"description"`
	Section      string          `json:"section,omitempty"`
	Kind         string          `json:"kind"`
	Value        string          `json:"value"`
	Options      []settingOption `json:"options,omitempty"`
	Integer      bool            `json:"integer,omitempty"`
	SecretSet    bool            `json:"secret_set,omitempty"`
	DefaultValue string          `json:"default_value,omitempty"`
	Resettable   bool            `json:"resettable,omitempty"`
}

type settingGroup struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Fields      []settingField `json:"fields"`
}

func textSetting(key, label, description, value string) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "text", Value: value}
}
func hotkeySetting(key, label, description, value string) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "hotkey", Value: value}
}
func textareaSetting(key, label, description, value, defaultValue string, resettable bool) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "textarea", Value: value, DefaultValue: defaultValue, Resettable: resettable}
}
func secretSetting(key, label, description string, set bool) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "password", SecretSet: set}
}
func boolSetting(key, label, description string, value bool) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "checkbox", Value: strconv.FormatBool(value)}
}
func intSetting(key, label, description string, value int) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "number", Value: strconv.Itoa(value), Integer: true}
}
func floatSetting(key, label, description string, value float64) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "number", Value: strconv.FormatFloat(value, 'f', -1, 64)}
}
func selectSetting(key, label, description, value string, options ...settingOption) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "select", Value: value, Options: options}
}
func selectIntSetting(key, label, description string, value int, options ...settingOption) settingField {
	return settingField{Key: key, Label: label, Description: description, Kind: "select", Value: strconv.Itoa(value), Options: options, Integer: true}
}
func option(value, label string) settingOption { return settingOption{Value: value, Label: label} }

func inSection(section string, fields ...settingField) []settingField {
	for index := range fields {
		fields[index].Section = section
	}
	return fields
}

func sectionSetting(section string, field settingField) settingField {
	field.Section = section
	return field
}

func joinFields(groups ...[]settingField) []settingField {
	var result []settingField
	for _, fields := range groups {
		result = append(result, fields...)
	}
	return result
}

func siblingModelOptions(current string, names ...settingOption) []settingOption {
	dir := filepath.Dir(current)
	prefixDot := strings.HasPrefix(current, "."+string(filepath.Separator))
	options := make([]settingOption, 0, len(names)+1)
	known := false
	for _, item := range names {
		value := filepath.Join(dir, item.Value)
		if prefixDot && !strings.HasPrefix(value, "."+string(filepath.Separator)) {
			value = "." + string(filepath.Separator) + value
		}
		options = append(options, option(value, item.Label))
		known = known || value == current
	}
	if current != "" && !known {
		options = append([]settingOption{option(current, "Текущая пользовательская модель")}, options...)
	}
	return options
}

func summaryPromptValue(cfg config.Config) string {
	if value := strings.TrimSpace(cfg.Summary.SystemPrompt); value != "" {
		return value
	}
	if path := strings.TrimSpace(cfg.Summary.PromptFile); path != "" {
		if !filepath.IsAbs(path) && cfg.ConfigFile != "" {
			path = filepath.Join(filepath.Dir(cfg.ConfigFile), path)
		}
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
	}
	return config.DefaultSummaryPrompt
}

// settingsSchema describes every editable setting, localized to the
// configured interface language.
func settingsSchema(cfg config.Config) []settingGroup {
	groups := []settingGroup{
		{ID: "app", Title: "Приложение", Description: "Интерфейс, каталог данных и локальный HTTP-сервер.", Fields: []settingField{
			selectSetting("app.language", "Язык интерфейса", "Язык интерфейса LocalMeetAssist. Применяется после перезагрузки страницы.", cfg.App.Language, option("ru", "Русский"), option("en", "English")),
			selectSetting("app.theme", "Тема оформления", "Цветовая тема интерфейса. Применяется сразу в этом браузере.", cfg.App.Theme, option("dark", "Тёмная"), option("light", "Светлая")),
			textSetting("app.microphone_owner_name", "Владелец микрофона", "Имя локального участника, автоматически используемое в новых встречах. Пусто — значение по языку интерфейса.", cfg.App.MicrophoneOwnerName),
			textSetting("app.data_dir", "Каталог данных", "База, записи, транскрипты и логи.", cfg.App.DataDir),
			textSetting("app.listen_host", "Адрес", "Разрешён только loopback: 127.0.0.1, localhost или ::1.", cfg.App.ListenHost),
			intSetting("app.listen_port", "Порт", "0 — выбрать свободный порт автоматически.", cfg.App.ListenPort),
			boolSetting("app.open_browser", "Открывать браузер", "Открыть UI после запуска LocalMeetAssist.", cfg.App.OpenBrowser),
		}},
		{ID: "audio", Title: "Аудио", Description: "Нативная запись микрофона и системного звука.", Fields: []settingField{
			selectSetting("audio.backend", "Backend", "Запись выполняется встроенным нативным backend.", cfg.Audio.Backend, option("native", "Нативный")),
			selectIntSetting("audio.sample_rate", "Частота", "Частота WAV в герцах.", cfg.Audio.SampleRate, option("8000", "8 кГц"), option("16000", "16 кГц"), option("24000", "24 кГц"), option("48000", "48 кГц")),
			selectIntSetting("audio.channels", "Каналы", "Для распознавания рекомендуется моно.", cfg.Audio.Channels, option("1", "Моно"), option("2", "Стерео")),
			intSetting("audio.block_ms", "Размер блока, мс", "Размер блока записи на диск.", cfg.Audio.BlockMS),
			textSetting("audio.input_mode", "Режим микрофона", "Обычно auto.", cfg.Audio.InputMode),
			textSetting("audio.input_device_name", "Имя микрофона", "Системное имя; пусто — устройство по умолчанию.", cfg.Audio.InputDeviceName),
			textSetting("audio.input_device_id", "ID микрофона", "Используйте только если выбор по имени неоднозначен.", cfg.Audio.InputDeviceID),
			textSetting("audio.output_mode", "Режим системного звука", "Обычно native.", cfg.Audio.OutputMode),
			textSetting("audio.output_device_name", "Имя системного источника", "Системное имя; пусто — источник по умолчанию.", cfg.Audio.OutputDeviceName),
			textSetting("audio.output_device_id", "ID системного источника", "Используйте только если выбор по имени неоднозначен.", cfg.Audio.OutputDeviceID),
			floatSetting("audio.microphone_gain", "Усиление микрофона", "Множитель при создании общего потока.", cfg.Audio.MicrophoneGain),
			floatSetting("audio.system_gain", "Усиление системного звука", "Множитель при создании общего потока.", cfg.Audio.SystemGain),
			boolSetting("audio.normalize", "Нормализация", "Разрешить нормализацию при подготовке звука.", cfg.Audio.Normalize),
			selectSetting("audio.echo_cancellation", "Подавление эха", "WebRTC AEC3 очищает микрофон от звука, пришедшего из динамиков. Исходный WAV всегда сохраняется; auto обрабатывает только при обнаруженном эхо.", cfg.Audio.EchoCancellation, option("auto", "Автоматически"), option("on", "Всегда"), option("off", "Выключено")),
			intSetting("audio.echo_delay_ms", "Задержка эха, мс", "Ориентировочная задержка между системным звуком и его попаданием в микрофон.", cfg.Audio.EchoDelayMS),
		}},
		{ID: "desktop", Title: "Трей и горячие клавиши", Description: "Быстрое управление записью без открытого браузера. Обычные сочетания на macOS не требуют Accessibility; результат регистрации записывается в журнал.", Fields: []settingField{
			boolSetting("desktop.tray_enabled", "Иконка в трее", "Показывать LocalMeetAssist в системном трее. Во время записи красный индикатор появляется в нижней правой четверти иконки.", cfg.Desktop.TrayEnabled),
			hotkeySetting("desktop.toggle_record", "Одна клавиша старт/стоп", "Нажмите «Записать сочетание», затем нужные клавиши. Оставьте пустым, если нужны отдельные комбинации.", cfg.Desktop.ToggleRecord),
			hotkeySetting("desktop.start_recording", "Начать запись", "Отдельная глобальная комбинация для начала записи.", cfg.Desktop.StartRecording),
			hotkeySetting("desktop.stop_recording", "Остановить запись", "Отдельная глобальная комбинация для остановки записи.", cfg.Desktop.StopRecording),
			hotkeySetting("desktop.open_ui", "Открыть UI", "Необязательная глобальная комбинация для открытия Web UI.", cfg.Desktop.OpenUI),
		}},
		{ID: "inference", Title: "ONNX Runtime", Description: "Нативная библиотека нейросетевого вывода. LocalMeetAssist загружает её напрямую; CLI и Python не нужны.", Fields: []settingField{
			textSetting("inference.runtime_path", "Библиотека или каталог", "Можно указать готовый .dll/.dylib/.so вручную. Для каталога LocalMeetAssist выбирает файл по ОС и архитектуре.", cfg.Inference.RuntimePath),
			boolSetting("inference.auto_download", "Скачивать автоматически", "При отсутствии библиотеки скачать официальный CPU runtime при запуске приложения.", cfg.Inference.AutoDownload),
			textSetting("inference.runtime_version", "Версия", "Зафиксированная версия ONNX Runtime; для автоматической загрузки поддерживается 1.23.2.", cfg.Inference.RuntimeVersion),
			textSetting("inference.whisper_runtime_path", "Whisper runtime", "Каталог нативных библиотек whisper.cpp; устанавливается из раздела «Модели» без CLI.", cfg.Inference.WhisperRuntimePath),
		}},
		{ID: "transcription", Title: "Транскрибация", Description: "GigaAM v3 или Whisper выполняются непосредственно в LocalMeetAssist, без запуска CLI.", Fields: []settingField{
			boolSetting("transcription.auto_run", "Выполнять автоматически", "Автоматически распознавать встречу после остановки. Ручной запуск доступен независимо от этой настройки.", cfg.Transcription.AutoRun),
			selectSetting("transcription.engine", "Движок", "Выбранная модель вызывается нативно из процесса LocalMeetAssist.", cfg.Transcription.Engine, option("gigaam-onnx", "GigaAM v3 ONNX"), option("whispercpp-native", "Whisper.cpp native")),
			textSetting("transcription.model_path", "Модель", "Каталог GigaAM или файл Whisper GGML; удобнее скачать и применить модель в разделе «Модели».", cfg.Transcription.ModelPath),
			textSetting("transcription.language", "Язык", "Код языка, например ru.", cfg.Transcription.Language),
			intSetting("transcription.threads", "Потоки CPU", "Количество потоков распознавания; 0 — выбрать автоматически.", cfg.Transcription.Threads),
			intSetting("transcription.timeout_seconds", "Таймаут, сек", "Максимальное время обработки источника.", cfg.Transcription.TimeoutSeconds),
			intSetting("transcription.chunk_seconds", "Максимальный речевой фрагмент, сек", "Silero VAD разделяет найденную речь на независимые фрагменты не длиннее этого значения.", cfg.Transcription.ChunkSeconds),
			selectSetting("transcription.source_mode", "Источники", "auto — отдельные потоки с резервом mixed; mixed — итог только из общего потока; separate — без резерва.", cfg.Transcription.SourceMode, option("auto", "Автоматически"), option("mixed", "Всегда mixed"), option("separate", "Только раздельно")),
			boolSetting("transcription.mixed_fallback_enabled", "Fallback на mixed", "Использовать общий поток при ошибке, пустом или повторяющемся системном транскрипте.", cfg.Transcription.MixedFallbackEnabled),
			boolSetting("transcription.echo_dedup_enabled", "Удалять эхо", "Удалять совпавшие реплики из микрофонного потока.", cfg.Transcription.EchoDedupEnabled),
			intSetting("transcription.echo_time_tolerance_ms", "Допуск времени эха, мс", "Максимальное расхождение реплик по времени.", cfg.Transcription.EchoTimeToleranceMS),
			floatSetting("transcription.echo_text_similarity", "Сходство текста", "Порог 0–1 для определения дубликата.", cfg.Transcription.EchoTextSimilarity),
			boolSetting("transcription.vad_enabled", "VAD", "Отделять речь от тишины до распознавания.", cfg.Transcription.VADEnabled),
			textSetting("transcription.vad_model_path", "Модель VAD", "Путь к модели Silero VAD.", cfg.Transcription.VADModelPath),
			floatSetting("transcription.vad_threshold", "Порог VAD", "Вероятность речи 0–1.", cfg.Transcription.VADThreshold),
			intSetting("transcription.vad_min_speech_ms", "Минимальная речь, мс", "Короткие фрагменты ниже значения отбрасываются.", cfg.Transcription.VADMinSpeechMS),
			intSetting("transcription.vad_min_silence_ms", "Минимальная пауза, мс", "Пауза для разделения речевых фрагментов.", cfg.Transcription.VADMinSilenceMS),
			boolSetting("transcription.suppress_non_speech", "Подавлять неречевые фрагменты", "Не передавать VAD-тишину в модель речи.", cfg.Transcription.SuppressNonSpeech),
			boolSetting("transcription.no_fallback", "Строгий режим", "Не создавать текст при пустом результате модели.", cfg.Transcription.NoFallback),
			boolSetting("transcription.silence_filter", "RMS-фильтр тишины", "Дополнительная фильтрация очень тихих фрагментов.", cfg.Transcription.SilenceFilter),
			floatSetting("transcription.min_segment_rms", "Минимальный RMS", "Сегменты тише этого значения считаются тишиной.", cfg.Transcription.MinSegmentRMS),
		}},
		{ID: "diarization", Title: "Диаризация", Description: "PyAnnote Segmentation 3 + WeSpeaker и кластеризация голосов на Go.", Fields: []settingField{
			boolSetting("diarization.auto_run", "Выполнять автоматически", "Автоматически разделять удалённых участников на спикеров. Ручной запуск доступен всегда.", cfg.Diarization.AutoRun),
			selectSetting("diarization.engine", "Движок", "Выполняется в процессе LocalMeetAssist без CLI.", cfg.Diarization.Engine, option("pyannote-wespeaker-onnx", "PyAnnote + WeSpeaker ONNX")),
			textSetting("diarization.segmentation_model", "Модель сегментации", "Путь к segmentation.onnx.", cfg.Diarization.SegmentationModel),
			textSetting("diarization.embedding_model", "Модель голосовых признаков", "Путь к WeSpeaker ResNet34-LM ONNX.", cfg.Diarization.EmbeddingModel),
			floatSetting("diarization.cluster_threshold", "Порог косинусного расстояния", "Без заданного количества участников голоса объединяются, пока расстояние не превышает порог.", cfg.Diarization.ClusterThreshold),
			intSetting("diarization.max_auto_speakers", "Максимум при auto", "Защита от появления десятков ложных спикеров.", cfg.Diarization.MaxAutoSpeakers),
			intSetting("diarization.chunk_seconds", "Размер блока (совместимость)", "Новая модель сама работает окнами 10 секунд с перекрытием; значение не используется.", cfg.Diarization.ChunkSeconds),
			intSetting("diarization.chunk_overlap_seconds", "Перекрытие (совместимость)", "Новая модель использует встроенное перекрытие 5 секунд.", cfg.Diarization.ChunkOverlapSeconds),
			boolSetting("diarization.microphone_enabled", "Диаризовать микрофон", "Обычно выключено: владелец определяется по отдельному потоку.", cfg.Diarization.MicrophoneEnabled),
			intSetting("diarization.timeout_seconds", "Таймаут, сек", "Максимальное время диаризации.", cfg.Diarization.TimeoutSeconds),
			intSetting("diarization.num_threads", "Потоки CPU", "Потоки ONNX Runtime для сегментации и извлечения голосовых признаков; 0 — значение движка.", cfg.Diarization.NumThreads),
		}},
		{ID: "summary", Title: "Саммаризация", Description: "OpenAI-совместимая модель протоколирования.", Fields: []settingField{
			boolSetting("summary.auto_run", "Выполнять автоматически", "Автоматически создавать протокол после готового транскрипта. Ручной запуск доступен всегда.", cfg.Summary.AutoRun),
			selectSetting("summary.language", "Язык протокола", "Язык ответа саммари. По умолчанию — как язык интерфейса.", cfg.Summary.Language, option("", "Как язык интерфейса"), option("ru", "Русский"), option("en", "English")),
			textSetting("summary.base_url", "URL API", "Например http://127.0.0.1:8080/v1.", cfg.Summary.BaseURL),
			textSetting("summary.model", "Модель", "Имя модели в OpenAI-совместимом API.", cfg.Summary.Model),
			secretSetting("summary.token", "Токен", "Оставьте пустым, чтобы не изменять сохранённый токен.", cfg.Summary.Token != ""),
			textSetting("summary.token_env", "Переменная токена", "Переменная окружения с токеном.", cfg.Summary.TokenEnv),
			intSetting("summary.timeout_seconds", "Таймаут, сек", "Таймаут запроса к модели.", cfg.Summary.TimeoutSeconds),
			intSetting("summary.max_retries", "Повторы", "Количество повторных запросов при временной ошибке.", cfg.Summary.MaxRetries),
			textareaSetting("summary.system_prompt", "Системный промт", "Инструкция модели для формирования протокола. Кнопка сброса возвращает встроенный безопасный шаблон.", summaryPromptValue(cfg), config.DefaultSummaryPrompt, true),
			textSetting("summary.prompt_file", "Файл промта (совместимость)", "Используется только когда системный промт пуст. Новые настройки удобнее задавать полем выше.", cfg.Summary.PromptFile),
			boolSetting("summary.tls_verify", "Проверять TLS", "Проверять сертификат сервера.", cfg.Summary.TLSVerify),
			textSetting("summary.tls_ca_file", "CA-файл", "Дополнительный доверенный CA PEM.", cfg.Summary.TLSCAFile),
			textSetting("summary.tls_server_name", "TLS server name", "Переопределение имени сертификата.", cfg.Summary.TLSServerName),
		}},
		{ID: "storage", Title: "Хранение", Description: "Имена файлов, Ogg/Opus и политика исходных WAV.", Fields: []settingField{
			textSetting("storage.file_name_template", "Шаблон имени", "Поддерживает date, time, title_slug, uid_short и artifact_type в двойных фигурных скобках.", cfg.Storage.FileNameTemplate),
			textSetting("storage.date_format", "Формат даты", "Формат Go time, например 2006-01-02.", cfg.Storage.DateFormat),
			textSetting("storage.time_format", "Формат времени", "Формат Go time, например 15-04-05.", cfg.Storage.TimeFormat),
			selectSetting("storage.audio_after_processing", "После обработки", "По умолчанию WAV сохраняются. opus — сохранить общий Ogg/Opus и удалить WAV только после успешного пайплайна; delete — удалить звук.", cfg.Storage.AudioAfterProcessing, option("wav", "Все WAV (по умолчанию)"), option("opus", "Ogg/Opus"), option("delete", "Удалить звук"), option("mp3", "MP3 (внешний encoder)")),
			intSetting("storage.opus_bitrate_kbps", "Opus, кбит/с", "Битрейт встроенного Go-кодировщика.", cfg.Storage.OpusBitrateKbps),
			intSetting("storage.mp3_bitrate_kbps", "MP3, кбит/с", "Используется только для политики mp3.", cfg.Storage.MP3BitrateKbps),
			textSetting("storage.encoder_command", "MP3 encoder", "Путь к внешнему LAME; для Opus не нужен.", cfg.Storage.EncoderCommand),
			boolSetting("storage.keep_source_on_failure", "Хранить WAV при ошибке", "Не удалять исходники, если обработка не завершилась.", cfg.Storage.KeepSourceOnFailure),
			textSetting("storage.database_path", "База bbolt", "Путь к локальной базе встреч.", cfg.Storage.DatabasePath),
			intSetting("storage.backup_count", "Количество резервных копий", "Количество сохраняемых копий базы.", cfg.Storage.BackupCount),
		}},
		{ID: "models", Title: "Файлы моделей", Description: "Источники прямых ONNX-моделей и контрольные суммы. Комплект можно скачать и применить в разделе «Модели».", Fields: joinFields(
			inSection("Общие параметры",
				intSetting("models.download_timeout_seconds", "Таймаут загрузки, сек", "Максимальная длительность одной загрузки.", cfg.Models.DownloadTimeoutSeconds),
			),
			inSection("Перевод речи в текст",
				textSetting("models.gigaam_encoder_url", "GigaAM encoder URL", "INT8 encoder — основная и самая крупная часть модели.", cfg.Models.GigaAMEncoderURL),
				textSetting("models.gigaam_encoder_sha256", "Encoder SHA-256", "Контрольная сумма encoder.", cfg.Models.GigaAMEncoderSHA256),
				textSetting("models.gigaam_decoder_url", "GigaAM decoder URL", "RNNT predictor/decoder.", cfg.Models.GigaAMDecoderURL),
				textSetting("models.gigaam_decoder_sha256", "Decoder SHA-256", "Контрольная сумма decoder.", cfg.Models.GigaAMDecoderSHA256),
				textSetting("models.gigaam_joint_url", "GigaAM joint URL", "RNNT joint network.", cfg.Models.GigaAMJointURL),
				textSetting("models.gigaam_joint_sha256", "Joint SHA-256", "Контрольная сумма joint.", cfg.Models.GigaAMJointSHA256),
				textSetting("models.gigaam_vocab_url", "GigaAM vocabulary URL", "Словарь токенов GigaAM.", cfg.Models.GigaAMVocabURL),
				textSetting("models.gigaam_vocab_sha256", "Vocabulary SHA-256", "Необязательная контрольная сумма словаря.", cfg.Models.GigaAMVocabSHA256),
				textSetting("models.whisper_small_url", "Whisper small Q5_1 URL", "Быстрая компактная мультиязычная модель (~181 МБ).", cfg.Models.WhisperSmallURL),
				textSetting("models.whisper_small_sha256", "Whisper small SHA-256", "Контрольная сумма оптимального Q5_1-варианта.", cfg.Models.WhisperSmallSHA256),
				textSetting("models.whisper_medium_url", "Whisper medium Q5_0 URL", "Более точная мультиязычная модель (~514 МБ).", cfg.Models.WhisperMediumURL),
				textSetting("models.whisper_medium_sha256", "Whisper medium SHA-256", "Контрольная сумма Q5_0-варианта.", cfg.Models.WhisperMediumSHA256),
				textSetting("models.whisper_turbo_url", "Whisper large-v3-turbo Q5_0 URL", "Лучшее качество Whisper при разумной скорости (~574 МБ).", cfg.Models.WhisperTurboURL),
				textSetting("models.whisper_turbo_sha256", "Whisper turbo SHA-256", "Контрольная сумма Q5_0-варианта.", cfg.Models.WhisperTurboSHA256),
			),
			inSection("Фильтрация речи",
				textSetting("models.vad_url", "Silero VAD URL", "Источник модели, которая отделяет речь от тишины.", cfg.Models.VADURL),
				textSetting("models.vad_sha256", "Silero VAD SHA-256", "Ожидаемая контрольная сумма файла VAD.", cfg.Models.VADSHA256),
			),
			inSection("Определение спикеров",
				textSetting("models.segmentation_url", "PyAnnote Segmentation URL", "ONNX-модель powerset-сегментации с поддержкой перекрывающейся речи.", cfg.Models.SegmentationURL),
				textSetting("models.segmentation_sha256", "Segmentation SHA-256", "Контрольная сумма model.onnx.", cfg.Models.SegmentationSHA256),
				textSetting("models.embedding_url", "WeSpeaker URL", "ResNet34-LM VoxCeleb для голосовых признаков.", cfg.Models.EmbeddingURL),
				textSetting("models.embedding_sha256", "WeSpeaker SHA-256", "Контрольная сумма embedding-модели.", cfg.Models.EmbeddingSHA256),
			),
		)},
		{ID: "logging", Title: "Логирование", Description: "Уровень и ротация локального журнала.", Fields: []settingField{
			selectSetting("logging.level", "Уровень", "Минимальный уровень сообщений.", cfg.Logging.Level, option("debug", "Debug"), option("info", "Info"), option("warn", "Warning"), option("error", "Error")),
			textSetting("logging.directory", "Каталог логов", "Путь к журналам приложения.", cfg.Logging.Directory),
			intSetting("logging.max_file_mb", "Размер файла, МБ", "Порог ротации одного журнала.", cfg.Logging.MaxFileMB),
			intSetting("logging.max_files", "Количество файлов", "Количество журналов после ротации.", cfg.Logging.MaxFiles),
		}},
	}
	localizeSettingsSchema(groups, cfg.App.Language)
	return groups
}

// settings returns the settings schema or persists validated changes.
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := s.config()
		if cfg.ConfigFile != "" {
			if raw, err := config.Load(cfg.ConfigFile); err == nil {
				raw.ConfigFile = cfg.ConfigFile
				cfg = raw
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": settingsSchema(cfg), "config_path": cfg.ConfigFile})
		return
	}
	if r.Method == http.MethodPost {
		s.testSettings(w, r)
		return
	}
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	var request struct {
		Values map[string]string `json:"values"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(request.Values) == 0 {
		writeError(w, http.StatusBadRequest, "no changed settings")
		return
	}
	if len(request.Values) > 100 {
		writeError(w, http.StatusBadRequest, "too many settings")
		return
	}

	current := s.config()
	validationConfig := current
	if current.ConfigFile != "" {
		if raw, err := config.Load(current.ConfigFile); err == nil {
			raw.ConfigFile = current.ConfigFile
			validationConfig = raw
		}
	}
	known := make(map[string]settingField)
	for _, group := range settingsSchema(validationConfig) {
		for _, field := range group.Fields {
			known[field.Key] = field
		}
	}
	literals := make(map[string]string, len(request.Values))
	for key, value := range request.Values {
		field, ok := known[key]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown setting: "+key)
			return
		}
		value = strings.TrimSpace(value)
		switch field.Kind {
		case "checkbox":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: expected true or false", key))
				return
			}
			literals[key] = strconv.FormatBool(parsed)
		case "number":
			if field.Integer {
				if _, err := strconv.Atoi(value); err != nil {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: expected integer", key))
					return
				}
			} else if _, err := strconv.ParseFloat(value, 64); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: expected number", key))
				return
			}
			literals[key] = value
		case "password":
			if value == "" {
				continue
			}
			literals[key] = strconv.Quote(value)
		default:
			if field.Kind == "select" && len(field.Options) > 0 {
				valid := false
				for _, candidate := range field.Options {
					if candidate.Value == value {
						valid = true
						break
					}
				}
				if !valid {
					writeError(w, http.StatusBadRequest, "invalid value for "+key)
					return
				}
			}
			if field.Kind == "select" && field.Integer {
				if _, err := strconv.Atoi(value); err != nil {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: expected integer", key))
					return
				}
				literals[key] = value
			} else {
				literals[key] = strconv.Quote(value)
			}
		}
	}
	if len(literals) == 0 {
		writeError(w, http.StatusBadRequest, "no changed settings")
		return
	}
	restartOnly := map[string]bool{
		"app.data_dir": true, "app.listen_host": true, "app.listen_port": true, "app.open_browser": true,
		"desktop.tray_enabled":  true,
		"storage.database_path": true, "storage.backup_count": true,
		"logging.directory": true, "logging.max_file_mb": true, "logging.max_files": true,
	}
	dynamicChanged := false
	modelSettingsChanged := false
	var restartKeys []string
	var appliedKeys []string
	for key := range literals {
		if restartOnly[key] {
			restartKeys = append(restartKeys, key)
		} else {
			if key == "logging.level" {
				appliedKeys = append(appliedKeys, key)
				continue
			}
			dynamicChanged = true
			appliedKeys = append(appliedKeys, key)
		}
		if strings.HasPrefix(key, "models.") || strings.HasPrefix(key, "inference.") || key == "transcription.model_path" || key == "transcription.vad_model_path" || strings.HasPrefix(key, "diarization.") {
			modelSettingsChanged = true
		}
	}
	sort.Strings(restartKeys)
	sort.Strings(appliedKeys)
	if dynamicChanged && (s.pipeline.AnyRunning() || s.recorder.AnyActive()) {
		writeError(w, http.StatusConflict, "wait for recording and processing to finish before changing working settings")
		return
	}
	if modelSettingsChanged && s.models.AnyDownloading() {
		writeError(w, http.StatusConflict, "wait for the model download to finish")
		return
	}
	if err := config.UpdateFile(current.ConfigFile, literals); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	loaded, err := config.Load(current.ConfigFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings were saved but could not be reloaded: "+err.Error())
		return
	}
	loaded.ConfigFile = current.ConfigFile
	config.ResolvePaths(&loaded, current.ConfigFile)
	// These resources are owned by the running process and cannot be replaced
	// safely without reopening the listener, database or log file.
	loaded.App.DataDir = current.App.DataDir
	loaded.App.ListenHost = current.App.ListenHost
	loaded.App.ListenPort = current.App.ListenPort
	loaded.App.OpenBrowser = current.App.OpenBrowser
	loaded.Desktop.TrayEnabled = current.Desktop.TrayEnabled
	loaded.Storage.DatabasePath = current.Storage.DatabasePath
	loaded.Storage.BackupCount = current.Storage.BackupCount
	loaded.Logging = current.Logging
	var nextLogLevel string
	if value, changed := literals["logging.level"]; changed {
		nextLogLevel, _ = strconv.Unquote(value)
		loaded.Logging.Level = nextLogLevel
	}
	if dynamicChanged {
		if err := s.pipeline.UpdateConfig(loaded); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err := s.recorder.UpdateConfig(loaded.Audio); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if modelSettingsChanged {
			if err := s.models.UpdateConfig(loaded); err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
		}
	}
	s.setConfig(loaded)
	if nextLogLevel != "" && s.logLevel != nil {
		s.logLevel.Set(nextLogLevel)
	}
	restartRequired := len(restartKeys) > 0
	s.logger.Printf("configuration updated path=%q keys=%d hot_applied=%d restart_required=%t", current.ConfigFile, len(literals), len(appliedKeys), restartRequired)
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "restart_required": restartRequired, "restart_keys": restartKeys, "applied_keys": appliedKeys, "changed": len(literals)})
}

// testSettings performs a real completion against the summary endpoint.
func (s *Server) testSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Group  string            `json:"group"`
		Values map[string]string `json:"values"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Group != "summary" {
		writeError(w, http.StatusBadRequest, "testing this settings group is not supported")
		return
	}
	cfg := s.config().Summary
	if value, ok := request.Values["summary.base_url"]; ok {
		cfg.BaseURL = strings.TrimSpace(value)
	}
	if value, ok := request.Values["summary.model"]; ok {
		cfg.Model = strings.TrimSpace(value)
	}
	if value := strings.TrimSpace(request.Values["summary.token"]); value != "" {
		cfg.Token = value
	}
	if value, ok := request.Values["summary.token_env"]; ok {
		cfg.TokenEnv = strings.TrimSpace(value)
	}
	if value, ok := request.Values["summary.tls_verify"]; ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "summary.tls_verify: expected true or false")
			return
		}
		cfg.TLSVerify = parsed
	}
	if value, ok := request.Values["summary.tls_ca_file"]; ok {
		cfg.TLSCAFile = strings.TrimSpace(value)
	}
	if value, ok := request.Values["summary.tls_server_name"]; ok {
		cfg.TLSServerName = strings.TrimSpace(value)
	}
	if value, ok := request.Values["summary.timeout_seconds"]; ok {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.TimeoutSeconds = parsed
		}
	}
	if cfg.TimeoutSeconds <= 0 || cfg.TimeoutSeconds > 20 {
		cfg.TimeoutSeconds = 20
	}
	// A connection test should be fast and deterministic, independent from the
	// retry policy used for a real meeting summary.
	cfg.MaxRetries = 0
	started := time.Now()
	summarizer, err := pipeline.NewSummarizer(cfg)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		_, err = summarizer.Test(ctx)
	}
	result := map[string]any{"ok": err == nil, "duration_ms": time.Since(started).Milliseconds(), "model": cfg.Model}
	if err != nil {
		result["message"] = err.Error()
	} else {
		result["message"] = "URL, TLS, authorization and test generation work"
	}
	writeJSON(w, http.StatusOK, result)
}
