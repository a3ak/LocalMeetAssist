const state = {
  month: new Date(),
  meetings: [],
  recording: null,
  timer: null,
  eventSource: null,
  recordingPollTimer: null,
  detailTimer: null,
  detailTab: "artifacts",
  detail: null,
  devices: null,
  diagnostics: null,
  modelTimer: null,
  settings: null,
  settingsDirty: new Map(),
  tokenSecrets: new Map(),
  speakerEditor: null,
  speakerPlayer: null,
  searchQuery: "",
  searchMatches: null,
  searchSpeakers: [],
  searchTimer: null,
  searchRequest: 0,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

// --- i18n ---------------------------------------------------------------
const I18N = {};
const I18N_FALLBACK = {
  "api.timeout": "The server did not respond within 15 seconds. Check the LocalMeetAssist log.",
  "api.sessionFailed": "Could not get local session: HTTP {status}",
  "app.bootstrapError": "UI startup error: {message}",
};
let LOCALE = "ru-RU";

function t(key, vars) {
  let template = I18N[key] ?? I18N_FALLBACK[key] ?? key;
  if (vars) {
    for (const [name, value] of Object.entries(vars)) {
      template = template.split(`{${name}}`).join(String(value ?? ""));
    }
  }
  return template;
}

function applyStaticTranslations() {
  document.title = t("app.title");
  $$("[data-i18n]").forEach((node) => { node.textContent = t(node.dataset.i18n); });
  $$("[data-i18n-placeholder]").forEach((node) => { node.placeholder = t(node.dataset.i18nPlaceholder); });
  $$("[data-i18n-title]").forEach((node) => { node.title = t(node.dataset.i18nTitle); });
  $$("[data-i18n-label]").forEach((node) => { node.label = t(node.dataset.i18nLabel); });
  // The theme button label depends on the active theme and is set dynamically
  // by applyTheme, which may run before the translation dictionary is loaded.
  const themeButton = $("#themeBtn");
  if (themeButton) themeButton.textContent = document.documentElement.dataset.theme === "dark" ? t("app.themeLight") : t("app.themeDark");
}

async function loadLanguage(code) {
  const language = code === "en" ? "en" : "ru";
  LOCALE = language === "en" ? "en-US" : "ru-RU";
  document.documentElement.lang = language;
  const response = await fetch(`/web/lang/${language}.json`, { cache: "no-store" });
  if (!response.ok) throw new Error(`Failed to load language ${language}: HTTP ${response.status}`);
  const data = await response.json();
  for (const [key, value] of Object.entries(data)) I18N[key] = value;
  applyStaticTranslations();
}

function showAuthHint() {
  const node = $("#authHint");
  if (node) node.classList.remove("hidden");
}

function isForbidden(error) {
  const message = String((error && error.message) || "").toLowerCase();
  return message.includes("403") || message.includes("forbidden");
}

async function api(path, options = {}) {
  const opts = { ...options, headers: { ...(options.headers || {}) } };
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), options.timeoutMs || 15000);
  opts.signal = options.signal || controller.signal;
  if (opts.json !== undefined) {
    opts.body = JSON.stringify(opts.json);
    opts.headers["Content-Type"] = "application/json";
    delete opts.json;
  }
  let response;
  try {
    response = await fetch(path, opts);
  } catch (error) {
    if (error.name === "AbortError") throw new Error(t("api.timeout"));
    throw error;
  } finally {
    clearTimeout(timeout);
  }
  if (response.status === 403) showAuthHint();
  if (!response.ok) {
    let error = {};
    try { error = await response.json(); } catch {}
    throw new Error(error.error || `HTTP ${response.status}`);
  }
  if (response.status === 204) return null;
  return response.json();
}

function toast(message) {
  const node = $("#toast");
  node.textContent = message;
  node.classList.remove("hidden");
  clearTimeout(node._timer);
  node._timer = setTimeout(() => node.classList.add("hidden"), 6500);
}

function esc(value = "") {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[char]);
}

function fmtDate(value) {
  return new Intl.DateTimeFormat(LOCALE, {
    day: "2-digit", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit",
  }).format(new Date(value));
}

function monthBounds(date) {
  return {
    from: new Date(date.getFullYear(), date.getMonth(), 1),
    to: new Date(date.getFullYear(), date.getMonth() + 1, 1),
  };
}

async function loadMeetings() {
  const { from, to } = monthBounds(state.month);
  const result = await api(`/api/v1/meetings?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`);
  state.meetings = Array.isArray(result) ? result : [];
  renderCalendar();
  renderList();
  if (state.searchQuery) scheduleMeetingSearch();
}

function meetingMatchesSearch(meeting) {
  return !state.searchMatches || state.searchMatches.has(meeting.uid);
}

function appendSearchCondition(value) {
  const input = $("#meetingSearch");
  const prefix = input.value.trim();
  input.value = prefix ? `${prefix}${prefix.endsWith(";") ? " " : "; "}${value}` : value;
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
  scheduleMeetingSearch();
}

function renderSearchSuggestions() {
  const root = $("#speakerSuggestions");
  root.innerHTML = "";
  const input = $("#meetingSearch").value;
  const clauseStart = Math.max(input.lastIndexOf(";"), -1) + 1;
  const clause = input.slice(clauseStart);
  const match = clause.match(/^\s*speakers\s*:\s*(.*)$/i);
  if (!match) return;
  const valueStart = clauseStart + clause.indexOf(":") + 1;
  const comma = input.lastIndexOf(",");
  const fragmentStart = comma >= valueStart ? comma + 1 : valueStart;
  const rawFragment = input.slice(fragmentStart);
  const excluded = /^\s*!/.test(rawFragment);
  const fragment = rawFragment.replace(/^\s*!?\s*/, "").trim().toLocaleLowerCase(LOCALE);
  if (fragment.length < 3) return;
  state.searchSpeakers
    .filter((name) => !/^SPEAKER_\d+$/i.test(name) && name.toLocaleLowerCase(LOCALE).includes(fragment))
    .slice(0, 8)
    .forEach((name) => {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = name;
      button.title = t("search.speakerSuggestionTitle", { name });
      button.onclick = () => {
        const prefix = input.slice(0, fragmentStart);
        $("#meetingSearch").value = `${prefix} ${excluded ? "!" : ""}${name}`;
        $("#meetingSearch").focus();
        scheduleMeetingSearch();
      };
      root.appendChild(button);
    });
}

async function executeMeetingSearch() {
  const query = $("#meetingSearch").value.trim();
  const request = ++state.searchRequest;
  state.searchQuery = query;
  $("#clearSearch").classList.toggle("hidden", !query);
  try {
    const result = await api(`/api/v1/search?q=${encodeURIComponent(query)}`);
    if (request !== state.searchRequest) return;
    state.searchSpeakers = Array.isArray(result.speaker_names) ? result.speaker_names : [];
    state.searchMatches = query ? new Set(result.matching_uids || []) : null;
    renderSearchSuggestions();
    renderCalendar();
    renderList();
    if (query) {
      const visible = state.meetings.filter(meetingMatchesSearch).length;
      $("#searchStatus").textContent = t("search.matchesInMonth", { visible, total: state.meetings.length });
      $("#searchStatus").classList.remove("error");
    } else {
      $("#searchStatus").textContent = t("search.hint");
      $("#searchStatus").classList.remove("error");
    }
  } catch (error) {
    if (request !== state.searchRequest) return;
    state.searchMatches = null;
    renderCalendar();
    renderList();
    $("#searchStatus").textContent = error.message;
    $("#searchStatus").classList.add("error");
  }
}

function scheduleMeetingSearch() {
  clearTimeout(state.searchTimer);
  state.searchTimer = setTimeout(() => executeMeetingSearch().catch((error) => toast(error.message)), 300);
}

