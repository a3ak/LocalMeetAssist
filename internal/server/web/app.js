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
  appVersion: "",
  serverTheme: "dark",
  tokenSecrets: new Map(),
  speakerEditor: null,
  speakerPlayer: null,
  searchQuery: "",
  calendarView: "month",
  listDay: null,
  modelTab: "runtime",
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
  // Быстрая подсказка живёт в data-tip, поэтому переводим её отдельно.
  $$("[data-i18n-tip]").forEach((node) => {
    const text = t(node.dataset.i18nTip);
    node.dataset.tip = text;
    node.setAttribute("aria-label", text);
  });
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

// fmtFullDay даёт дату с годом без времени — для шапки карточки встречи.
function fmtFullDay(value) {
  return new Intl.DateTimeFormat(LOCALE, { day: "numeric", month: "long", year: "numeric" }).format(new Date(value));
}

// participantsLabel склоняет число участников по языку интерфейса.
function participantsLabel(n) {
  const lang = (LOCALE || "ru").split("-")[0];
  if (lang === "ru") {
    const m10 = n % 10, m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return `${n} участник`;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return `${n} участника`;
    return `${n} участников`;
  }
  return `${n} participant${n === 1 ? "" : "s"}`;
}

function monthBounds(date) {
  return {
    from: new Date(date.getFullYear(), date.getMonth(), 1),
    to: new Date(date.getFullYear(), date.getMonth() + 1, 1),
  };
}

async function loadMeetings() {
  const { from, to } = calendarBounds(state.month);
  const result = await api(`/api/v1/meetings?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`);
  state.meetings = Array.isArray(result) ? result : [];
  // Период сменился — выбранный день из него больше не актуален.
  state.listDay = null;
  renderCalendar();
  renderList();
  if (state.searchQuery) scheduleMeetingSearch();
}

function meetingMatchesSearch(meeting) {
  return !state.searchMatches || state.searchMatches.has(meeting.uid);
}

// День, выбранный в панели справа кнопкой «Ещё N». Режим временный: он
// сбрасывается при любом другом клике и при смене периода.
function clearListDay() {
  if (!state.listDay) return;
  state.listDay = null;
  renderList();
}

document.addEventListener("click", (event) => {
  if (event.target.closest("#meetingList") || event.target.closest(".event-more")) return;
  clearListDay();
});

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
  const month = state.calendarView !== "week";
  $("#weekdaysRow").classList.toggle("hidden", !month);
  $("#calendar").classList.toggle("hidden", !month);
  $("#weekCalendar").classList.toggle("hidden", month);
  if (!month) {
    renderWeek();
    return;
  }
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
    const dayMeetings = state.meetings
      .filter((meeting) => sameDay(new Date(meeting.started_at), day))
      // Активный фильтр не затеняет лишние встречи, а убирает их: в дне
      // остаются только подходящие, остальные не показываются вовсе.
      .filter(meetingMatchesSearch);
    // В дне помещается ограниченное число плашек, остальные считаем строкой
    // «Ещё N»: иначе короткие встречи вытесняют друг друга.
    const visible = dayMeetings.slice(0, CALENDAR_EVENTS_PER_DAY);
    visible.forEach((meeting) => {
      const event = document.createElement("button");
      event.className = `event ${meeting.status}`;
      event.textContent = `${new Date(meeting.started_at).toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" })} ${meeting.title}`;
      event.onclick = () => openDetail(meeting.uid);
      bindMeetingHover(event, meeting.uid);
      cell.appendChild(event);
    });
    if (dayMeetings.length > visible.length) {
      const more = document.createElement("button");
      more.type = "button";
      more.className = "event-more";
      more.textContent = t("calendar.moreEvents", { count: dayMeetings.length - visible.length });
      // «Ещё N» временно показывает в панели справа только этот день.
      more.onclick = () => { state.listDay = new Date(day); renderList(); };
      cell.appendChild(more);
    }
    root.appendChild(cell);
  }
}

const WEEKDAY_KEYS = ["weekday.mon", "weekday.tue", "weekday.wed", "weekday.thu", "weekday.fri", "weekday.sat", "weekday.sun"];

// Сколько плашек встреч показывать в дне до строки «Ещё N». Больше двух не
// помещается: ячейка около 100 px, плюс номер дня и сама строка «Ещё N».
const CALENDAR_EVENTS_PER_DAY = 2;

// meetingInterval returns the wall-clock start and end of a meeting.
function meetingInterval(meeting) {
  const start = new Date(meeting.started_at);
  const durationMS = Math.max(0, Number(meeting.duration_ms || 0));
  return { start, end: new Date(start.getTime() + durationMS) };
}

function fmtClock(date) {
  return date.toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" });
}

// fmtDayShort даёт дату без времени: в строке списка время показано интервалом,
// и «8 октября 2026 г. в 09:00 · 09:00–09:45» читать невозможно.
function fmtDayShort(value) {
  return new Intl.DateTimeFormat(LOCALE, { day: "numeric", month: "long" }).format(new Date(value));
}

// weekTitle renders a range such as "9 — 15 февраля 2026 г." for the week header.
function weekTitle(first, last) {
  const day = String(last.getDate());
  const month = new Intl.DateTimeFormat(LOCALE, { day: "numeric", month: "long" })
    .format(last).replace(day, "").replace(/^[,\s]+|[,\s]+$/g, "");
  const year = new Intl.DateTimeFormat(LOCALE, { year: "numeric" }).format(last);
  return `${first.getDate()} — ${last.getDate()} ${month} ${year}`;
}

