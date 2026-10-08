package server

import "strings"

// settingTextEN carries an English label + description pair used by the
// settings schema overlay.
type settingTextEN struct {
	Label string
	Desc  string
}

// settingGroupsEN translates the settings group titles and descriptions.
var settingGroupsEN = map[string]settingTextEN{
	"app":           {"Application", "Interface, data directory and local HTTP server."},
	"audio":         {"Audio", "Native microphone and system audio recording."},
	"desktop":       {"Tray and hotkeys", "Quick recording control without an open browser. Regular combinations on macOS do not require Accessibility; the registration result is written to the log."},
	"inference":     {"ONNX Runtime", "Native neural inference library. LocalMeetAssist loads it directly; no CLI or Python needed."},
	"transcription": {"Transcription", "GigaAM v3 or Whisper run directly inside LocalMeetAssist, without launching a CLI."},
	"diarization":   {"Diarization", "PyAnnote Segmentation 3 + WeSpeaker with voice clustering in Go."},
	"summary":       {"Summarization", "OpenAI-compatible minutes model."},
	"storage":       {"Storage", "File names, Ogg/Opus and source WAV policy."},
	"models":        {"Model files", "Direct ONNX model sources and checksums. The bundle can be downloaded and applied in the “Models” section."},
	"logging":       {"Logging", "Level and rotation of the local log."},
}

// settingSectionsEN translates the section headings used inside a group.
var settingSectionsEN = map[string]string{
	"Общие параметры":      "Common parameters",
	"Перевод речи в текст": "Speech-to-text",
	"Фильтрация речи":      "Speech filtering",
	"Определение спикеров": "Speaker identification",
}

// settingOptionsEN translates select option labels, keyed by "<field key>.<value>".
var settingOptionsEN = map[string]string{
	"app.language.ru": "Russian",
	"app.language.en": "English",
	"app.theme.dark":  "Dark",
	"app.theme.light": "Light",

	"audio.backend.native":         "Native",
	"audio.sample_rate.8000":       "8 kHz",
	"audio.sample_rate.16000":      "16 kHz",
	"audio.sample_rate.24000":      "24 kHz",
	"audio.sample_rate.48000":      "48 kHz",
	"audio.channels.1":             "Mono",
	"audio.channels.2":             "Stereo",
	"audio.echo_cancellation.auto": "Automatic",
	"audio.echo_cancellation.on":   "Always",
	"audio.echo_cancellation.off":  "Off",

	"transcription.engine.gigaam-onnx":       "GigaAM v3 ONNX",
	"transcription.engine.whispercpp-native": "Whisper.cpp native",
	"transcription.source_mode.auto":         "Automatic",
	"transcription.source_mode.mixed":        "Always mixed",
	"transcription.source_mode.separate":     "Separate only",

	"diarization.engine.pyannote-wespeaker-onnx": "PyAnnote + WeSpeaker ONNX",

	"summary.language.":   "Same as interface",
	"summary.language.ru": "Russian",
	"summary.language.en": "English",

	"storage.audio_after_processing.wav":    "All WAV (default)",
	"storage.audio_after_processing.opus":   "Ogg/Opus",
	"storage.audio_after_processing.delete": "Remove audio",
	"storage.audio_after_processing.mp3":    "MP3 (external encoder)",

	"logging.level.debug": "Debug",
	"logging.level.info":  "Info",
	"logging.level.warn":  "Warning",
	"logging.level.error": "Error",
}

// settingOptionLabelsEN translates option labels that cannot be keyed by value,
// for example a dynamically inserted custom model path.
var settingOptionLabelsEN = map[string]string{
	"Текущая пользовательская модель": "Current custom model",
}