function renderRecordingState(meeting) {
  const previousUID = state.recording?.uid || "";
  const nextUID = meeting?.uid || "";
  state.recording = meeting || null;
  clearInterval(state.timer);
  state.timer = null;
  if (!meeting) {
    $("#stopBtn").disabled = false;
    $("#stopBtn").classList.add("hidden");
    $("#startBtn").classList.remove("hidden");
    $("#timer").classList.add("hidden");
    $("#timer").textContent = "00:00";
    return previousUID !== nextUID;
  }
  $("#startBtn").classList.add("hidden");
  $("#stopBtn").classList.remove("hidden");
  $("#timer").classList.remove("hidden");
  const started = new Date(meeting.started_at).getTime();
  const updateTimer = () => {
    const seconds = Math.max(0, Math.floor((Date.now() - started) / 1000));
    $("#timer").textContent = `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
    $("#stopBtn").title = t("record.stopTitle", { title: meeting.title });
  };
  updateTimer();
  state.timer = setInterval(updateTimer, 1000);
  return previousUID !== nextUID;
}

async function refreshRecordingState() {
  const payload = await api("/api/v1/recordings");
  const changed = renderRecordingState(payload.active ? payload.meeting : null);
  if (changed) await loadMeetings();
}

function startRecordingFallbackPoll() {
  if (state.recordingPollTimer) return;
  state.recordingPollTimer = setInterval(() => refreshRecordingState().catch(() => {}), 5000);
}

function stopRecordingFallbackPoll() {
  clearInterval(state.recordingPollTimer);
  state.recordingPollTimer = null;
}

function connectEvents() {
  state.eventSource?.close();
  const source = new EventSource("/api/v1/events");
  state.eventSource = source;
  source.onopen = stopRecordingFallbackPoll;
  source.onerror = startRecordingFallbackPoll;
  source.onmessage = (event) => {
    try {
      const message = JSON.parse(event.data);
      if (message.type !== "recording") return;
      const payload = message.payload || {};
      const changed = renderRecordingState(payload.active ? payload.meeting : null);
      if (changed) loadMeetings().catch(() => {});
    } catch (error) {
      console.warn("LocalMeetAssist event ignored", error);
    }
  };
}

function sameDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

function statusText(value) {
  const key = `status.${value}`;
  const translated = t(key);
  return translated === key ? value : translated;
}

function stageText(value) {
  const key = `stage.${value}`;
  const translated = t(key);
  return translated === key ? value : translated;
}

function renderCalendar() {
  const date = state.month;
  $("#monthTitle").textContent = new Intl.DateTimeFormat(LOCALE, { month: "long", year: "numeric" }).format(date);
  const first = new Date(date.getFullYear(), date.getMonth(), 1);
  const offset = (first.getDay() + 6) % 7;
  const start = new Date(first);
  start.setDate(1 - offset);
  const today = new Date();
  const root = $("#calendar");
  root.innerHTML = "";
  for (let i = 0; i < 42; i += 1) {
    const day = new Date(start);
    day.setDate(start.getDate() + i);
    const cell = document.createElement("div");
    cell.className = "day" + (day.getMonth() !== date.getMonth() ? " outside" : "") + (sameDay(day, today) ? " today" : "");
    cell.innerHTML = `<span class="day-number">${day.getDate()}</span>`;
    const dayMeetings = state.meetings.filter((meeting) => sameDay(new Date(meeting.started_at), day));
    if (state.searchMatches && dayMeetings.some(meetingMatchesSearch)) cell.classList.add("search-match-day");
    dayMeetings.forEach((meeting) => {
      const event = document.createElement("button");
      const matches = meetingMatchesSearch(meeting);
      event.className = `event ${meeting.status}${state.searchMatches ? (matches ? " search-match" : " search-dimmed") : ""}`;
      event.textContent = `${new Date(meeting.started_at).toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" })} ${meeting.title}`;
      event.onclick = () => openDetail(meeting.uid);
      cell.appendChild(event);
    });
    root.appendChild(cell);
  }
}

function renderList() {
  const root = $("#meetingList");
  root.innerHTML = "";
  const matched = state.meetings.filter(meetingMatchesSearch).length;
  $("#meetingCount").textContent = state.searchMatches ? `${matched}/${state.meetings.length}` : state.meetings.length;
  state.meetings.forEach((meeting) => {
    const node = document.createElement("div");
    const matches = meetingMatchesSearch(meeting);
    node.className = `meeting-item${state.searchMatches ? (matches ? " search-match" : " search-dimmed") : ""}`;
    const topic = meetingTopic(meeting.summary);
    const participants = activeParticipants(meeting.transcript, 5);
    const participantText = participants.names.length
      ? `${participants.names.map(esc).join(", ")}${participants.more ? ` <span class="more-participants">+${participants.more}</span>` : ""}`
      : t("list.noParticipants");
    node.innerHTML = `<strong>${esc(meeting.title)}</strong><div class="meta">${fmtDate(meeting.started_at)} · ${formatDuration(meeting.duration_ms)}</div>${topic ? `<div class="meeting-topic" title="${esc(topic)}">${esc(topic)}</div>` : ""}<div class="meeting-participants">${participantText}</div><span class="badge ${meeting.status}">${statusText(meeting.status)}</span>`;
    node.onclick = () => openDetail(meeting.uid);
    root.appendChild(node);
  });
  if (!state.meetings.length) root.innerHTML = `<div class="hint">${t("list.noMeetings")}</div>`;
}

function formatDuration(durationMS) {
  const total = Math.max(0, Math.round(Number(durationMS || 0) / 1000));
  if (!total) return t("duration.none");
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  return hours ? t("duration.hours", { hours, minutes }) : t("duration.minutes", { minutes: Math.max(1, minutes) });
}

function meetingTopic(summary = "") {
  const lines = String(summary).split(/\r?\n/).map((line) => line.trim());
  const briefIndex = lines.findIndex((line) => /^#\s+(краткий итог|brief summary)/i.test(line));
  const candidates = briefIndex >= 0 ? lines.slice(briefIndex + 1) : lines;
  let frontMatter = false;
  for (const line of candidates) {
    if (line === "---") { frontMatter = !frontMatter; continue; }
    if (frontMatter || !line || /^[-*_]+$/.test(line)) continue;
    if (/^#{1,3}\s+/.test(line)) {
      if (briefIndex >= 0) break;
      continue;
    }
    if (!/^[a-z_]+:\s/i.test(line)) return line.replace(/^[-*•]\s*/, "").slice(0, 180);
  }
  return "";
}

function activeParticipants(transcript = "", limit = 5) {
  const counts = new Map();
  String(transcript).split(/\r?\n/).forEach((line) => {
    const match = line.match(/^\s*\[[^\]]+\]\s+(.+?):\s+/);
    if (!match) return;
    const name = match[1].trim();
    counts.set(name, (counts.get(name) || 0) + 1);
  });
  const all = [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([name]) => name);
  return { names: all.slice(0, limit), more: Math.max(0, all.length - limit) };
}

function applyTheme(theme) {
  const selected = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = selected;
  localStorage.setItem("localmeetassist-theme", selected);
  // Until the translation dictionary is loaded, keep the HTML fallback label
  // instead of flashing the raw "app.themeLight" key. applyStaticTranslations
  // refreshes the label once the language is ready.
  const button = $("#themeBtn");
  if (button && I18N["app.themeLight"]) button.textContent = selected === "dark" ? t("app.themeLight") : t("app.themeDark");
}

function fillDeviceSelect(element, items, emptyText, configuredID) {
  element.innerHTML = "";
  if (!Array.isArray(items) || items.length === 0) {
    element.innerHTML = `<option value="">${emptyText}</option>`;
    element.disabled = true;
    return;
  }
  element.disabled = false;
  items.forEach((device) => {
    const option = document.createElement("option");
    option.value = device.id;
    option.textContent = device.name;
    option.dataset.name = device.name;
    if (configuredID && configuredID === device.id) option.selected = true;
    element.appendChild(option);
  });
}

async function refreshDevices() {
  const [devices, config] = await Promise.all([api("/api/v1/audio/devices"), api("/api/v1/config")]);
  state.devices = devices;
  fillDeviceSelect($("#micSelect"), devices.microphones, t("record.noMic"), config.input_device_id);
  fillDeviceSelect($("#outputSelect"), devices.system_sources, t("record.noSystem"), config.output_device_id);
  const warnings = Array.isArray(devices.warnings) ? devices.warnings : [];
  $("#permissionHint").textContent = warnings.length ? warnings.join(" ") : t("record.devicesOk");
  const ready = devices.available && devices.microphones?.length > 0 && devices.system_sources?.length > 0;
  $("#confirmRecord").disabled = !ready;
  const bar = $("#diagBar");
  bar.className = `diag-bar ${ready ? "ok" : "error"}`;
  bar.textContent = ready
    ? t("record.audioReady", { mics: devices.microphones.length, systems: devices.system_sources.length })
    : t("record.audioNotReady", { reason: warnings.join(" ") || t("record.noMic") });
  return devices;
}

function selectedDevice(selector) {
  const element = $(selector);
  const option = element.selectedOptions[0];
  return { id: element.value, name: option?.dataset.name || option?.textContent || element.value };
}

async function startRecording() {
  const button = $("#confirmRecord");
  button.disabled = true;
  button.textContent = t("record.starting");
  try {
    const meeting = await api("/api/v1/recordings", {
      method: "POST",
      json: {
        title: $("#recordTitle").value,
        input_device: selectedDevice("#micSelect"),
        output_device: selectedDevice("#outputSelect"),
        participant_count: Number($("#speakerCount").value || 0),
      },
    });
    renderRecordingState(meeting);
    $("#recordDialog").close();
    await loadMeetings();
    toast(t("record.started", { title: meeting.title }));
  } catch (error) {
    await loadMeetings().catch(() => {});
    toast(error.message);
  } finally {
    button.textContent = t("record.confirm");
    button.disabled = !(state.devices?.available && state.devices?.microphones?.length && state.devices?.system_sources?.length);
  }
}

async function stopRecording() {
  if (!state.recording) return;
  const uid = state.recording.uid;
  const title = state.recording.title;
  $("#stopBtn").disabled = true;
  clearInterval(state.timer);
  try {
    await api(`/api/v1/recordings/${uid}/stop`, { method: "POST" });
    toast(t("record.saved", { title }));
  } catch (error) {
    toast(error.message);
  } finally {
    renderRecordingState(null);
    await loadMeetings();
  }
}

async function openDetail(uid, preserveTab = false) {
  try {
    const detail = await api(`/api/v1/meetings/${uid}`);
    state.detail = detail;
    const meeting = detail.meeting;
    $("#detailTitle").value = meeting.title;
    const durationText = meeting.duration_ms ? t("duration.seconds", { n: Math.round(meeting.duration_ms / 1000) }) : t("duration.none");
    $("#detailMeta").textContent = t("detail.meta", {
      date: fmtDate(meeting.started_at),
      duration: durationText,
      mic: meeting.input_device?.name || "—",
      sys: meeting.output_device?.name || "—",
    });
    renderStatusStrip(meeting.status, meeting.processing_stage, meeting.last_error);
    $("#summaryText").value = meeting.summary || "";
    $("#summaryStale").classList.toggle("hidden", meeting.metadata?.summary_stale !== "true");
    $("#transcriptText").value = meeting.transcript || "";
    $("#detailSpeakerCount").value = meeting.metadata?.participant_count || "0";
    setProcessingControls(Boolean(detail.processing_active));
    const artifacts = Array.isArray(detail.artifacts) ? detail.artifacts : [];
    renderSpeakers(Array.isArray(detail.speakers) ? detail.speakers : [], artifacts, uid);
    renderArtifacts(artifacts, uid);
    renderJobs(Array.isArray(detail.jobs) ? detail.jobs : []);
    if (!preserveTab) showTab("artifacts");
    if (!$("#detailDialog").open) $("#detailDialog").showModal();
    scheduleProcessingPoll();
    if (!detail.processing_active) await loadMeetings();
  } catch (error) {
    toast(error.message);
  }
}