// layoutDayEvents assigns overlapping meetings to lanes so they sit side by side
// instead of hiding each other.
function layoutDayEvents(items) {
  const placed = [];
  let cluster = [];
  let clusterEnd = null;
  const flush = () => {
    if (!cluster.length) return;
    const lanes = [];
    cluster.forEach((item) => {
      let lane = lanes.findIndex((end) => end <= item.start);
      if (lane < 0) { lanes.push(item.end); lane = lanes.length - 1; } else { lanes[lane] = item.end; }
      item.lane = lane;
    });
    cluster.forEach((item) => { item.lanes = lanes.length; });
    placed.push(...cluster);
    cluster = [];
    clusterEnd = null;
  };
  items.forEach((item) => {
    if (clusterEnd && item.start >= clusterEnd) flush();
    cluster.push(item);
    clusterEnd = clusterEnd && clusterEnd > item.end ? clusterEnd : item.end;
  });
  flush();
  return placed;
}

// renderWeek draws the timeline: one column per day, hour marks shared by all
// columns, and every meeting placed at its own time.
function renderWeek() {
  const { start } = weekBounds(state.month);
  const days = [];
  for (let index = 0; index < 7; index += 1) {
    const day = new Date(start);
    day.setDate(start.getDate() + index);
    days.push(day);
  }
  $("#monthTitle").textContent = weekTitle(days[0], days[6]);

  const perDay = days.map((day) => state.meetings
    .map((meeting) => ({ meeting, ...meetingInterval(meeting) }))
    .filter((item) => sameDay(item.start, day))
    // Активный фильтр убирает неподходящие встречи и из недельной сетки.
    .filter((item) => meetingMatchesSearch(item.meeting))
    .sort((a, b) => a.start - b.start));

  // The visible window follows the meetings but never gets shorter than 09–19.
  let firstHour = 9;
  let lastHour = 19;
  perDay.flat().forEach((item) => {
    firstHour = Math.min(firstHour, item.start.getHours());
    lastHour = Math.max(lastHour, item.end.getHours() + (item.end.getMinutes() ? 1 : 0));
  });
  firstHour = Math.max(0, firstHour);
  lastHour = Math.min(24, Math.max(lastHour, firstHour + 2));
  const rows = lastHour - firstHour;

  const today = new Date();
  const root = $("#weekCalendar");
  root.innerHTML = "";
  const head = document.createElement("div");
  head.className = "week-head";
  head.innerHTML = `<div class="wh"></div>${days.map((day, index) => `<div class="wh${sameDay(day, today) ? " today" : ""}">${esc(t(WEEKDAY_KEYS[index]))}<b>${day.getDate()}</b></div>`).join("")}`;
  root.appendChild(head);

  const body = document.createElement("div");
  body.className = "week-body";
  const hours = document.createElement("div");
  hours.className = "week-hours";
  hours.style.gridTemplateRows = `repeat(${rows},1fr)`;
  for (let hour = firstHour; hour < lastHour; hour += 1) {
    const label = document.createElement("span");
    label.textContent = `${String(hour).padStart(2, "0")}:00`;
    hours.appendChild(label);
  }
  body.appendChild(hours);

  perDay.forEach((items) => {
    const column = document.createElement("div");
    column.className = "week-col";
    for (let row = 0; row < rows; row += 1) {
      const line = document.createElement("div");
      line.className = "hour-line";
      line.style.top = `${(row / rows) * 100}%`;
      column.appendChild(line);
    }
    layoutDayEvents(items).forEach((item) => {
      const event = document.createElement("button");
      const matches = meetingMatchesSearch(item.meeting);
      event.className = `event ${item.meeting.status}${state.searchMatches ? (matches ? " search-match" : " search-dimmed") : ""}`;
      const startHour = item.start.getHours() + item.start.getMinutes() / 60;
      const lengthHours = Math.max(item.end - item.start, 15 * 60 * 1000) / 3600000;
      event.style.top = `${((startHour - firstHour) / rows) * 100}%`;
      event.style.height = `${(lengthHours / rows) * 100}%`;
      if (item.lanes > 1) {
        event.style.left = `calc(${(item.lane / item.lanes) * 100}% + 3px)`;
        event.style.width = `calc(${100 / item.lanes}% - 5px)`;
        event.style.right = "auto";
      }
      const startText = fmtClock(item.start);
      const endText = fmtClock(item.end);
      const range = startText === endText ? startText : `${startText} – ${endText}`;
      event.innerHTML = `<small>${esc(range)}</small>${esc(item.meeting.title)}`;
      event.onclick = () => openDetail(item.meeting.uid);
      bindMeetingHover(event, item.meeting.uid);
      column.appendChild(event);
    });
    body.appendChild(column);
  });
  root.appendChild(body);
}

// revealMeetingCard mirrors a hovered calendar entry onto its card in the
// sidebar, so a block too small for a title still shows the full text,
// participants and topic. The list only scrolls when the card is out of view.
function revealMeetingCard(uid, active) {
  const card = document.querySelector(`.meeting-item[data-uid="${uid}"]`);
  if (!card) return;
  card.classList.toggle("hovered", active);
  if (active) card.scrollIntoView({ block: "nearest", behavior: "smooth" });
}

// bindMeetingHover wires a calendar entry to its sidebar card.
function bindMeetingHover(element, uid) {
  element.onmouseenter = () => revealMeetingCard(uid, true);
  element.onmouseleave = () => revealMeetingCard(uid, false);
  element.onblur = () => revealMeetingCard(uid, false);
  element.onfocus = () => revealMeetingCard(uid, true);
}

// listMeetings returns what the right panel shows: the whole loaded period by
// default, or a single day while the user is looking at one.
function listMeetings() {
  const base = state.listDay
    ? state.meetings.filter((meeting) => sameDay(new Date(meeting.started_at), state.listDay))
    : state.meetings;
  return base.filter(meetingMatchesSearch);
}