// settingFieldsEN translates every settings field label and description.
var settingFieldsEN = map[string]settingTextEN{
	// Application
	"app.language":              {"Interface language", "LocalMeetAssist interface language. Applies after the page reloads."},
	"app.theme":                 {"Theme", "Interface colour theme. Applied immediately in this browser."},
	"app.microphone_owner_name": {"Microphone owner", "Name of the local participant, used automatically in new meetings. Empty — value depends on the interface language."},
	"app.data_dir":              {"Data directory", "Database, recordings, transcripts and logs."},
	"app.listen_host":           {"Address", "Only loopback is allowed: 127.0.0.1, localhost or ::1."},
	"app.listen_port":           {"Port", "0 — pick a free port automatically."},
	"app.open_browser":          {"Open browser", "Open the UI after LocalMeetAssist starts."},

	// Audio
	"audio.backend":            {"Backend", "Recording is performed by the built-in native backend."},
	"audio.sample_rate":        {"Sample rate", "WAV sample rate in hertz."},
	"audio.channels":           {"Channels", "Mono is recommended for recognition."},
	"audio.block_ms":           {"Block size, ms", "Size of a block written to disk."},
	"audio.input_mode":         {"Microphone mode", "Usually auto."},
	"audio.input_device_name":  {"Microphone name", "System name; empty — default device."},
	"audio.input_device_id":    {"Microphone ID", "Use only when selecting by name is ambiguous."},
	"audio.output_mode":        {"System audio mode", "Usually native."},
	"audio.output_device_name": {"System source name", "System name; empty — default source."},
	"audio.output_device_id":   {"System source ID", "Use only when selecting by name is ambiguous."},
	"audio.microphone_gain":    {"Microphone gain", "Multiplier used when building the mixed stream."},
	"audio.system_gain":        {"System audio gain", "Multiplier used when building the mixed stream."},
	"audio.normalize":          {"Normalization", "Allow normalization while preparing audio."},
	"audio.echo_cancellation":  {"Echo cancellation", "WebRTC AEC3 cleans the microphone from speaker playback. The source WAV is always kept; auto processes only when echo is detected."},
	"audio.echo_delay_ms":      {"Echo delay, ms", "Approximate delay between system audio and its arrival at the microphone."},

	// Desktop
	"desktop.tray_enabled":    {"Tray icon", "Show LocalMeetAssist in the system tray. During recording a red indicator appears in the lower-right quarter of the icon."},
	"desktop.toggle_record":   {"Single start/stop key", "Press “Record shortcut”, then the keys. Leave empty if you want separate combinations."},
	"desktop.start_recording": {"Start recording", "Separate global combination to start recording."},
	"desktop.stop_recording":  {"Stop recording", "Separate global combination to stop recording."},
	"desktop.open_ui":         {"Open UI", "Optional global combination to open the Web UI."},

	// Inference
	"inference.runtime_path":         {"Library or directory", "You can point to a ready .dll/.dylib/.so manually. For a directory, LocalMeetAssist picks the file by OS and architecture."},
	"inference.auto_download":        {"Download automatically", "Download the official CPU runtime at startup when the library is missing."},
	"inference.runtime_version":      {"Version", "Pinned ONNX Runtime version; 1.23.2 is supported for automatic download."},
	"inference.whisper_runtime_path": {"Whisper runtime", "Directory of whisper.cpp native libraries; installed from the “Models” section without a CLI."},

	// Transcription
	"transcription.auto_run":               {"Run automatically", "Recognize the meeting automatically after it stops. Manual start is available regardless of this setting."},
	"transcription.engine":                 {"Engine", "The selected model is called natively from the LocalMeetAssist process."},
	"transcription.model_path":             {"Model", "GigaAM directory or Whisper GGML file; it is easier to download and apply a model in the “Models” section."},
	"transcription.language":               {"Language", "Language code, for example ru."},
	"transcription.threads":                {"CPU threads", "Number of recognition threads; 0 — choose automatically."},
	"transcription.timeout_seconds":        {"Timeout, sec", "Maximum processing time for one source."},
	"transcription.chunk_seconds":          {"Max speech fragment, sec", "Silero VAD splits detected speech into independent fragments no longer than this value."},
	"transcription.source_mode":            {"Sources", "auto — separate streams with a mixed fallback; mixed — result from the mixed stream only; separate — no fallback."},
	"transcription.mixed_fallback_enabled": {"Fallback to mixed", "Use the mixed stream when the system transcript fails, is empty or repeats."},
	"transcription.echo_dedup_enabled":     {"Remove echo", "Remove duplicated lines from the microphone stream."},
	"transcription.echo_time_tolerance_ms": {"Echo time tolerance, ms", "Maximum time difference between matching lines."},
	"transcription.echo_text_similarity":   {"Text similarity", "Threshold 0–1 for detecting a duplicate."},
	"transcription.vad_enabled":            {"VAD", "Separate speech from silence before recognition."},
	"transcription.vad_model_path":         {"VAD model", "Path to the Silero VAD model."},
	"transcription.vad_threshold":          {"VAD threshold", "Speech probability 0–1."},
	"transcription.vad_min_speech_ms":      {"Minimum speech, ms", "Fragments shorter than this value are dropped."},
	"transcription.vad_min_silence_ms":     {"Minimum silence, ms", "Pause used to split speech fragments."},
	"transcription.suppress_non_speech":    {"Suppress non-speech", "Do not pass VAD silence to the speech model."},
	"transcription.no_fallback":            {"Strict mode", "Do not produce text when the model result is empty."},
	"transcription.silence_filter":         {"RMS silence filter", "Additional filtering of very quiet fragments."},
	"transcription.min_segment_rms":        {"Minimum RMS", "Segments quieter than this value are treated as silence."},

	// Diarization
	"diarization.auto_run":           {"Run automatically", "Automatically separate remote participants into speakers. Manual start is always available."},
	"diarization.engine":             {"Engine", "Runs inside the LocalMeetAssist process without a CLI."},
	"diarization.segmentation_model": {"Segmentation model", "Path to segmentation.onnx."},
	"diarization.embedding_model":    {"Voice embedding model", "Path to WeSpeaker ResNet34-LM ONNX."},
	"diarization.cluster_threshold":  {"Cosine distance threshold", "Without a fixed participant count, voices are merged while the distance stays below the threshold."},
	"diarization.max_auto_speakers":  {"Maximum with auto", "Protection against dozens of false speakers."},
	"diarization.microphone_enabled": {"Diarize microphone", "Usually off: the owner is identified by the separate stream."},
	"diarization.timeout_seconds":    {"Timeout, sec", "Maximum diarization time."},
	"diarization.num_threads":        {"CPU threads", "ONNX Runtime threads for segmentation and embedding extraction; 0 — engine default."},

	// Summarization
	"summary.auto_run":        {"Run automatically", "Create the minutes automatically after the transcript is ready. Manual start is always available."},
	"summary.language":        {"Minutes language", "Language of the whole response: headings, labels and text. Default — same as the interface language. The prompt defines the structure; the language comes from here."},
	"summary.base_url":        {"API URL", "For example http://127.0.0.1:8080/v1."},
	"summary.model":           {"Model", "Model name in the OpenAI-compatible API."},
	"summary.token":           {"Token", "Leave empty to keep the saved token unchanged."},
	"summary.token_env":       {"Token environment variable", "Environment variable that holds the token."},
	"summary.timeout_seconds": {"Timeout, sec", "Request timeout for the model."},
	"summary.max_retries":     {"Retries", "Number of retries on a temporary error."},
	"summary.system_prompt":   {"System prompt", "Instruction for the model that builds the minutes. The result is plain text without markup, ready to paste into an email. The reset button restores the built-in safe template."},
	"summary.tls_verify":      {"Verify TLS", "Verify the server certificate."},
	"summary.tls_ca_file":     {"CA file", "Additional trusted CA PEM."},
	"summary.tls_server_name": {"TLS server name", "Certificate name override."},

	// Storage
	"storage.file_name_template":     {"Name template", "Supports date, time, title_slug, uid_short and artifact_type in double curly braces."},
	"storage.date_format":            {"Date format", "Go time format, for example 2006-01-02."},
	"storage.time_format":            {"Time format", "Go time format, for example 15-04-05."},
	"storage.audio_after_processing": {"After processing", "WAV files are kept by default. opus — keep the mixed Ogg/Opus and delete WAV only after a successful pipeline; delete — remove audio."},
	"storage.opus_bitrate_kbps":      {"Opus, kbps", "Bitrate of the built-in Go encoder."},
	"storage.mp3_bitrate_kbps":       {"MP3, kbps", "Used only for the mp3 policy."},
	"storage.encoder_command":        {"MP3 encoder", "Path to an external LAME; not needed for Opus."},
	"storage.keep_source_on_failure": {"Keep WAV on failure", "Do not delete sources when processing did not finish."},
	"storage.database_path":          {"bbolt database", "Path to the local meetings database."},
	"storage.backup_count":           {"Backup copies", "Number of database copies to keep."},

	// Model files
	"models.download_timeout_seconds": {"Download timeout, sec", "Maximum duration of a single download."},
	"models.gigaam_encoder_url":       {"GigaAM encoder URL", "INT8 encoder — the main and largest part of the model."},
	"models.gigaam_encoder_sha256":    {"Encoder SHA-256", "Encoder checksum."},
	"models.gigaam_decoder_url":       {"GigaAM decoder URL", "RNNT predictor/decoder."},
	"models.gigaam_decoder_sha256":    {"Decoder SHA-256", "Decoder checksum."},
	"models.gigaam_joint_url":         {"GigaAM joint URL", "RNNT joint network."},
	"models.gigaam_joint_sha256":      {"Joint SHA-256", "Joint checksum."},
	"models.gigaam_vocab_url":         {"GigaAM vocabulary URL", "GigaAM token vocabulary."},
	"models.gigaam_vocab_sha256":      {"Vocabulary SHA-256", "Optional vocabulary checksum."},
	"models.whisper_small_url":        {"Whisper small Q5_1 URL", "Fast compact multilingual model (~181 MB)."},
	"models.whisper_small_sha256":     {"Whisper small SHA-256", "Checksum of the optimal Q5_1 variant."},
	"models.whisper_medium_url":       {"Whisper medium Q5_0 URL", "More accurate multilingual model (~514 MB)."},
	"models.whisper_medium_sha256":    {"Whisper medium SHA-256", "Checksum of the Q5_0 variant."},
	"models.whisper_turbo_url":        {"Whisper large-v3-turbo Q5_0 URL", "Best Whisper quality at reasonable speed (~574 MB)."},
	"models.whisper_turbo_sha256":     {"Whisper turbo SHA-256", "Checksum of the Q5_0 variant."},
	"models.vad_url":                  {"Silero VAD URL", "Source of the model that separates speech from silence."},
	"models.vad_sha256":               {"Silero VAD SHA-256", "Expected checksum of the VAD file."},
	"models.segmentation_url":         {"PyAnnote Segmentation URL", "ONNX powerset segmentation model with overlapping speech support."},
	"models.segmentation_sha256":      {"Segmentation SHA-256", "Checksum of model.onnx."},
	"models.embedding_url":            {"WeSpeaker URL", "ResNet34-LM VoxCeleb for voice embeddings."},
	"models.embedding_sha256":         {"WeSpeaker SHA-256", "Checksum of the embedding model."},

	// Logging
	"logging.level":       {"Level", "Minimum message level."},
	"logging.directory":   {"Log directory", "Path to application logs."},
	"logging.max_file_mb": {"File size, MB", "Rotation threshold for a single log."},
	"logging.max_files":   {"File count", "Number of logs kept after rotation."},
}

// localizeSettingsSchema replaces the Russian schema text with English when the
// interface language is English. Missing entries silently keep the Russian
// fallback that settingsSchema already provides.
func localizeSettingsSchema(groups []settingGroup, language string) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		return
	}
	for gi := range groups {
		if tr, ok := settingGroupsEN[groups[gi].ID]; ok {
			groups[gi].Title, groups[gi].Description = tr.Label, tr.Desc
		}
		for fi := range groups[gi].Fields {
			field := &groups[gi].Fields[fi]
			if tr, ok := settingFieldsEN[field.Key]; ok {
				field.Label, field.Description = tr.Label, tr.Desc
			}
			if section, ok := settingSectionsEN[field.Section]; ok {
				field.Section = section
			}
			for oi := range field.Options {
				if label, ok := settingOptionsEN[field.Key+"."+field.Options[oi].Value]; ok {
					field.Options[oi].Label = label
				} else if label, ok := settingOptionLabelsEN[field.Options[oi].Label]; ok {
					field.Options[oi].Label = label
				}
			}
		}
	}
}