function renderStatusStrip(status, stage, lastError) {
  $("#statusStrip").innerHTML = `${t("detail.status", { status: statusText(status) })}${stage ? ` · ${t("detail.stage", { stage: stageText(stage) })}` : ""}${lastError ? `<br><span style="color:#b42318">${esc(lastError)}</span>` : ""}`;
}

function setProcessingControls(active) {
  $("#detailSpeakerCount").disabled = active;
  $("#saveMeetingProcessing").disabled = active;
  $("#retryBtn").classList.toggle("hidden", active);
  $("#cancelProcessingBtn").classList.toggle("hidden", !active);
}

function scheduleProcessingPoll(delay = 5000) {
  clearTimeout(state.detailTimer);
  state.detailTimer = null;
  if (!state.detail?.processing_active || state.detailTab !== "jobs" || !$("#detailDialog").open) return;
  state.detailTimer = setTimeout(pollProcessing, delay);
}

async function pollProcessing() {
  if (!state.detail || state.detailTab !== "jobs" || !$("#detailDialog").open) return;
  const uid = state.detail.meeting.uid;
  try {
    const payload = await api(`/api/v1/meetings/${uid}/jobs`);
    state.detail.processing_active = Boolean(payload.processing_active);
    state.detail.jobs = Array.isArray(payload.jobs) ? payload.jobs : [];
    Object.assign(state.detail.meeting, {
      status: payload.status,
      processing_stage: payload.processing_stage,
      last_error: payload.last_error,
    });
    renderStatusStrip(payload.status, payload.processing_stage, payload.last_error);
    setProcessingControls(state.detail.processing_active);
    renderJobs(state.detail.jobs);
    if (!state.detail.processing_active) {
      await openDetail(uid, true);
      return;
    }
  } catch (error) {
    toast(t("jobs.progressUpdateFailed", { message: error.message }));
  }
  scheduleProcessingPoll();
}

function audioArtifactURL(uid, artifact) {
  return `/api/v1/meetings/${encodeURIComponent(uid)}/artifacts/${encodeURIComponent(artifact.uid)}`;
}

function isAudioArtifact(artifact) {
  return artifact.mime_type?.startsWith("audio/") || /\.(wav|opus|ogg|mp3)$/i.test(artifact.path || "");
}

function sampleSource(speaker, artifacts) {
  const preferred = speaker.source === "microphone"
    ? ["mic_wav"]
    : speaker.source === "system"
      ? ["system_wav"]
      : ["mixed_opus", "mixed_wav", "imported_mixed_wav"];
  return preferred.map((type) => artifacts.find((item) => item.type === type)).find(Boolean)
    || artifacts.find((item) => item.type === "mixed_opus")
    || artifacts.find((item) => item.type === "mixed_wav")
    || artifacts.find((item) => item.type === "system_wav")
    || artifacts.find((item) => item.type === "mic_wav")
    || artifacts.find(isAudioArtifact);
}

function clockMS(value) {
  const total = Math.max(0, Math.floor(Number(value || 0) / 1000));
  return `${String(Math.floor(total / 60)).padStart(2, "0")}:${String(total % 60).padStart(2, "0")}`;
}

function renderSpeakers(items, artifacts, uid) {
  const root = $("#speakersList");
  root.innerHTML = items.length ? "" : `<div class="hint">${t("speakers.empty")}</div>`;
  state.speakerEditor = {
    uid,
    items,
    names: new Map(items.map((speaker) => [speaker.id, speaker.display_name])),
    assignments: new Map(),
    merges: new Map(),
    selected: new Set(),
    artifacts,
    dirty: false,
  };
  const target = $("#speakerMoveTarget");
  target.innerHTML = items.map((speaker) => `<option value="${esc(speaker.id)}">${esc(speaker.display_name || speaker.id)}</option>`).join("");
  $("#speakerBulkActions").classList.toggle("hidden", !items.length);
  updateSpeakerEditorStatus();
  items.forEach((speaker) => {
    const node = document.createElement("div");
    node.className = "speaker";
    node.dataset.speakerId = speaker.id;
    const source = sampleSource(speaker, artifacts);
    const samples = Array.isArray(speaker.samples) ? speaker.samples.slice(0, 3) : [];
    const fragments = Array.isArray(speaker.fragments) ? speaker.fragments : [];
    const sampleHTML = source && samples.length
      ? `<div class="speaker-samples">${samples.map((sample, index) => {
          const start = Math.max(0, Number(sample.start_ms || 0) / 1000 - 0.25);
          const end = Math.min(start + 10, Math.max(start + 0.5, Number(sample.end_ms || 0) / 1000 + 0.35));
          const url = audioArtifactURL(uid, source);
          return `<div class="voice-sample"><span>${t("speakers.sample", { n: index + 1 })} · ${clockMS(sample.start_ms)}–${clockMS(sample.end_ms)}</span><button type="button" class="ghost small play-fragment" data-url="${esc(url)}" data-start="${start}" data-end="${end}">▶</button></div>`;
        }).join("")}</div>`
      : `<div class="hint speaker-hint">${t("speakers.noAudio")}</div>`;
    const fragmentHTML = fragments.length
      ? `<div class="speaker-fragments-head"><span>${t("speakers.fragments", { n: fragments.length })}</span><small>${t("speakers.fragmentsHint")}</small></div><div class="speaker-fragments">${fragments.map((fragment) => {
          const start = Math.max(0, Number(fragment.start_ms || 0) / 1000 - 0.15);
          const end = Math.min(start + 20, Math.max(start + 0.5, Number(fragment.end_ms || 0) / 1000 + 0.2));
          const fragmentSource = fragment.source === "microphone"
            ? artifacts.find((item) => item.type === "mic_wav")
            : artifacts.find((item) => item.type === "system_wav") || source;
          const url = fragmentSource ? audioArtifactURL(uid, fragmentSource) : "";
          return `<div class="speaker-fragment" draggable="true" data-fragment-id="${esc(fragment.id)}" data-source-speaker="${esc(speaker.id)}"><input class="fragment-select" type="checkbox" aria-label="${t("speakers.fragmentSelect")}"><button type="button" class="ghost play-fragment" data-url="${esc(url)}" data-start="${start}" data-end="${end}" ${url ? "" : "disabled"}>▶</button><div><div class="fragment-time">${clockMS(fragment.start_ms)}–${clockMS(fragment.end_ms)}</div><div class="fragment-text" title="${esc(fragment.text || "")}">${esc(fragment.text || t("speakers.noText"))}</div></div></div>`;
        }).join("")}</div>`
      : `<div class="hint speaker-hint">${t("speakers.oldFragments")}</div>`;
    node.innerHTML = `<div class="speaker-info"><strong class="speaker-card-title" draggable="true" title="${t("speakers.dragTitle")}">${esc(speaker.id)}</strong><div class="meta">${esc(speaker.source)}</div>${sampleHTML}${fragmentHTML}</div><div class="speaker-name"><input value="${esc(speaker.display_name)}" aria-label="${t("speakers.nameLabel", { id: esc(speaker.id) })}"></div>`;
    const nameInput = node.querySelector(".speaker-name input");
    nameInput.addEventListener("input", () => {
      state.speakerEditor.names.set(speaker.id, nameInput.value);
      markSpeakerEditorDirty();
      refreshSpeakerTargetNames();
    });
    node.querySelectorAll(".fragment-select").forEach((checkbox) => {
      checkbox.addEventListener("change", () => {
        const fragment = checkbox.closest(".speaker-fragment");
        if (checkbox.checked) state.speakerEditor.selected.add(fragment.dataset.fragmentId);
        else state.speakerEditor.selected.delete(fragment.dataset.fragmentId);
        updateSpeakerEditorStatus();
      });
    });
    node.querySelectorAll(".play-fragment").forEach((button) => {
      button.onclick = () => playSpeakerFragment(button.dataset.url, Number(button.dataset.start), Number(button.dataset.end));
    });
    node.querySelectorAll(".speaker-fragment").forEach((fragment) => {
      fragment.addEventListener("dragstart", (event) => {
        if (!state.speakerEditor.selected.has(fragment.dataset.fragmentId)) {
          state.speakerEditor.selected.clear();
          root.querySelectorAll(".fragment-select").forEach((item) => { item.checked = false; });
          state.speakerEditor.selected.add(fragment.dataset.fragmentId);
          fragment.querySelector(".fragment-select").checked = true;
        }
        event.dataTransfer.setData("application/x-localmeetassist-fragments", JSON.stringify([...state.speakerEditor.selected]));
        event.dataTransfer.effectAllowed = "move";
        fragment.classList.add("dragging");
      });
      fragment.addEventListener("dragend", () => fragment.classList.remove("dragging"));
    });
    const title = node.querySelector(".speaker-card-title");
    title.addEventListener("dragstart", (event) => {
      event.dataTransfer.setData("application/x-localmeetassist-speaker", speaker.id);
      event.dataTransfer.effectAllowed = "move";
    });
    node.addEventListener("dragover", (event) => { event.preventDefault(); node.classList.add("drag-over"); });
    node.addEventListener("dragleave", () => node.classList.remove("drag-over"));
    node.addEventListener("drop", (event) => {
      event.preventDefault();
      node.classList.remove("drag-over");
      const fromSpeaker = event.dataTransfer.getData("application/x-localmeetassist-speaker");
      if (fromSpeaker) {
        mergeSpeakerDraft(fromSpeaker, speaker.id);
        return;
      }
      try {
        const ids = JSON.parse(event.dataTransfer.getData("application/x-localmeetassist-fragments") || "[]");
        moveSpeakerFragments(ids, speaker.id);
      } catch { toast(t("speakers.cantReadDrop")); }
    });
    root.appendChild(node);
  });
}