function renderList() {
  const root = $("#meetingList");
  root.innerHTML = "";
  // Фильтр скрывает неподходящие встречи и в списке: показываем только то, что
  // нашлось, а счётчик считает именно показанные.
  const shown = listMeetings();
  $("#meetingCount").textContent = shown.length;
  $("#meetingListTitle").textContent = state.listDay
    ? new Intl.DateTimeFormat(LOCALE, { day: "numeric", month: "long" }).format(state.listDay)
    : t("meetings.title");
  shown.forEach((meeting) => {
    const node = document.createElement("div");
    node.className = "meeting-item";
    const topic = meetingTopic(meeting.summary);
    const participants = activeParticipants(meeting.transcript, 5);
    const participantText = participants.names.length
      ? `${participants.names.map(esc).join(", ")}${participants.more ? ` <span class="more-participants">+${participants.more}</span>` : ""}`
      : t("list.noParticipants");
    const { start, end } = meetingInterval(meeting);
    const range = `${fmtClock(start)}–${fmtClock(end)}`;
    // В режиме одного дня дата уже стоит в заголовке панели, поэтому в строке
    // остаётся только время.
    const stamp = state.listDay ? esc(range) : `${esc(fmtDayShort(meeting.started_at))} · ${esc(range)}`;
    node.innerHTML = `<div class="meta">${stamp}</div><strong>${esc(meeting.title)}</strong><div class="meeting-meta-line"><span>${formatDuration(meeting.duration_ms)}</span><span class="badge ${meeting.status}">${statusText(meeting.status)}</span></div>${topic ? `<div class="meeting-topic" title="${esc(topic)}">${esc(topic)}</div>` : ""}${participants.names.length ? `<div class="meeting-participants">${participantText}</div>` : ""}`;
    node.dataset.uid = meeting.uid;
    node.onclick = () => openDetail(meeting.uid);
    root.appendChild(node);
  });
  if (!shown.length) root.innerHTML = `<div class="hint">${t(state.searchMatches ? "list.noMatches" : "list.noMeetings")}</div>`;
  if (state.listDay) {
    const back = document.createElement("button");
    back.type = "button";
    back.className = "ghost small list-back";
    back.textContent = t("list.allPeriod");
    back.onclick = () => { state.listDay = null; renderList(); };
    root.appendChild(back);
  }
}

function formatDuration(durationMS) {
  const total = Math.max(0, Math.round(Number(durationMS || 0) / 1000));
  if (!total) return t("duration.none");
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  return hours ? t("duration.hours", { hours, minutes }) : t("duration.minutes", { minutes: Math.max(1, minutes) });
}

// Section titles a minutes template can emit. They are printed in the response
// language, so both the Russian and the English spelling of every title is
// listed; the older Markdown template is covered as well.
const SUMMARY_TITLES = ["brief summary", "краткий итог", "краткое содержание", "participants", "участники", "discussed topics", "обсуждённые темы", "обсужденные темы", "decisions", "решения", "action items", "задачи", "open questions and risks", "открытые вопросы и риски", "открытые вопросы", "риски"];