function markSpeakerEditorDirty() {
  if (!state.speakerEditor) return;
  state.speakerEditor.dirty = true;
  updateSpeakerEditorStatus();
}

function updateSpeakerEditorStatus() {
  const editor = state.speakerEditor;
  if (!editor) return;
  const changes = editor.assignments.size + editor.merges.size + [...editor.names].filter(([id, name]) => editor.items.find((item) => item.id === id)?.display_name !== name).length;
  $("#speakerDirtyText").textContent = changes ? t("speakers.dirtyCount", { n: changes }) : t("speakers.selectedCount", { n: editor.selected.size });
  $("#saveSpeakerChanges").disabled = changes === 0;
  $("#discardSpeakerChanges").disabled = changes === 0;
  $("#moveSelectedSpeakerFragments").disabled = editor.selected.size === 0;
}

function refreshSpeakerTargetNames() {
  const editor = state.speakerEditor;
  if (!editor) return;
  const value = $("#speakerMoveTarget").value;
  $("#speakerMoveTarget").innerHTML = editor.items
    .filter((speaker) => !editor.merges.has(speaker.id))
    .map((speaker) => `<option value="${esc(speaker.id)}">${esc(editor.names.get(speaker.id) || speaker.id)}</option>`).join("");
  if ([...$("#speakerMoveTarget").options].some((option) => option.value === value)) $("#speakerMoveTarget").value = value;
}

function moveSpeakerFragments(ids, targetID) {
  const editor = state.speakerEditor;
  if (!editor || !ids.length) return;
  const targetNode = document.querySelector(`.speaker[data-speaker-id="${CSS.escape(targetID)}"] .speaker-fragments`);
  if (!targetNode) return;
  ids.forEach((id) => {
    const fragment = document.querySelector(`.speaker-fragment[data-fragment-id="${CSS.escape(id)}"]`);
    if (!fragment) return;
    const sourceID = fragment.closest(".speaker").dataset.speakerId;
    const source = editor.items.find((item) => item.id === sourceID);
    const target = editor.items.find((item) => item.id === targetID);
    if (source?.source !== target?.source) {
      toast(t("speakers.cantMove"));
      return;
    }
    editor.assignments.set(id, targetID);
    fragment.dataset.sourceSpeaker = targetID;
    fragment.querySelector(".fragment-select").checked = false;
    targetNode.appendChild(fragment);
    editor.selected.delete(id);
  });
  markSpeakerEditorDirty();
}

function mergeSpeakerDraft(fromID, intoID) {
  const editor = state.speakerEditor;
  if (!editor || !fromID || fromID === intoID) return;
  const source = editor.items.find((item) => item.id === fromID);
  const target = editor.items.find((item) => item.id === intoID);
  if (!source || !target || source.source !== target.source) {
    toast(t("speakers.cantMerge"));
    return;
  }
  if (!confirm(t("speakers.mergeConfirm", { from: editor.names.get(fromID) || fromID, into: editor.names.get(intoID) || intoID }))) return;
  editor.merges.set(fromID, intoID);
  const sourceNode = document.querySelector(`.speaker[data-speaker-id="${CSS.escape(fromID)}"]`);
  const fragments = [...(sourceNode?.querySelectorAll(".speaker-fragment") || [])];
  moveSpeakerFragments(fragments.map((item) => item.dataset.fragmentId), intoID);
  sourceNode?.classList.add("merged-away");
  refreshSpeakerTargetNames();
  markSpeakerEditorDirty();
}

function playSpeakerFragment(url, start, end) {
  if (!url) return;
  if (state.speakerPlayer) state.speakerPlayer.pause();
  const player = new Audio(url);
  state.speakerPlayer = player;
  player.addEventListener("loadedmetadata", () => { player.currentTime = start; player.play().catch((error) => toast(error.message)); }, { once: true });
  player.addEventListener("timeupdate", () => { if (player.currentTime >= end) player.pause(); });
}

async function saveSpeakerChanges() {
  const editor = state.speakerEditor;
  if (!editor || !editor.dirty) return;
  const names = Object.fromEntries(editor.names.entries());
  const assignments = Object.fromEntries(editor.assignments.entries());
  const merges = Object.fromEntries(editor.merges.entries());
  const button = $("#saveSpeakerChanges");
  button.disabled = true;
  try {
    await api(`/api/v1/meetings/${editor.uid}/speakers`, { method: "PATCH", json: { names, assignments, merges } });
    await openDetail(editor.uid, true);
    toast(t("speakers.saved"));
  } catch (error) {
    toast(error.message);
    button.disabled = false;
  }
}

function renderArtifacts(items, uid) {
  const root = $("#artifactsList");
  root.innerHTML = "";
  $("#audioPlayer").innerHTML = "";
  items.forEach((artifact) => {
    const node = document.createElement("div");
    node.className = `artifact${isAudioArtifact(artifact) ? " audio-artifact" : ""}`;
    const url = audioArtifactURL(uid, artifact);
    const player = isAudioArtifact(artifact) ? `<audio controls preload="none" src="${url}"></audio>` : "";
    const fileName = artifact.path.split(/[\\/]/).pop();
    node.innerHTML = `<div class="artifact-main"><strong>${esc(artifact.type)}</strong><small>${esc(fileName)} · ${(artifact.size_bytes / 1024 / 1024).toFixed(2)}${t("bytes.mb")}</small>${player}</div><div class="artifact-actions"><a class="ghost small" href="${url}" target="_blank">${t("common.open")}</a><button class="danger ghost small delete-artifact" type="button">${t("common.delete")}</button></div>`;
    node.querySelector(".delete-artifact").onclick = async () => {
      if (!confirm(t("artifacts.deleteConfirm", { name: fileName }))) return;
      try {
        await api(url, { method: "DELETE" });
        await openDetail(uid, true);
        toast(t("artifacts.deleted"));
      } catch (error) { toast(error.message); }
    };
    root.appendChild(node);
  });
}

function renderJobs(items) {
  const root = $("#jobsList");
  root.innerHTML = "";
  const retryable = new Set(["transcribing", "diarizing", "summarizing", "encoding"]);
  const visibleItems = items.filter((job) => job.stage !== "merging");
  const merging = items.find((job) => job.stage === "merging");
  const transcription = visibleItems.find((job) => job.stage === "transcribing");
  if (transcription && merging) {
    if (merging.status === "failed") {
      transcription.status = "failed";
      transcription.last_error = merging.last_error;
    } else if (merging.status === "processing") {
      transcription.status = "processing";
      transcription.progress = 85 + Math.round(Math.max(0, Math.min(100, Number(merging.progress || 0))) * 0.15);
    }
  }
  visibleItems.forEach((job) => {
    const node = document.createElement("div");
    node.className = "job";
    const progressValue = ["completed", "skipped"].includes(job.status)
      ? 100
      : Math.max(0, Math.min(99, Number(job.progress || 0)));
    const progress = `<progress class="job-progress ${esc(job.status)}" max="100" value="${progressValue}"></progress><small class="job-progress-text">${progressValue}%</small>`;
    const retry = retryable.has(job.stage)
      ? `<button type="button" class="ghost small retry-stage" data-stage="${esc(job.stage)}" ${state.detail?.processing_active ? "disabled" : ""}>${t("jobs.retryStage")}</button>`
      : "";
    node.innerHTML = `<div class="job-main"><strong>${esc(stageText(job.stage))}</strong><small>${esc(job.last_error || "")}</small>${progress}</div><div class="job-actions"><span class="badge ${job.status}">${esc(statusText(job.status))}</span>${retry}</div>`;
    const button = node.querySelector(".retry-stage");
    if (button) button.onclick = () => retryProcessing(button.dataset.stage, button);
    root.appendChild(node);
  });
}

async function retryProcessing(stage, button = null) {
  if (!state.detail) return;
  const uid = state.detail.meeting.uid;
  if (button) button.disabled = true;
  $("#retryBtn").disabled = true;
  clearTimeout(state.detailTimer);
  try {
    await api(`/api/v1/meetings/${uid}/jobs/${encodeURIComponent(stage)}/retry`, { method: "POST" });
    state.detail.meeting.status = "processing";
    state.detail.processing_active = true;
    await openDetail(uid, true);
    toast(stage === "all" ? t("jobs.retryStarted") : t("jobs.retryStageStarted", { stage: stageText(stage) }));
  } catch (error) {
    toast(error.message);
  } finally {
    if (button) button.disabled = false;
    $("#retryBtn").disabled = false;
  }
}

function showTab(name) {
  state.detailTab = name;
  $$(".tabs button").forEach((button) => button.classList.toggle("active", button.dataset.tab === name));
  $$(".tab-content").forEach((content) => content.classList.add("hidden"));
  $(`#tab-${name}`).classList.remove("hidden");
  scheduleProcessingPoll(name === "jobs" ? 250 : 5000);
}

async function saveMeetingField(field) {
  if (!state.detail) return;
  const value = field === "summary" ? $("#summaryText").value : $("#transcriptText").value;
  try {
    await api(`/api/v1/meetings/${state.detail.meeting.uid}`, { method: "PATCH", json: { [field]: value } });
    toast(t("detail.saved"));
  } catch (error) { toast(error.message); }
}

async function showDiagnostics() {
  $("#diagDialog").showModal();
  $("#diagDetails").textContent = t("common.loading");
  try {
    state.diagnostics = await api("/api/v1/diagnostics");
    $("#diagDetails").textContent = JSON.stringify(state.diagnostics, null, 2);
    await refreshLogs();
  } catch (error) {
    $("#diagDetails").textContent = error.message;
  }
}

async function refreshLogs() {
  try {
    const result = await api("/api/v1/logs?limit=200");
    $("#logDetails").textContent = (result.lines || []).join("\n") || t("diag.emptyLog");
    $("#logDetails").scrollTop = $("#logDetails").scrollHeight;
  } catch (error) {
    $("#logDetails").textContent = error.message;
  }
}

function humanBytes(value) {
  const size = Number(value || 0);
  if (size < 1024) return `${size}${t("bytes.b")}`;
  if (size < 1024 ** 2) return `${(size / 1024).toFixed(1)}${t("bytes.kb")}`;
  if (size < 1024 ** 3) return `${(size / 1024 ** 2).toFixed(1)}${t("bytes.mb")}`;
  return `${(size / 1024 ** 3).toFixed(2)}${t("bytes.gb")}`;
}

async function refreshModels() {
  clearTimeout(state.modelTimer);
  const root = $("#modelsList");
  try {
    const models = await api("/api/v1/models");
    root.innerHTML = "";
    let active = false;
    const groups = [
      { id: "runtime", title: t("models.group.runtime.title"), description: t("models.group.runtime.desc") },
      { id: "transcription", title: t("models.group.transcription.title"), description: t("models.group.transcription.desc") },
      { id: "speech_filter", title: t("models.group.speech_filter.title"), description: t("models.group.speech_filter.desc") },
      { id: "diarization", title: t("models.group.diarization.title"), description: t("models.group.diarization.desc") },
    ];
    groups.forEach((group) => {
      const entries = models.filter((item) => item.group === group.id);
      if (!entries.length) return;
      const section = document.createElement("section");
      section.className = "model-group";
      section.innerHTML = `<div class="model-group-head"><h3>${esc(group.title)}</h3><p>${esc(group.description)}</p></div><div class="model-group-items"></div>`;
      const list = section.querySelector(".model-group-items");
      entries.forEach((model) => {
        active ||= Boolean(model.downloading);
        const node = document.createElement("div");
        node.className = `model-item${model.selected ? " selected" : ""}`;
        const progress = model.total_bytes > 0
          ? Math.min(100, Math.round(model.downloaded_bytes * 100 / model.total_bytes))
          : 0;
        const selected = model.selected ? ` · ${t("models.applied")}` : "";
        const status = model.downloading
          ? (model.total_bytes > 0 ? t("models.downloadingTotal", { downloaded: humanBytes(model.downloaded_bytes), total: humanBytes(model.total_bytes), percent: progress }) : t("models.downloading", { downloaded: humanBytes(model.downloaded_bytes) }))
          : model.exists ? `${t("models.installed")} · ${humanBytes(model.size_bytes)}${selected}` : `${t("models.notInstalled")}${selected}`;
        const size = model.approx_bytes > 0 ? `${t("models.approxSize", { size: humanBytes(model.approx_bytes) })}` : "";
        const apply = model.selectable && model.exists
          ? `<button class="primary small apply-model" ${model.selected || model.downloading ? "disabled" : ""}>${model.selected ? t("models.appliedBtn") : t("models.apply")}</button>`
          : "";
        const test = model.exists ? `<button class="ghost small test-model" ${model.downloading ? "disabled" : ""}>${t("models.test")}</button>` : "";
        node.innerHTML = `<div class="model-head"><div><strong>${esc(model.name)}</strong><div class="model-description">${esc(model.description || "")}</div><small>${esc(size)}${size ? " · " : ""}${esc(model.path)}${model.license ? ` · ${t("models.license", { license: esc(model.license) })}` : ""}</small></div><div class="model-actions">${test}${apply}<button class="ghost small download-model" ${model.downloading ? "disabled" : ""}>${model.exists ? t("models.redownload") : t("models.download")}</button></div></div><div class="model-status">${esc(status)}</div><div class="model-test-result hidden"></div>${model.downloading && model.total_bytes > 0 ? `<progress max="100" value="${progress}"></progress>` : ""}${model.last_error ? `<div class="model-error">${esc(model.last_error)}</div>` : ""}`;
        node.querySelector(".download-model").onclick = async () => {
          try {
            await api(`/api/v1/models/${encodeURIComponent(model.id)}/download`, { method: "POST" });
            await refreshModels();
          } catch (error) { toast(error.message); }
        };
        const applyButton = node.querySelector(".apply-model");
        if (applyButton) applyButton.onclick = async () => {
          applyButton.disabled = true;
          try {
            await api(`/api/v1/models/${encodeURIComponent(model.id)}/apply`, { method: "POST" });
            state.settings = null;
            state.settingsDirty.clear();
            await refreshModels();
            toast(t("models.appliedToast", { name: model.name }));
          } catch (error) {
            applyButton.disabled = false;
            toast(error.message);
          }
        };
        const testButton = node.querySelector(".test-model");
        if (testButton) testButton.onclick = async () => {
          const resultNode = node.querySelector(".model-test-result");
          testButton.disabled = true;
          resultNode.className = "model-test-result";
          resultNode.textContent = t("models.testing");
          try {
            const result = await api(`/api/v1/models/${encodeURIComponent(model.id)}/test`, { method: "POST" });
            resultNode.className = `model-test-result ${result.ok ? "ok" : "error"}`;
            resultNode.textContent = `${result.message}${result.duration_ms !== undefined ? ` · ${result.duration_ms}${t("models.ms")}` : ""}${result.checks?.length ? ` · ${result.checks.join("; ")}` : ""}`;
          } catch (error) {
            resultNode.className = "model-test-result error";
            resultNode.textContent = error.message;
          } finally {
            testButton.disabled = false;
          }
        };
        list.appendChild(node);
      });
      root.appendChild(section);
    });
    if (active && $("#modelsDialog").open) state.modelTimer = setTimeout(refreshModels, 1000);
  } catch (error) {
    root.innerHTML = `<div class="model-error">${esc(error.message)}</div>`;
  }
}

async function showModels() {
  $("#modelsDialog").showModal();
  $("#modelsList").innerHTML = `<div class="hint">${t("models.checking")}</div>`;
  await refreshModels();
}

function settingValue(input) {
  return input.type === "checkbox" ? String(input.checked) : input.value;
}

function updateSettingsDirty(input) {
  const key = input.dataset.key;
  const value = settingValue(input);
  const original = input.dataset.original ?? "";
  if (value === original || (input.type === "password" && value === "")) state.settingsDirty.delete(key);
  else state.settingsDirty.set(key, value);
  const count = state.settingsDirty.size;
  $("#settingsDirty").textContent = count ? t("settings.changedCount", { n: count }) : t("settings.noChanges");
  $("#saveSettings").disabled = count === 0;
}

function hotkeyFromEvent(event) {
  const modifiers = [];
  if (event.ctrlKey) modifiers.push("Ctrl");
  if (event.altKey) modifiers.push(/Mac|iPhone|iPad/.test(navigator.platform) ? "Option" : "Alt");
  if (event.shiftKey) modifiers.push("Shift");
  if (event.metaKey) modifiers.push(/Mac|iPhone|iPad/.test(navigator.platform) ? "Cmd" : "Win");
  let key = "";
  if (/^Key[A-Z]$/.test(event.code)) key = event.code.slice(3);
  else if (/^Digit[0-9]$/.test(event.code)) key = event.code.slice(5);
  else if (/^F([1-9]|1[0-2])$/.test(event.code)) key = event.code;
  else if (event.code === "Space") key = "Space";
  if (!key || modifiers.length === 0) return "";
  return [...modifiers, key].join("+");
}

function bindHotkeyCapture(input, captureButton, clearButton) {
  let capturing = false;
  const finish = () => {
    capturing = false;
    input.classList.remove("capturing");
    captureButton.textContent = t("settings.captureHotkey");
  };
  captureButton.onclick = () => {
    capturing = true;
    input.classList.add("capturing");
    input.placeholder = t("settings.pressHotkey");
    captureButton.textContent = t("settings.capturing");
    input.focus();
  };
  clearButton.onclick = () => {
    finish();
    input.value = "";
    updateSettingsDirty(input);
  };
  input.addEventListener("blur", () => { if (capturing) finish(); });
  input.addEventListener("keydown", (event) => {
    if (!capturing) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.key === "Escape") {
      finish();
      return;
    }
    const value = hotkeyFromEvent(event);
    if (!value) return;
    const duplicate = [...document.querySelectorAll("input[data-hotkey='true']")]
      .find((candidate) => candidate !== input && candidate.value === value);
    if (duplicate) {
      toast(t("settings.duplicateHotkey", { value }));
      return;
    }
    input.value = value;
    updateSettingsDirty(input);
    finish();
  });
}