function summaryTitleText(line) {
  return line.replace(/^#{1,6}\s*/, "").replace(/[:\s]+$/, "").trim().toLowerCase();
}

function isBriefSummaryTitle(line) {
  const text = summaryTitleText(line);
  return text === "brief summary" || text === "краткий итог" || text === "краткое содержание";
}

function isSectionTitle(line) {
  return line.startsWith("#") || SUMMARY_TITLES.includes(summaryTitleText(line));
}

function meetingTopic(summary = "") {
  const lines = String(summary).split(/\r?\n/).map((line) => line.trim());
  const briefIndex = lines.findIndex(isBriefSummaryTitle);
  const candidates = briefIndex >= 0 ? lines.slice(briefIndex + 1) : lines;
  let frontMatter = false;
  for (const line of candidates) {
    if (line === "---") { frontMatter = !frontMatter; continue; }
    if (frontMatter || !line || /^[-*_]+$/.test(line)) continue;
    if (isSectionTitle(line)) {
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

// applyTheme switches the colour scheme and caches it so the next paint does not
// flash the wrong theme while /api/v1/config is still in flight. The stored value
// is only a cache: config.toml stays the source of truth.
function applyTheme(theme) {
  const selected = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = selected;
  localStorage.setItem("localmeetassist-theme", selected);
}

// calendarBounds returns the fetch window for the current view: a whole week in
// week mode, so a week that spans two months still has all its meetings.
function calendarBounds(date) {
  if (state.calendarView !== "week") return monthBounds(date);
  const { start, end } = weekBounds(date);
  return { from: start, to: end };
}

// weekBounds returns Monday..Sunday around the anchor date.
function weekBounds(date) {
  const start = new Date(date.getFullYear(), date.getMonth(), date.getDate());
  start.setDate(start.getDate() - ((start.getDay() + 6) % 7));
  start.setHours(0, 0, 0, 0);
  const end = new Date(start);
  end.setDate(start.getDate() + 7);
  return { start, end };
}

// shiftCalendar moves the anchor by one month or one week, depending on the view.
function shiftCalendar(direction) {
  if (state.calendarView === "week") {
    const next = new Date(state.month);
    next.setDate(next.getDate() + direction * 7);
    state.month = next;
  } else {
    state.month = new Date(state.month.getFullYear(), state.month.getMonth() + direction, 1);
  }
  loadMeetings().catch((error) => toast(error.message));
}

// setCalendarView switches between the month grid and the week timeline.
function setCalendarView(view) {
  state.calendarView = view === "week" ? "week" : "month";
  localStorage.setItem("localmeetassist-calendar-view", state.calendarView);
  $$("#calendarView button").forEach((button) => {
    button.classList.toggle("active", button.dataset.view === state.calendarView);
  });
  renderCalendar();
}

// toggleSearchPanel reveals the filter row behind the funnel icon.
function toggleSearchPanel(force) {
  const panel = $("#searchPanel");
  const show = typeof force === "boolean" ? force : panel.classList.contains("hidden");
  panel.classList.toggle("hidden", !show);
  $("#filterToggle").classList.toggle("active", show);
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
  // The audio line lives in the diagnostics dialog; the button only carries a
  // small marker while something is wrong. The marker is cosmetic, so a missing
  // element must never break the rest of the startup sequence.
  const bar = $("#diagAudio");
  bar.className = `diag-status ${ready ? "ok" : "error"}`;
  bar.textContent = ready
    ? t("record.audioReady", { mics: devices.microphones.length, systems: devices.system_sources.length })
    : t("record.audioNotReady", { reason: warnings.join(" ") || t("record.noMic") });
  const badge = $("#diagBadge");
  if (badge) badge.classList.toggle("hidden", ready);
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
    // Длительность в шапке — «45 мин», а не «2700 сек».
    const durationText = formatDuration(meeting.duration_ms);
    const interval = meetingInterval(meeting);
    const metaParts = [fmtFullDay(meeting.started_at), `${fmtClock(interval.start)}–${fmtClock(interval.end)}`, durationText];
    if (meeting.speaker_count > 0) metaParts.push(participantsLabel(meeting.speaker_count));
    $("#detailMeta").textContent = metaParts.join(" · ");
    renderStatusStrip(meeting.status, meeting.processing_stage, meeting.last_error);
    // Счётчик спикеров на вкладке: он подсказывает, есть ли что смотреть.
    const speakerCount = Number(meeting.speaker_count || 0);
    const speakersBadge = $("#speakersTabCount");
    if (speakersBadge) {
      speakersBadge.textContent = speakerCount ? String(speakerCount) : "";
      speakersBadge.classList.toggle("hidden", !speakerCount);
    }
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
  const CHECK = `<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" aria-hidden="true"><path d="M5 13l4 4L19 7"/></svg>`;
  const pill = ["completed", "failed", "processing", "recording", "queued", "starting"].includes(status) ? status : "queued";
  const stageHint = stage && !["completed", "idle", ""].includes(stage) ? ` · ${esc(stageText(stage))}` : "";
  $("#statusStrip").innerHTML = `<span class="badge ${pill}">${status === "completed" ? CHECK : ""}${esc(statusText(status))}${stageHint}</span>${lastError ? `<span class="detail-error">${esc(lastError)}</span>` : ""}`;
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
  renderAudioConversion(items);
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

// Преобразование форматов — действие над файлами встречи, поэтому живёт во
// вкладке «Файлы», а не в списке этапов: оно доступно и после завершения
// обработки, и не зависит от настройки хранения.
function renderAudioConversion(items) {
  const root = $("#audioActions");
  const types = new Set(items.map((artifact) => artifact.type));
  const hasWAV = types.has("mixed_wav") || (types.has("mic_wav") && types.has("system_wav"));
  const hasPacked = types.has("mixed_opus") || types.has("mixed_mp3");
  const buttons = [];
  if (hasWAV) buttons.push(`<button type="button" class="ghost small" data-format="opus">${t("jobs.toOpus")}</button>`);
  if (hasPacked) buttons.push(`<button type="button" class="ghost small" data-format="wav">${t("jobs.toWav")}</button>`);
  root.classList.toggle("hidden", !buttons.length);
  root.innerHTML = buttons.length ? `<span class="audio-actions-title">${t("artifacts.convertTitle")}</span>${buttons.join("")}` : "";
  root.querySelectorAll("[data-format]").forEach((button) => {
    button.onclick = () => retryProcessing("encoding", button, button.dataset.format);
  });
}

function renderJobs(items) {
  const root = $("#jobsList");
  root.innerHTML = "";
  const retryable = new Set(["transcribing", "diarizing", "summarizing", "encoding"]);
  // merging и completed — служебные отметки: первая дублирует транскрибацию,
  // вторая просто повторяет статус встречи и своей информации не несёт.
  const visibleItems = items.filter((job) => job.stage !== "merging" && job.stage !== "completed");
  // Этап сохранения аудио мог не выполняться ни разу: встречи, обработанные до
  // его появления, не имеют такой записи. Без строки его нельзя запустить вручную.
  if (!visibleItems.some((job) => job.stage === "encoding")) {
    visibleItems.push({ stage: "encoding", status: "not_started", progress: 0, last_error: "" });
  }
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
      ? `<button type="button" class="ghost small retry-stage" data-stage="${esc(job.stage)}" ${state.detail?.processing_active ? "disabled" : ""}>${t(job.status === "not_started" ? "jobs.runStage" : "jobs.retryStage")}</button>`
      : "";
    node.innerHTML = `<div class="job-main"><strong>${esc(stageText(job.stage))}</strong><small>${esc(job.last_error || "")}</small>${progress}</div><div class="job-actions"><span class="badge ${job.status}">${esc(statusText(job.status))}</span>${retry}</div>`;
    const button = node.querySelector(".retry-stage");
    if (button) button.onclick = () => retryProcessing(button.dataset.stage, button);
    root.appendChild(node);
  });
}

async function retryProcessing(stage, button = null, format = "") {
  if (!state.detail) return;
  const uid = state.detail.meeting.uid;
  if (button) button.disabled = true;
  $("#retryBtn").disabled = true;
  clearTimeout(state.detailTimer);
  try {
    const query = format ? `?format=${encodeURIComponent(format)}` : "";
    await api(`/api/v1/meetings/${uid}/jobs/${encodeURIComponent(stage)}/retry${query}`, { method: "POST" });
    state.detail.meeting.status = "processing";
    state.detail.processing_active = true;
    await openDetail(uid, true);
    toast(stage === "all"
      ? t("jobs.retryStarted")
      : (format ? t("jobs.convertStarted") : t("jobs.retryStageStarted", { stage: stageText(stage) })));
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
    renderDiagDevices(state.diagnostics);
    await refreshLogs();
  } catch (error) {
    $("#diagDetails").textContent = error.message;
  }
}

// Карточки устройств: что найдено, доступно ли и через какой драйвер.
function renderDiagDevices(diagnostics) {
  const root = $("#diagDevices");
  if (!root) return;
  const audio = diagnostics?.audio || {};
  const available = audio.available !== false;
  // Карточки растягиваются на одну высоту, а подпись драйвера прижата к низу:
  // иначе микрофон и системный звук стояли на разной высоте.
  const device = (icon, title, name) => `<div class="card diag-device">
      <div class="card-head"><div><h3>${icon} ${esc(title)}</h3></div></div>
      <div class="diag-device-name">${esc(name || t("diag.noDevice"))}</div>
      <div class="diag-device-foot">
        <span class="pill ${available ? "ok" : "error"}">${esc(available ? t("diag.available") : t("diag.unavailable"))}</span>
        <small class="field-hint">${esc(audio.backend || "")}</small>
      </div>
    </div>`;
  const microphones = audio.microphones || [];
  const systemSources = audio.system_sources || [];
  root.innerHTML = device("🎤", t("diag.microphone"), microphones[0]?.name)
    + device("🔊", t("diag.systemAudio"), systemSources[0]?.name);
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
  // Подсветка вкладки всегда следует за состоянием: иначе после первого
  // открытия активной выглядит одна вкладка, а список показывает другую.
  // Загрузка рекомендованного набора: сервер сам запускает скачивание и включает
// шаги обработки, которым эти модели служат.
$("#downloadRecommended").onclick = async () => {
  const button = $("#downloadRecommended");
  button.disabled = true;
  try {
    const result = await api("/api/v1/models/recommended", { method: "POST" });
    const started = Array.isArray(result?.started) ? result.started.length : 0;
    toast(started ? t("models.recommendedStarted", { n: started }) : t("models.recommendedNothing"));
    await refreshModels();
  } catch (error) {
    toast(error.message);
  } finally {
    button.disabled = false;
  }
};

$$("#modelTabs [data-mgroup]").forEach((button) => {
    button.classList.toggle("active", button.dataset.mgroup === state.modelTab);
  });
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
    // Вкладка показывает свою часть: VAD относится к транскрибации, как в макете.
    const tabGroups = state.modelTab === "speakers" ? ["diarization"]
      : (state.modelTab === "runtime" ? ["runtime"] : ["transcription", "speech_filter"]);
    groups.filter((group) => tabGroups.includes(group.id)).forEach((group) => {
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
        // Раскладка карточки как в макете: заголовок с пометкой, описание,
        // размер и лицензия, пилюля состояния, ряд кнопок и сворачиваемый путь
        // к файлу. Длинный путь в строке выдавливал кнопки за край карточки.
        // Подписи «Применена» и «Установлена» не влезали в угол карточки: их
        // заменили значки. Текст остался в подсказке при наведении.
        const installedState = model.downloading ? "downloading" : (model.exists ? "on" : "off");
        const installedLabel = model.downloading
          ? t("models.downloadingShort")
          : (model.exists ? t("models.installed") : t("models.notInstalled"));
        const appliedLabel = model.selected ? t("models.appliedBtn") : t("models.notApplied");
        const flags = `<span class="model-flags">`
          + `<span class="model-flag installed ${installedState}" title="${esc(installedLabel)}" role="img" aria-label="${esc(installedLabel)}">${MODEL_INSTALLED_ICON}</span>`
          + `<span class="model-flag applied ${model.selected ? "on" : "off"}" title="${esc(appliedLabel)}" role="img" aria-label="${esc(appliedLabel)}">${MODEL_CHECK_ICON}</span>`
          + `</span>`;
        const sizeText = model.exists ? humanBytes(model.size_bytes) : (model.approx_bytes > 0 ? t("models.approxSize", { size: humanBytes(model.approx_bytes) }) : "");
        const licenseText = model.license ? `<span>${esc(t("models.license", { license: model.license }))}</span>` : "";
        node.innerHTML = `<div class="model-title"><strong>${esc(model.name)}</strong>${flags}</div>`
          + `<div class="model-description">${esc(model.description || "")}</div>`
          + `<div class="model-meta"><span>${esc(sizeText)}</span>${licenseText}</div>`
          + `<div class="model-actions">${test}${apply}<button class="ghost small download-model" ${model.downloading ? "disabled" : ""}>${model.exists ? t("models.redownload") : t("models.download")}</button></div>`
          + `<div class="model-test-result hidden"></div>`
          + (model.downloading && model.total_bytes > 0 ? `<progress max="100" value="${progress}"></progress>` : "")
          + (model.last_error ? `<div class="model-error">${esc(model.last_error)}</div>` : "")
          // Строку с файлом показываем только у скачанной модели: у отсутствующей
          // нет ни пути для показа, ни файла для удаления.
          + (model.exists && model.path ? `<details class="model-file"><summary><span>${esc(t("models.file"))}</span><span class="model-file-tools"><button type="button" class="model-delete" title="${esc(t("models.delete"))}">${TRASH_ICON}</button></span></summary><code>${esc(model.path)}</code></details>` : "");
        const removeButton = node.querySelector(".model-delete");
        if (removeButton) removeButton.onclick = async (event) => {
          // Кнопка внутри summary: без этого клик ещё и раскрывал бы блок.
          event.preventDefault();
          event.stopPropagation();
          if (!confirm(t("models.deleteConfirm", { name: model.name }))) return;
          try {
            await api(`/api/v1/models/${encodeURIComponent(model.id)}/delete`, { method: "POST" });
            toast(t("models.deleted"));
            await refreshModels();
          } catch (error) { toast(error.message); }
        };
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

$$("#modelTabs [data-mgroup]").forEach((button) => {
  button.onclick = () => {
    state.modelTab = button.dataset.mgroup;
    $$("#modelTabs [data-mgroup]").forEach((other) => other.classList.toggle("active", other === button));
    refreshModels();
  };
});

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

// applyAppVersion renders the build version next to the logo. It stays muted and
// stays hidden when the server did not report a version.
function applyAppVersion() {
  const badge = $("#appVersion");
  if (!badge) return;
  badge.textContent = state.appVersion || "";
  badge.classList.toggle("hidden", !state.appVersion);
  badge.title = state.appVersion ? `${t("settings.version")} ${state.appVersion}` : "";
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
  // The version is informational, not a setting: it is appended below the last
  // field of the application group and never part of the edited values.
  if (group.id === "app" && state.appVersion) {
    const version = document.createElement("div");
    version.className = "settings-version";
    version.textContent = `${t("settings.version")} ${state.appVersion}`;
    root.appendChild(version);
  }
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
  const GROUP_SECTIONS = [
    { caption: "settings.group.basic", ids: ["app", "audio", "desktop"] },
    { caption: "settings.group.processing", ids: ["transcription", "diarization", "summary"] },
    { caption: "settings.group.system", ids: ["inference", "storage", "models", "logging"] },
  ];
  const byId = new Map((payload.groups || []).map((group) => [group.id, group]));
  const addButton = (group) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "settings-nav-item";
    button.dataset.group = group.id;
    button.textContent = group.title;
    button.onclick = () => renderSettingsGroup(group.id);
    nav.appendChild(button);
  };
  const nav = $("#settingsNav");
  nav.innerHTML = "";
  const used = new Set();
  GROUP_SECTIONS.forEach((section) => {
    const groups = section.ids.map((id) => byId.get(id)).filter(Boolean);
    if (!groups.length) return;
    const caption = document.createElement("div");
    caption.className = "settings-nav-caption";
    caption.textContent = t(section.caption);
    nav.appendChild(caption);
    groups.forEach((group) => { used.add(group.id); addButton(group); });
  });
  (payload.groups || []).forEach((group) => { if (!used.has(group.id)) addButton(group); });
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
    if (Object.prototype.hasOwnProperty.call(values, "app.theme")) {
      applyTheme(values["app.theme"]);
    }
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

$("#prevMonth").onclick = () => shiftCalendar(-1);
$("#nextMonth").onclick = () => shiftCalendar(1);
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
$("#closeDetail").onclick = closeDetailWithCheck;
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
$$("#calendarView button").forEach((button) => { button.onclick = () => setCalendarView(button.dataset.view); });
$("#filterToggle").onclick = () => toggleSearchPanel();
$("#refreshLogs").onclick = refreshLogs;
$("#closeDiag").onclick = () => closeDialog("#diagDialog");
$("#modelsBtn").onclick = showModels;
$("#refreshModels").onclick = refreshModels;
$("#deleteAllModels").onclick = async () => {
  if (!confirm(t("models.deleteAllConfirm"))) return;
  try {
    const result = await api("/api/v1/models/delete-all", { method: "POST" });
    toast(t("models.deletedAll", { n: Number(result?.removed || 0) }));
    await refreshModels();
  } catch (error) { toast(error.message); }
};
$("#closeModels").onclick = () => closeDialog("#modelsDialog");
$("#settingsBtn").onclick = showSettings;
$("#saveSettings").onclick = saveSettings;
$("#settingsForm").onsubmit = (event) => event.preventDefault();
$("#settingsDialog").addEventListener("cancel", (event) => {
  if (state.settingsDirty.size && !confirm(t("settings.closeConfirm"))) event.preventDefault();
  else state.settingsDirty.clear();
});
$("#closeSettings").onclick = () => {
  if (state.settingsDirty.size && !confirm(t("settings.closeConfirm"))) return;
  state.settingsDirty.clear();
  closeDialog("#settingsDialog");
};

// --- Integrations ---------------------------------------------------------

function switchIntegrationsTab(name) {
  $$("#integrationsDialog [data-igtab]").forEach((button) => button.classList.toggle("active", button.dataset.igtab === name));
  // Вкладка «Токены» временно скрыта; её раздел остаётся в разметке, поэтому
  // переключаем только видимые.
  ["browser", "audit"].forEach((tab) => $("#ig-" + tab).classList.toggle("hidden", tab !== name));
  // Настройки браузера сохраняются кнопкой в шапке; аудит пишется сразу при
  // переключении, а токены создаются своей формой — там кнопка не нужна.
  $("#saveBrowser").classList.toggle("hidden", name !== "browser");
  if (name === "browser") updateBrowserDirty();
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
  switchIntegrationsTab("browser");
  setTokenFormOpen(false);
  resetTokenSecrets();
  await Promise.all([loadTokens(), loadBrowserConfig(), loadAudit()]);
}

function fmtExpiry(value) {
  if (!value) return "∞";
  return new Intl.DateTimeFormat(LOCALE, { day: "2-digit", month: "short", year: "numeric" }).format(new Date(value));
}

// Идентификатор служебного токена плагина: заполняется при загрузке списка.
let pluginsTokenID = "";

// Копировать можно только тот токен, значение которого известно: сервер
// показывает секрет один раз — при создании или перегенерации.
function updatePluginCopyState() {
  const button = $("#copyPluginToken");
  if (!button) return;
  const secret = pluginsTokenID ? state.tokenSecrets.get(pluginsTokenID) : "";
  button.disabled = !secret;
  button.setAttribute("data-tip", secret ? t("integrations.copyPluginBuffer") : t("integrations.copyPluginToken"));
}

async function loadTokens() {
  const tokens = await api("/api/v1/integrations/tokens");
  const plugin = tokens.find((token) => token.kind === "plugins");
  const others = tokens.filter((token) => token.kind !== "plugins");

  // Служебный токен плагина — отдельная карточка. Его идентификатор нужен
  // разделу «Браузер», чтобы перегенерировать токен прямо там.
  const pluginCard = $("#pluginTokenCard");
  pluginsTokenID = plugin ? plugin.id : "";
  if (plugin) {
    const secret = state.tokenSecrets.get(plugin.id);
    pluginCard.classList.remove("hidden");
    pluginCard.innerHTML =
      `<div class="card-head">` +
        `<div class="icon-tile">${GLOBE_ICON}</div>` +
        `<div class="plugin-token-title"><h3>${esc(plugin.name)}</h3><p>${esc(t("integrations.pluginSubtitle"))}</p>` +
          `<div class="plugin-token-meta"><span class="pill">${esc(t("integrations.tabBrowser"))}</span><span class="plugin-token-tail">…${esc(plugin.fingerprint || "")}</span></div></div>` +
      `</div>` +
      `<div class="plugin-token-foot">` +
        `<span class="status-line"><span class="status-dot ok"></span>${esc(t("integrations.pluginActive"))}</span>` +
        `<button type="button" class="ghost small renew-plugin">${esc(t("integrations.renewToken"))}</button>` +
      `</div>` +
      (secret ? `<div class="token-reveal"><span class="token-note">${esc(t("integrations.secretOnce"))}</span>${tokenFieldHTML(secret)}</div>` : "");
    const copy = pluginCard.querySelector(".token-field");
    if (copy) copy.onclick = () => copySecretText(secret);
    const renew = pluginCard.querySelector(".renew-plugin");
    renew.onclick = () => regenerateToken(plugin.id);
  } else {
    pluginCard.classList.add("hidden");
  }

  // Остальные токены — таблица.
  const root = $("#tokenList");
  root.innerHTML = "";
  if (!others.length) {
    root.innerHTML = `<div class="hint">${esc(t("integrations.noTokens"))}</div>`;
    return;
  }
  others.forEach((token) => {
    const item = document.createElement("div");
    item.className = "token-item";
    const secret = state.tokenSecrets.get(token.id);
    item.innerHTML =
      `<div class="token-cell token-name-cell"><div class="token-name">${esc(token.name)}</div><div class="token-meta">…${esc(token.fingerprint || "")}</div></div>` +
      `<div class="token-cell">${esc(token.kind === "read_only" ? t("integrations.readOnly") : t("integrations.readWrite"))}</div>` +
      `<div class="token-cell">${esc(fmtExpiry(token.expires_at))}</div>` +
      `<div class="token-actions">` +
        `<button type="button" class="ghost small" data-act="regen">${esc(t("integrations.regenerate"))}</button>` +
        `<button type="button" class="ghost small" data-act="revoke">${esc(t("integrations.revoke"))}</button>` +
      `</div>` +
      (secret ? `<div class="token-reveal"><span class="token-note">${esc(t("integrations.secretOnce"))}</span>${tokenFieldHTML(secret)}</div>` : "");
    item.querySelector('[data-act="regen"]').onclick = () => regenerateToken(token.id);
    item.querySelector('[data-act="revoke"]').onclick = () => revokeToken(token.id);
    const copy = item.querySelector(".token-field");
    if (copy) copy.onclick = () => copySecretText(secret);
    root.appendChild(item);
  });
}

// Индикаторы модели: галка в круге — применена, стрелка на диск — установлена.
const MODEL_CHECK_ICON = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 13l4 4L19 7"/></svg>`;
const MODEL_INSTALLED_ICON = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3v11"/><path d="M7.5 10.5L12 15l4.5-4.5"/><rect x="4" y="17.5" width="16" height="3" rx="1.5"/></svg>`;

const KEY_ICON = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="7.5" cy="15.5" r="3.5"/><path d="M10 13l8-8m-3 3l3 3m-5-1l2 2"/></svg>`;
const TRASH_ICON = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>`;
const GLOBE_ICON = `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/></svg>`;

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
    setTokenFormOpen(false);
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
    // Название и шаблон стоят в одной строке: две строки занимали вдвое больше
    // места без всякой пользы.
    el.innerHTML =
      `<input type="checkbox"${row.enabled ? " checked" : ""} title="${esc(t("integrations.maskEnabled"))}">` +
      `<input type="text" class="mask-name" autocomplete="off" value="${esc(row.name || "")}" placeholder="${esc(t("integrations.maskName"))}">` +
      `<input type="text" class="mask-pattern" autocomplete="off" spellcheck="false" value="${esc(row.pattern || "")}" placeholder="*example.com/*">` +
      `<button type="button" class="mask-remove" title="${esc(t("integrations.maskRemove"))}">${TRASH_ICON}</button>`;
    el.querySelector(".mask-remove").onclick = () => {
      const current = collectMaskRows();
      current.splice(index, 1);
      renderMaskRows(current);
    };
    root.appendChild(el);
  });
  updateBrowserDirty();
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

// browserConfigSnapshot фиксирует состояние формы: «Сохранить» активна только
// тогда, когда что-то действительно изменилось.
function browserConfigSnapshot() {
  return JSON.stringify({
    enabled: $("#browserEnabled").checked,
    masks: serializeMaskRows(collectMaskRows()),
    mode: $("#browserMode").value,
    title: $("#browserTitleTemplate").value,
    missed: $("#browserMissedPolls").value,
    poll: $("#browserPoll").value,
  });
}

let browserCleanSnapshot = "";

function updateBrowserDirty() {
  const button = $("#saveBrowser");
  if (button) button.disabled = browserConfigSnapshot() === browserCleanSnapshot;
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
  $("#browserLinkSummary").textContent = t("integrations.linkSummary", {
    polls: cfg.stop_after_missed_polls ?? 4,
    interval: cfg.poll_interval_seconds ?? 20,
  });
  browserCleanSnapshot = browserConfigSnapshot();
  updateBrowserDirty();
  updatePluginCopyState();
  const tokenBox = $("#browserTokenBox");
  if (cfg.plugins_token) {
    tokenBox.innerHTML = `${KEY_ICON}<span>${esc(t("integrations.pluginsToken"))}: …${esc(cfg.plugins_token.fingerprint || "")}</span>`;
  } else {
    tokenBox.innerHTML = `${KEY_ICON}<span>${esc(t("integrations.noPluginsToken"))}</span>`;
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
  // Аудит выводим как есть, строками лога: время, токен, вызов, статус и то,
  // что известно только нам — событие, идентификатор команды, результат и
  // адрес страницы, по которой плагин создал встречу.
  const EVENT_TEXT = {
    meeting_started: "integrations.auditMeetingStarted",
    command: "integrations.auditCommand",
  };
  const lines = entries.map((entry) => {
    const time = entry.time ? new Date(entry.time).toLocaleTimeString(LOCALE, { hour12: false }) : "";
    const eventText = EVENT_TEXT[entry.event] ? t(EVENT_TEXT[entry.event]) : (entry.event || "");
    const details = [
      eventText,
      entry.url || "",
      entry.command_id ? `id ${entry.command_id}` : "",
      entry.result || "",
    ].filter(Boolean).join(" · ");
    return `<div class="audit-line"><span class="audit-time">${esc(time)}</span><span class="audit-token">${esc(entry.token || "")}</span><span class="audit-call">${esc(`${entry.method || ""} ${entry.path || ""}`.trim())}</span><span class="audit-details">${esc(details)}</span><span class="audit-status">${esc(String(entry.status ?? ""))}</span></div>`;
  }).join("");
  root.innerHTML = `<div class="audit-log">${lines}</div>`;
}


$("#integrationsBtn").onclick = openIntegrations;
$("#closeIntegrations").onclick = () => closeDialog("#integrationsDialog");
$$("#integrationsDialog [data-igtab]").forEach((button) => { button.onclick = () => switchIntegrationsTab(button.dataset.igtab); });
$("#tokenForm").onsubmit = submitTokenForm;
// Форма создания не занимает место постоянно: её открывает кнопка.
const setTokenFormOpen = (open) => {
  $("#tokenForm").classList.toggle("hidden", !open);
  $("#openTokenForm").classList.toggle("hidden", open);
  if (open) $("#tokenName").focus();
  else { $("#tokenName").value = ""; $("#tokenExpires").value = ""; }
};
$("#openTokenForm").onclick = () => setTokenFormOpen(true);
$("#cancelTokenForm").onclick = () => setTokenFormOpen(false);
$("#saveBrowser").onclick = saveBrowserConfig;
$("#regeneratePluginToken").onclick = async () => {
  if (!pluginsTokenID) { toast(t("integrations.noPluginsToken")); return; }
  try {
    const result = await api(`/api/v1/integrations/tokens/${encodeURIComponent(pluginsTokenID)}/regenerate`, { method: "POST" });
    const secret = result.secret || "";
    if (result.token?.id) state.tokenSecrets.set(result.token.id, secret);
    showTokenSecret("#browserTokenSecret", secret, t("integrations.pluginsSecretOnce"));
    updatePluginCopyState();
    await loadBrowserConfig();
    await loadTokens();
    toast(t("integrations.tokenRegenerated"));
  } catch (error) { toast(error.message); }
};
$("#copyPluginToken").onclick = async () => {
  const secret = pluginsTokenID ? state.tokenSecrets.get(pluginsTokenID) : "";
  if (!secret) { toast(t("integrations.tokenShownOnce")); return; }
  await copySecretText(secret);
};
$("#ig-browser").addEventListener("input", updateBrowserDirty);
$("#ig-browser").addEventListener("change", updateBrowserDirty);
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
      state.appVersion = config.version || "";
      state.serverTheme = config.theme || "dark";
    } catch {}
    await loadLanguage(language);
    // config.toml stays the source of truth for the theme; the cached value only
    // avoids flashing the wrong colours on the first paint.
    applyTheme(state.serverTheme);
    setCalendarView(localStorage.getItem("localmeetassist-calendar-view") || "month");
    // After the language is loaded, so the tooltip is translated.
    applyAppVersion();
    try {
      await Promise.all([loadMeetings(), refreshDevices(), refreshRecordingState(), executeMeetingSearch()]);
    } catch (error) {
      if (!isForbidden(error)) throw error;
    } finally {
      // Live updates are independent of the first data load: without this the
      // interface would silently stop refreshing until the page is reloaded.
      connectEvents();
    }
  } catch (error) {
    toast(error.message);
    const bar = $("#diagAudio");
    bar.className = "diag-status error";
    bar.textContent = t("app.bootstrapError", { message: error.message });
    const badge = $("#diagBadge");
    if (badge) badge.classList.remove("hidden");
  }
}

bootstrap();