function renderSettingsGroup(groupID) {
  const groups = state.settings?.groups || [];
  const group = groups.find((item) => item.id === groupID) || groups[0];
  if (!group) return;
  state.settings.active = group.id;
  $$("#settingsNav button").forEach((button) => button.classList.toggle("active", button.dataset.group === group.id));
  $("#settingsGroupTitle").innerHTML = `<div class="settings-title-line"><div><h3>${esc(group.title)}</h3><p>${esc(group.description)}</p></div>${group.id === "summary" ? `<button id="testSummaryConnection" type="button" class="ghost small">${t("settings.testConnection")}</button>` : ""}</div><div id="settingsTestResult" class="model-test-result hidden"></div>`;
  const root = $("#settingsFields");
  root.innerHTML = "";
  let lastSection = "";
  group.fields.forEach((field) => {
    if (field.section && field.section !== lastSection) {
      const heading = document.createElement("div");
      heading.className = "settings-section-title";
      heading.textContent = field.section;
      root.appendChild(heading);
      lastSection = field.section;
    }
    const row = document.createElement("label");
    row.className = `setting-field ${field.kind === "checkbox" ? "setting-checkbox" : ""}`;
    const title = document.createElement("span");
    title.className = "setting-label";
    title.textContent = field.label;
    let input;
    if (field.kind === "select") {
      input = document.createElement("select");
      (field.options || []).forEach((item) => {
        const option = document.createElement("option");
        option.value = item.value;
        option.textContent = item.label;
        input.appendChild(option);
      });
      input.value = field.value;
    } else if (field.kind === "textarea") {
      input = document.createElement("textarea");
      input.value = field.value || "";
      input.rows = 18;
    } else {
      input = document.createElement("input");
      input.type = field.kind === "checkbox" ? "checkbox" : (field.kind === "hotkey" ? "text" : field.kind);
      if (field.kind === "checkbox") input.checked = field.value === "true";
      else input.value = field.value || "";
      if (field.kind === "number") input.step = field.integer ? "1" : "any";
      if (field.kind === "password") input.placeholder = field.secret_set ? t("settings.secretSet") : t("settings.notSet");
    }
    input.dataset.key = field.key;
    input.dataset.original = settingValue(input);
    input.autocomplete = "off";
    if (field.kind !== "hotkey") input.addEventListener(field.kind === "checkbox" || field.kind === "select" ? "change" : "input", () => updateSettingsDirty(input));
    const description = document.createElement("small");
    description.textContent = field.description;
    if (field.kind === "checkbox") {
      const line = document.createElement("span");
      line.className = "checkbox-line";
      line.append(input, title);
      row.append(line, description);
    } else {
      if (field.kind === "hotkey") {
        input.readOnly = true;
        input.dataset.hotkey = "true";
        input.placeholder = t("settings.notAssigned");
        const control = document.createElement("div");
        control.className = "setting-control hotkey-control";
        const capture = document.createElement("button");
        capture.type = "button";
        capture.className = "ghost small";
        capture.textContent = t("settings.captureHotkey");
        const clear = document.createElement("button");
        clear.type = "button";
        clear.className = "ghost small";
        clear.textContent = t("settings.clear");
        bindHotkeyCapture(input, capture, clear);
        control.append(input, capture, clear);
        row.append(title, control, description);
      } else if (field.resettable) {
        const control = document.createElement("div");
        control.className = "setting-control setting-prompt-control";
        const reset = document.createElement("button");
        reset.type = "button";
        reset.className = "ghost small";
        reset.textContent = t("settings.resetPrompt");
        reset.onclick = () => {
          input.value = field.default_value || "";
          updateSettingsDirty(input);
        };
        control.append(input, reset);
        row.append(title, control, description);
      } else {
        row.append(title, input, description);
      }
    }
    root.appendChild(row);
  });
  // Prefill the port field with the currently bound port when the user focuses
  // it, so a random port can be pinned to a stable value for the browser plugin.
  const portField = root.querySelector("[data-key='app.listen_port']");
  if (portField) {
    portField.addEventListener("focus", () => {
      if (!portField.value || portField.value === "0") {
        if (state.listenPort) portField.value = String(state.listenPort);
      }
    });
  }
  const testSummary = $("#testSummaryConnection");
  if (testSummary) testSummary.onclick = async () => {
    testSummary.disabled = true;
    const resultNode = $("#settingsTestResult");
    resultNode.className = "model-test-result";
    resultNode.textContent = t("settings.testing");
    const values = {};
    root.querySelectorAll("[data-key^='summary.']").forEach((input) => { values[input.dataset.key] = settingValue(input); });
    try {
      const result = await api("/api/v1/settings", { method: "POST", json: { group: "summary", values } });
      resultNode.className = `model-test-result ${result.ok ? "ok" : "error"}`;
      resultNode.textContent = `${result.message} · ${t("settings.testModel", { model: result.model || "—" })} · ${result.duration_ms}${t("models.ms")}`;
    } catch (error) {
      resultNode.className = "model-test-result error";
      resultNode.textContent = error.message;
    } finally {
      testSummary.disabled = false;
    }
  };
}

async function loadSettings(resetDirty = true) {
  const payload = await api("/api/v1/settings");
  state.settings = payload;
  if (resetDirty) state.settingsDirty.clear();
  const nav = $("#settingsNav");
  nav.innerHTML = "";
  (payload.groups || []).forEach((group) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "settings-nav-item";
    button.dataset.group = group.id;
    button.textContent = group.title;
    button.onclick = () => renderSettingsGroup(group.id);
    nav.appendChild(button);
  });
  $("#settingsDirty").textContent = t("settings.noChanges");
  $("#saveSettings").disabled = true;
  renderSettingsGroup(payload.groups?.[0]?.id);
}

async function showSettings() {
  $("#settingsDialog").showModal();
  $("#settingsFields").innerHTML = `<div class="hint">${t("settings.loading")}</div>`;
  $("#settingsRestart").classList.add("hidden");
  try { await loadSettings(); } catch (error) { $("#settingsFields").innerHTML = `<div class="model-error">${esc(error.message)}</div>`; }
}

async function saveSettings() {
  if (!state.settingsDirty.size) return;
  const button = $("#saveSettings");
  button.disabled = true;
  const values = Object.fromEntries(state.settingsDirty.entries());
  try {
    const result = await api("/api/v1/settings", { method: "PUT", json: { values } });
    await loadSettings();
    $("#settingsDirty").textContent = t("settings.saved");
    $("#settingsRestart").classList.toggle("hidden", !result.restart_required);
    $("#settingsRestart").textContent = result.restart_required
      ? t("settings.restartRequired", { keys: (result.restart_keys || []).join(", ") })
      : "";
    if (Object.prototype.hasOwnProperty.call(values, "app.language")) {
      setTimeout(() => window.location.reload(), 500);
      return;
    }
    toast(result.restart_required ? t("settings.savedToastRestart") : t("settings.savedToast"));
  } catch (error) {
    toast(error.message);
    button.disabled = false;
  }
}

function closeDialog(id) {
  const dialog = $(id);
  if (dialog.open) dialog.close();
  if (id === "#detailDialog") {
    clearTimeout(state.detailTimer);
    state.detailTimer = null;
  }
  if (id === "#modelsDialog") {
    clearTimeout(state.modelTimer);
    state.modelTimer = null;
  }
}

$("#startBtn").onclick = async () => {
  $("#recordTitle").value = "";
  $("#recordDialog").showModal();
  try { await refreshDevices(); } catch (error) { toast(error.message); }
};
$("#stopBtn").onclick = stopRecording;
$("#recordForm").onsubmit = (event) => { event.preventDefault(); startRecording(); };
$("#closeRecord").onclick = $("#cancelRecord").onclick = () => closeDialog("#recordDialog");

$("#createBtn").onclick = () => {
  $("#manualTitle").value = "";
  $("#manualDate").value = new Date(Date.now() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  $("#createDialog").showModal();
};
$("#createForm").onsubmit = async (event) => {
  event.preventDefault();
  try {
    await api("/api/v1/meetings", { method: "POST", json: { title: $("#manualTitle").value, started_at: new Date($("#manualDate").value).toISOString() } });
    closeDialog("#createDialog");
    await loadMeetings();
    toast(t("create.created"));
  } catch (error) { toast(error.message); }
};
$("#closeCreate").onclick = $("#cancelCreate").onclick = () => closeDialog("#createDialog");

$("#prevMonth").onclick = () => { state.month = new Date(state.month.getFullYear(), state.month.getMonth() - 1, 1); loadMeetings().catch((e) => toast(e.message)); };
$("#nextMonth").onclick = () => { state.month = new Date(state.month.getFullYear(), state.month.getMonth() + 1, 1); loadMeetings().catch((e) => toast(e.message)); };
$("#todayBtn").onclick = () => { state.month = new Date(); loadMeetings().catch((e) => toast(e.message)); };
$("#meetingSearch").addEventListener("input", () => {
  renderSearchSuggestions();
  scheduleMeetingSearch();
});
$("#meetingSearch").addEventListener("keydown", (event) => {
  if (event.key === "Enter") {
    event.preventDefault();
    clearTimeout(state.searchTimer);
    executeMeetingSearch().catch((error) => toast(error.message));
  }
});
$("#clearSearch").onclick = () => {
  $("#meetingSearch").value = "";
  executeMeetingSearch().catch((error) => toast(error.message));
};
$$('[data-search-key]').forEach((button) => { button.onclick = () => appendSearchCondition(button.dataset.searchKey); });

$$(".tabs button").forEach((button) => { button.onclick = () => showTab(button.dataset.tab); });
function closeDetailWithCheck() {
  if (state.speakerEditor?.dirty && !confirm(t("detail.closeDirtyConfirm"))) return;
  closeDialog("#detailDialog");
}
$("#closeDetail").onclick = $("#closeDetail2").onclick = closeDetailWithCheck;
$("#detailDialog").addEventListener("cancel", (event) => {
  if (state.speakerEditor?.dirty && !confirm(t("detail.closeDirtyConfirm"))) event.preventDefault();
});
$("#saveSummary").onclick = () => saveMeetingField("summary");
$("#saveTranscript").onclick = () => saveMeetingField("transcript");
$("#saveSpeakerChanges").onclick = saveSpeakerChanges;
$("#discardSpeakerChanges").onclick = () => {
  if (!state.detail) return;
  renderSpeakers(state.detail.speakers || [], state.detail.artifacts || [], state.detail.meeting.uid);
  toast(t("speakers.discarded"));
};
$("#moveSelectedSpeakerFragments").onclick = () => moveSpeakerFragments(state.speakerEditor ? [...state.speakerEditor.selected] : [], $("#speakerMoveTarget").value);
$("#detailTitle").onchange = async () => {
  if (!state.detail) return;
  try {
    await api(`/api/v1/meetings/${state.detail.meeting.uid}`, { method: "PATCH", json: { title: $("#detailTitle").value } });
    await loadMeetings();
  } catch (error) { toast(error.message); }
};
$("#deleteMeeting").onclick = async () => {
  if (!state.detail || !confirm(t("detail.deleteConfirm"))) return;
  try {
    await api(`/api/v1/meetings/${state.detail.meeting.uid}`, { method: "DELETE" });
    closeDialog("#detailDialog");
    await loadMeetings();
    toast(t("detail.deleted"));
  } catch (error) { toast(error.message); }
};
$("#retryBtn").onclick = async () => {
  await retryProcessing("all", $("#retryBtn"));
};
$("#cancelProcessingBtn").onclick = async () => {
  if (!state.detail || !confirm(t("jobs.cancelConfirm"))) return;
  const button = $("#cancelProcessingBtn");
  button.disabled = true;
  try {
    await api(`/api/v1/meetings/${state.detail.meeting.uid}/jobs/cancel`, { method: "POST" });
    toast(t("jobs.cancelRequested"));
    await openDetail(state.detail.meeting.uid, true);
  } catch (error) {
    toast(error.message);
  } finally {
    button.disabled = false;
  }
};
$("#saveMeetingProcessing").onclick = async () => {
  if (!state.detail) return;
  const value = Number($("#detailSpeakerCount").value || 0);
  if (!Number.isInteger(value) || value < 0 || value > 65) {
    toast(t("jobs.participantsError"));
    return;
  }
  try {
    await api(`/api/v1/meetings/${state.detail.meeting.uid}`, { method: "PATCH", json: { participant_count: value } });
    await openDetail(state.detail.meeting.uid, true);
    toast(t("jobs.participantsSaved"));
  } catch (error) { toast(error.message); }
};
$("#importForm").onsubmit = async (event) => {
  event.preventDefault();
  if (!state.detail) return;
  const file = $("#importFile").files[0];
  if (!file) return;
  const form = new FormData();
  form.append("file", file);
  form.append("type", $("#importType").value);
  try {
    await api(`/api/v1/meetings/${state.detail.meeting.uid}/artifacts`, { method: "POST", body: form });
    await openDetail(state.detail.meeting.uid);
    toast(t("import.attached"));
  } catch (error) { toast(error.message); }
};

$("#diagBtn").onclick = showDiagnostics;
$("#themeBtn").onclick = () => applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
$("#refreshLogs").onclick = refreshLogs;
$("#closeDiag").onclick = $("#closeDiag2").onclick = () => closeDialog("#diagDialog");
$("#modelsBtn").onclick = showModels;
$("#refreshModels").onclick = refreshModels;
$("#closeModels").onclick = $("#closeModels2").onclick = () => closeDialog("#modelsDialog");
$("#settingsBtn").onclick = showSettings;
$("#saveSettings").onclick = saveSettings;
$("#settingsForm").onsubmit = (event) => event.preventDefault();
$("#settingsDialog").addEventListener("cancel", (event) => {
  if (state.settingsDirty.size && !confirm(t("settings.closeConfirm"))) event.preventDefault();
  else state.settingsDirty.clear();
});
$("#closeSettings").onclick = $("#closeSettings2").onclick = () => {
  if (state.settingsDirty.size && !confirm(t("settings.closeConfirm"))) return;
  state.settingsDirty.clear();
  closeDialog("#settingsDialog");
};

// --- Integrations ---------------------------------------------------------

function switchIntegrationsTab(name) {
  $$("#integrationsDialog [data-igtab]").forEach((button) => button.classList.toggle("active", button.dataset.igtab === name));
  ["tokens", "browser", "audit"].forEach((tab) => $("#ig-" + tab).classList.toggle("hidden", tab !== name));
}

// resetTokenSecrets drops one-time secrets left over from an earlier visit.
// Secrets live only in memory; the server stores hashes.
function resetTokenSecrets() {
  state.tokenSecrets = new Map();
  const box = $("#browserTokenSecret");
  if (box) { box.classList.add("hidden"); box.innerHTML = ""; }
}

async function openIntegrations() {
  $("#integrationsDialog").showModal();
  resetTokenSecrets();
  await Promise.all([loadTokens(), loadBrowserConfig(), loadAudit()]);
}

function fmtExpiry(value) {
  if (!value) return "∞";
  return new Intl.DateTimeFormat(LOCALE, { day: "2-digit", month: "short", year: "numeric" }).format(new Date(value));
}

async function loadTokens() {
  const tokens = await api("/api/v1/integrations/tokens");
  const root = $("#tokenList");
  root.innerHTML = "";
  if (!tokens.length) {
    root.innerHTML = `<div class="hint">${esc(t("integrations.noTokens"))}</div>`;
    return;
  }
  tokens.forEach((token) => {
    const item = document.createElement("div");
    item.className = "token-item";
    const info = document.createElement("div");
    info.className = "token-info";
    const secret = state.tokenSecrets.get(token.id);
    info.innerHTML =
      `<div class="token-name">${esc(token.name)}</div>` +
      `<div class="token-meta">${esc(token.kind)} · …${esc(token.fingerprint || "")} · ${esc(t("integrations.expires"))} ${esc(fmtExpiry(token.expires_at))}</div>` +
      (secret ? `<div class="token-reveal"><span class="token-note">${esc(t("integrations.secretOnce"))}</span>${tokenFieldHTML(secret)}</div>` : "");
    const copy = info.querySelector(".token-field");
    if (copy) copy.onclick = () => copySecretText(secret);
    const actions = document.createElement("div");
    actions.className = "token-actions";
    if (token.kind !== "plugins") {
      const revoke = document.createElement("button");
      revoke.type = "button";
      revoke.className = "ghost small";
      revoke.textContent = t("integrations.revoke");
      revoke.onclick = () => revokeToken(token.id);
      actions.appendChild(revoke);
    }
    const regen = document.createElement("button");
    regen.type = "button";
    regen.className = "ghost small";
    regen.textContent = t("integrations.regenerate");
    regen.onclick = () => regenerateToken(token.id);
    actions.appendChild(regen);
    item.append(info, actions);
    root.appendChild(item);
  });
}

const COPY_ICON = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>`;

function maskSecret(secret) {
  return secret.length > 18 ? `${secret.slice(0, 8)}…${secret.slice(-8)}` : secret;
}

async function copySecretText(secret) {
  try {
    await navigator.clipboard.writeText(secret);
    toast(t("integrations.tokenCopied"));
  } catch (error) {
    toast(t("integrations.tokenCopyFailed"));
  }
}

// tokenFieldHTML renders a one-time secret as a single clickable control: the
// masked value and the copy icon belong to the same field, so it is obvious
// what is being copied.
function tokenFieldHTML(secret) {
  return `<button type="button" class="token-field" title="${esc(t("integrations.copyToken"))}">` +
    `<code>${esc(maskSecret(secret))}</code>${COPY_ICON}</button>`;
}

function showTokenSecret(target, secret, note) {
  const box = $(target);
  if (!box) return;
  if (!secret) { box.classList.add("hidden"); box.innerHTML = ""; return; }
  box.classList.remove("hidden");
  box.innerHTML = `<div class="token-note">${esc(note)}</div>${tokenFieldHTML(secret)}`;
  box.querySelector(".token-field").onclick = () => copySecretText(secret);
}

async function revokeToken(id) {
  try {
    await api(`/api/v1/integrations/tokens/${id}`, { method: "DELETE" });
    await loadTokens();
    toast(t("integrations.revoked"));
  } catch (error) { toast(error.message); }
}

async function regenerateToken(id) {
  try {
    const result = await api(`/api/v1/integrations/tokens/${id}/regenerate`, { method: "POST" });
    if (result.token?.id) state.tokenSecrets.set(result.token.id, result.secret);
    await loadTokens();
  } catch (error) { toast(error.message); }
}

async function submitTokenForm(event) {
  event.preventDefault();
  const name = $("#tokenName").value.trim();
  if (!name) { toast(t("integrations.nameRequired")); return; }
  try {
    const result = await api("/api/v1/integrations/tokens", {
      method: "POST",
      json: { name, kind: $("#tokenKind").value, expires_at: $("#tokenExpires").value },
    });
    if (result.token?.id) state.tokenSecrets.set(result.token.id, result.secret);
    $("#tokenName").value = "";
    $("#tokenExpires").value = "";
    await loadTokens();
  } catch (error) { toast(error.message); }
}

// --- Page masks editor ----------------------------------------------------
// The server stores masks as "Name = pattern" lines (a leading "#" disables a
// row); the UI edits them as structured rows.

function parseMaskRows(raw) {
  const rows = [];
  for (const line of String(raw || "").split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const disabled = trimmed.startsWith("#");
    const body = disabled ? trimmed.replace(/^#[ \t]*/, "") : trimmed;
    const eq = body.indexOf("=");
    if (eq < 0) continue;
    const name = body.slice(0, eq).trim();
    const pattern = body.slice(eq + 1).trim();
    if (!name || !pattern) continue;
    rows.push({ enabled: !disabled, name, pattern });
  }
  return rows;
}

function serializeMaskRows(rows) {
  return rows
    .map((row) => ({ name: String(row.name || "").trim(), pattern: String(row.pattern || "").trim(), enabled: row.enabled !== false }))
    .filter((row) => row.name && row.pattern)
    .map((row) => `${row.enabled ? "" : "# "}${row.name} = ${row.pattern}`)
    .join("\n");
}

function collectMaskRows() {
  return [...$("#maskList").querySelectorAll(".mask-row")].map((el) => ({
    enabled: el.querySelector("input[type=checkbox]").checked,
    name: el.querySelector(".mask-name").value,
    pattern: el.querySelector(".mask-pattern").value,
  }));
}

function renderMaskRows(rows) {
  const root = $("#maskList");
  root.innerHTML = "";
  if (!rows.length) {
    root.innerHTML = `<div class="mask-empty">${esc(t("integrations.masksEmpty"))}</div>`;
    return;
  }
  rows.forEach((row, index) => {
    const el = document.createElement("div");
    el.className = "mask-row";
    el.innerHTML =
      `<input type="checkbox"${row.enabled ? " checked" : ""} title="${esc(t("integrations.maskEnabled"))}">` +
      `<input type="text" class="mask-name" autocomplete="off" value="${esc(row.name || "")}" placeholder="${esc(t("integrations.maskName"))}">` +
      `<input type="text" class="mask-pattern" autocomplete="off" spellcheck="false" value="${esc(row.pattern || "")}" placeholder="*example.com/*">` +
      `<button type="button" class="mask-remove" title="${esc(t("integrations.maskRemove"))}">✕</button>`;
    el.querySelector(".mask-remove").onclick = () => {
      const current = collectMaskRows();
      current.splice(index, 1);
      renderMaskRows(current);
    };
    root.appendChild(el);
  });
}

function addEmptyMaskRow() {
  const rows = collectMaskRows();
  rows.push({ enabled: true, name: "", pattern: "" });
  renderMaskRows(rows);
  const names = $("#maskList").querySelectorAll(".mask-name");
  if (names.length) names[names.length - 1].focus();
}

function exportMasks() {
  const rows = collectMaskRows().filter((row) => String(row.name).trim() && String(row.pattern).trim());
  const blob = new Blob([JSON.stringify(rows, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = "localmeetassist-masks.json";
  link.click();
  URL.revokeObjectURL(url);
}

async function importMasksFile(file) {
  const data = JSON.parse(await file.text());
  const rows = Array.isArray(data) ? data : [];
  renderMaskRows(rows.map((row) => ({
    enabled: row.enabled !== false,
    name: String(row.name ?? ""),
    pattern: String(row.pattern ?? ""),
  })));
  toast(t("integrations.masksImported", { count: rows.length }));
}

// updatePortWarning distinguishes three states: the port is not pinned, it is
// pinned but the running listener still uses another port (restart pending), or
// everything matches.
function updatePortWarning(configured, effective) {
  const box = $("#browserPortWarning");
  const text = $("#browserPortWarningText");
  const button = $("#goToPortSetting");
  if (!box || !text || !button) return;
  if (configured === 0) {
    text.textContent = t("integrations.portWarning");
    button.classList.remove("hidden");
    box.classList.remove("hidden");
  } else if (effective !== 0 && effective !== configured) {
    text.textContent = t("integrations.portRestart", { port: configured, current: effective });
    button.classList.add("hidden");
    box.classList.remove("hidden");
  } else {
    box.classList.add("hidden");
  }
}

async function loadBrowserConfig() {
  const cfg = await api("/api/v1/integrations/browser");
  $("#browserEnabled").checked = !!cfg.enabled;
  renderMaskRows(parseMaskRows(cfg.masks || ""));
  $("#browserMode").value = cfg.mode || "auto";
  $("#browserTitleTemplate").value = cfg.title_template || "";
  $("#browserMissedPolls").value = String(cfg.stop_after_missed_polls ?? 4);
  $("#browserPoll").value = String(cfg.poll_interval_seconds ?? 20);
  updatePortWarning(Number(cfg.configured_port || 0), Number(cfg.effective_port || 0));
  const tokenBox = $("#browserTokenBox");
  if (cfg.plugins_token) {
    tokenBox.innerHTML = `${esc(t("integrations.pluginsToken"))}: …${esc(cfg.plugins_token.fingerprint || "")}`;
  } else {
    tokenBox.textContent = t("integrations.noPluginsToken");
  }
}

async function saveBrowserConfig() {
  const body = {
    enabled: $("#browserEnabled").checked,
    masks: serializeMaskRows(collectMaskRows()),
    mode: $("#browserMode").value,
    title_template: $("#browserTitleTemplate").value,
    stop_after_missed_polls: Number($("#browserMissedPolls").value || 4),
    poll_interval_seconds: Number($("#browserPoll").value || 20),
  };
  try {
    const result = await api("/api/v1/integrations/browser", { method: "PUT", json: body });
    await loadBrowserConfig();
    if (result.plugins_token_secret) {
      if (result.plugins_token_id) state.tokenSecrets.set(result.plugins_token_id, result.plugins_token_secret);
      showTokenSecret("#browserTokenSecret", result.plugins_token_secret, t("integrations.pluginsSecretOnce"));
      await loadTokens();
    }
    toast(t("integrations.saved"));
  } catch (error) { toast(error.message); }
}

async function loadAudit() {
  const data = await api("/api/v1/integrations/audit?limit=200");
  $("#auditEnabled").checked = !!data.enabled;
  renderAudit(data.entries || []);
}

function renderAudit(entries) {
  const root = $("#auditTable");
  if (!entries.length) {
    root.innerHTML = `<div class="hint">${esc(t("integrations.auditEmpty"))}</div>`;
    return;
  }
  const head = `<thead><tr><th>${esc(t("integrations.auditTime"))}</th><th>${esc(t("integrations.auditToken"))}</th><th>IP</th><th>User-Agent</th><th>${esc(t("integrations.auditMethod"))}</th><th>${esc(t("integrations.auditPath"))}</th><th>${esc(t("integrations.auditStatus"))}</th></tr></thead>`;
  const rows = entries.map((e) => `<tr><td>${esc(e.time || "")}</td><td>${esc(e.token || "")}</td><td>${esc(e.ip || "")}</td><td>${esc(e.user_agent || "")}</td><td>${esc(e.method || "")}</td><td>${esc(e.path || "")}</td><td>${esc(String(e.status ?? ""))}</td></tr>`).join("");
  root.innerHTML = `<table>${head}<tbody>${rows}</tbody></table>`;
}

$("#integrationsBtn").onclick = openIntegrations;
$("#closeIntegrations").onclick = $("#closeIntegrations2").onclick = () => closeDialog("#integrationsDialog");
$$("#integrationsDialog [data-igtab]").forEach((button) => { button.onclick = () => switchIntegrationsTab(button.dataset.igtab); });
$("#tokenForm").onsubmit = submitTokenForm;
$("#saveBrowser").onclick = saveBrowserConfig;
$("#goToPortSetting").onclick = async () => {
  closeDialog("#integrationsDialog");
  try {
    await showSettings();
    renderSettingsGroup("app");
    const port = $("#settingsFields").querySelector("[data-key='app.listen_port']");
    if (port) {
      port.focus();
      port.scrollIntoView({ block: "center" });
    }
  } catch (error) {
    toast(error.message);
  }
};
$("#addMask").onclick = addEmptyMaskRow;
$("#exportMasks").onclick = exportMasks;
$("#importMasks").onclick = () => $("#importMasksFile").click();
$("#importMasksFile").onchange = async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  try {
    await importMasksFile(file);
  } catch (error) {
    toast(t("integrations.masksImportError"));
  } finally {
    event.target.value = "";
  }
};
$("#auditEnabled").onchange = async () => {
  try {
    await api("/api/v1/integrations/audit", { method: "PUT", json: { enabled: $("#auditEnabled").checked } });
    toast(t("integrations.saved"));
  } catch (error) { toast(error.message); }
};

async function bootstrap() {
  try {
    applyTheme(localStorage.getItem("localmeetassist-theme") || "dark");
    let language = "ru";
    try {
      const config = await api("/api/v1/config");
      language = config.language || "ru";
      state.listenPort = config.listen_port || 0;
    } catch {}
    await loadLanguage(language);
    try {
      await Promise.all([loadMeetings(), refreshDevices(), refreshRecordingState(), executeMeetingSearch()]);
      connectEvents();
    } catch (error) {
      if (!isForbidden(error)) throw error;
    }
  } catch (error) {
    toast(error.message);
    $("#diagBar").className = "diag-bar error";
    $("#diagBar").textContent = t("app.bootstrapError", { message: error.message });
  }
}

bootstrap();
