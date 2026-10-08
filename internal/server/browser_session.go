package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

// protocolVersion is the wire version this server speaks. It is the first
// published revision of the integration protocol, so it starts at 1; older or
// missing values are rejected instead of being run under unknown rules.
const protocolVersion = 1

// Browser scenario values published in `state`. `state` describes the browser
// story only; whether a recording actually runs is reported separately by
// `recording_active`.
const (
	scenarioIdle      = "idle"
	scenarioRecording = "recording"
	scenarioConfirm   = "awaiting_confirmation"
)

// Command results published in `ack`.
const (
	commandApplied  = "applied"
	commandNoop     = "noop"
	commandRejected = "rejected"
)

// Service URLs carry manual start/stop commands. They are matched literally,
// before GLOB matching, and never counted as meeting pages.
const (
	serviceRecordURL = "https://localmeetassist.local/record_manual"
	serviceStopURL   = "https://localmeetassist.local/stop_manual"

	commandRecord = "record_manual"
	commandStop   = "stop_manual"

	maxCommandIDLength = 256
)

// browserCapabilities are announced in `config`. A client that misses any of
// them disables its controls and asks for a server update.
var browserCapabilities = []string{"manual_tabs", "recording_state"}

// browserSession is the server-side state machine for the browser integration.
// It owns the browser scenario only: the global recording flag and its revision
// live in recordingTracker, so an application button and a browser command are
// published through exactly the same mechanism.
type browserSession struct {
	mu           sync.Mutex
	scenario     string // idle | recording | awaiting_confirmation
	owner        string // client_id that owns the scenario
	browserOwned bool   // the running recording was started by this scenario
	detached     bool   // the owner's channel dropped; the reconnect grace runs
	detachAt     time.Time
	promptID     string
	pendingURL   string
	meetingUID   string
	clients      map[string]*browserClient
	lastAskAt    time.Time
	once         sync.Once
}

// browserClient is per-installation state. nonEmpty is the last known emptiness
// of the client's matched set; nil means the server has no prior knowledge.
// tokenID lets a token revocation drop the connections it authorized.
type browserClient struct {
	clientID string
	tokenID  string
	conn     *wsConn
	nonEmpty *bool
	lastSeen time.Time
	// audit carries who authorized the connection. The plugin authenticates
	// inside the hello frame, so the HTTP middleware never sees its token and
	// cannot write audit lines: they are written here instead.
	audit browserAuditInfo
	// lastState is the last `state` payload written to this connection, so a
	// scenario change and the recording change behind it are published once.
	lastState string
}

// browserAuditInfo describes the client behind a plugin connection.
type browserAuditInfo struct {
	TokenName string
	TokenKind string
	IP        string
	UserAgent string
}

func newBrowserSession() *browserSession {
	return &browserSession{scenario: scenarioIdle, clients: make(map[string]*browserClient)}
}

// browserWSMessage is the union of all client→server message shapes.
// ProtocolVersion is a pointer so a missing field is distinguishable from zero.
type browserWSMessage struct {
	Type            string   `json:"type"`
	Token           string   `json:"token"`
	ClientID        string   `json:"client_id"`
	ProtocolVersion *int     `json:"protocol_version"`
	URLs            []string `json:"urls"`
	Seq             int64    `json:"seq"`
	CommandID       string   `json:"command_id"`
	PromptID        string   `json:"prompt_id"`
}

// serveBrowserWS runs the handshake and read loop for one connection.
func (s *Server) serveBrowserWS(conn *wsConn, ip, userAgent string) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := conn.ReadText()
	if err != nil {
		return
	}
	var hello browserWSMessage
	if json.Unmarshal(raw, &hello) != nil || hello.Type != "hello" || hello.Token == "" || hello.ClientID == "" {
		_ = s.sendWSError(conn, "bad hello")
		_ = conn.CloseWithCode(closeProtocolError, "bad hello")
		return
	}
	if hello.ProtocolVersion == nil || *hello.ProtocolVersion < protocolVersion {
		_ = s.sendWSError(conn, "unsupported_protocol")
		_ = conn.CloseWithCode(closeProtocolError, "unsupported_protocol")
		return
	}
	token, err := s.store.FindTokenByHash(hashToken(hello.Token))
	if err != nil || token.Kind != tokenKindPlugins || (token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt)) {
		_ = s.sendWSError(conn, "forbidden")
		_ = conn.CloseWithCode(closePolicyViolation, "forbidden")
		return
	}
	_ = s.store.TouchTokenLastUsed(token.ID, time.Now())

	clientID := hello.ClientID
	info := browserAuditInfo{TokenName: token.Name, TokenKind: token.Kind, IP: ip, UserAgent: userAgent}
	s.browserAttach(clientID, token.ID, info, conn)
	_ = conn.SetReadDeadline(time.Time{})

	for {
		raw, err := conn.ReadText()
		if err != nil {
			break
		}
		var msg browserWSMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		s.browserHandleMessage(clientID, &msg)
	}
	s.browserDetach(clientID, conn)
}

// auditBrowser writes one plugin event to the same JSONL file as the HTTP audit
// entries. Without it the audit stays empty for the usual setup, because the
// plugin never makes an HTTP request that carries its token.
func (s *Server) auditBrowser(event string, info browserAuditInfo, clientID string, extra map[string]any) {
	if !s.config().Integrations.AuditEnabled {
		return
	}
	entry := map[string]any{
		"time":       time.Now().UTC().Format(time.RFC3339Nano),
		"token":      info.TokenName,
		"kind":       info.TokenKind,
		"ip":         info.IP,
		"user_agent": info.UserAgent,
		"method":     "WS",
		"path":       "/api/v1/ws",
		"status":     101,
		"event":      event,
		"client_id":  clientID,
	}
	for key, value := range extra {
		entry[key] = value
	}
	s.appendAudit(entry)
}

// browserAuditInfoFor returns the audit identity of a connected client.
func (s *Server) browserAuditInfoFor(clientID string) browserAuditInfo {
	bs := s.browser
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if client := bs.clients[clientID]; client != nil {
		return client.audit
	}
	return browserAuditInfo{}
}

// browserAttach registers the connection, atomically replacing any older one for
// the same client_id. Reconnecting the owner resumes the session.
func (s *Server) browserAttach(clientID, tokenID string, info browserAuditInfo, conn *wsConn) {
	bs := s.browser
	bs.once.Do(func() { go s.browserLoop() })
	bs.mu.Lock()
	client := bs.ensureClientLocked(clientID)
	if client.conn != nil && client.conn != conn {
		client.conn.Close() // replace old; its detach becomes a no-op
	}
	client.conn = conn
	client.tokenID = tokenID
	client.audit = info
	client.lastSeen = time.Now()
	if bs.owner == clientID && bs.detached {
		// The owner came back inside the reconnect grace: the session survives
		// and nothing is stopped.
		bs.detached = false
		bs.detachAt = time.Time{}
	}
	owner := bs.owner == clientID
	bs.mu.Unlock()

	// conn может быть nil в тестах: подключение к WebSocket там не поднимается,
	// а состояние клиента проверить нужно.
	if conn != nil {
		s.sendBrowserConfig(conn)
		s.sendBrowserState(clientID, conn, owner)
	}
}

// browserDetach drops a connection that has gone away. Losing the owner starts
// the reconnect grace; the recording itself keeps running until it expires.
func (s *Server) browserDetach(clientID string, conn *wsConn) {
	bs := s.browser
	bs.mu.Lock()
	client := bs.clients[clientID]
	if client == nil || client.conn != conn {
		bs.mu.Unlock()
		return // already replaced by a newer connection
	}
	client.conn = nil
	if bs.owner == clientID && !bs.detached {
		bs.detached = true
		bs.detachAt = time.Now().Add(s.reconnectGrace())
	}
	bs.mu.Unlock()
	s.broadcastBrowserState()
}

func (s *Server) browserHandleMessage(clientID string, msg *browserWSMessage) {
	switch msg.Type {
	case "tabs":
		s.handleBrowserTabs(clientID, msg.URLs, msg.Seq, msg.CommandID)
	case "confirm":
		s.handleBrowserConfirm(clientID, msg.PromptID)
	case "decline":
		s.handleBrowserDecline(clientID, msg.PromptID)
	}
}

// handleBrowserTabs dispatches a snapshot: service markers become commands, and
// everything else is an ordinary emptiness-transition snapshot.
func (s *Server) handleBrowserTabs(clientID string, urls []string, seq int64, commandID string) {
	if !s.config().Integrations.Browser.Enabled {
		// The integration is switched off: snapshots are ignored, commands are
		// rejected, and nothing is started or stopped.
		if _, markers := serviceMarker(urls); markers > 0 {
			s.sendBrowserAckWithResult(clientID, seq, commandID, commandRejected)
			return
		}
		s.sendBrowserAck(clientID, seq)
		return
	}

	marker, markers := serviceMarker(urls)
	if markers > 0 {
		s.handleBrowserCommand(clientID, seq, commandID, marker, markers)
		return
	}
	s.handleBrowserSnapshot(clientID, urls, seq)
}

// serviceMarker reports which service URL the snapshot carries and how many.
// Only the exact strings count; they never reach GLOB matching.
func serviceMarker(urls []string) (string, int) {
	marker := ""
	count := 0
	for _, u := range urls {
		switch u {
		case serviceRecordURL:
			if marker == "" {
				marker = commandRecord
			}
			count++
		case serviceStopURL:
			if marker == "" {
				marker = commandStop
			}
			count++
		}
	}
	return marker, count
}

// handleBrowserCommand runs a manual start/stop exactly once per command_id.
// The normal URL logic is deliberately skipped: a stop carrying the source URL
// must not immediately open a new meeting.
func (s *Server) handleBrowserCommand(clientID string, seq int64, commandID, marker string, markers int) {
	if markers != 1 || commandID == "" || len(commandID) > maxCommandIDLength {
		s.sendBrowserAckWithResult(clientID, seq, commandID, commandRejected)
		return
	}
	now := time.Now()
	result, cached, err := s.store.CommandResult(clientID, commandID, now, commandTTL)
	if err != nil || !cached {
		result = s.executeBrowserCommand(marker)
		if saveErr := s.store.SaveCommandResult(clientID, commandID, result, time.Now(), commandTTL); saveErr != nil {
			s.logger.Printf("integration command cache: %v", saveErr)
		}
	}
	s.sendBrowserAckWithResult(clientID, seq, commandID, result)
	s.auditBrowser("command", s.browserAuditInfoFor(clientID), clientID, map[string]any{
		"command_id": commandID,
		"action":     marker,
		"result":     result,
		"cached":     cached,
	})
}

// executeBrowserCommand performs the operation and reports what happened. The
// recording transition itself is published by syncRecording before the ack.
func (s *Server) executeBrowserCommand(marker string) string {
	switch marker {
	case commandRecord:
		meeting, err := s.activeRecording()
		if err != nil {
			return commandRejected
		}
		if meeting != nil {
			return commandNoop
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// record_manual is the application button sent through the plugin, so it
		// creates an application-owned recording: no tab binding, and losing the
		// plugin does not stop it.
		if _, err := s.startMeetingRecording(ctx, model.RecordingStart{}, "recorded", map[string]string{"command": commandRecord}); err != nil {
			appLogging.Errorf(s.logger, "record_manual failed error=%v", err)
			s.syncRecording()
			return commandRejected
		}
		return commandApplied
	case commandStop:
		meeting, err := s.activeRecording()
		if err != nil {
			return commandRejected
		}
		if meeting == nil {
			return commandNoop
		}
		// stop_manual is the application button: it ends any current recording,
		// whoever started it.
		if _, err := s.stopMeetingRecording(meeting.UID); err != nil {
			appLogging.Errorf(s.logger, "stop_manual failed error=%v", err)
			return commandRejected
		}
		return commandApplied
	}
	return commandRejected
}

// handleBrowserSnapshot applies the emptiness rules. An empty set never stops a
// recording: the plugin owns that delay and sends stop_manual itself.
func (s *Server) handleBrowserSnapshot(clientID string, urls []string, seq int64) {
	cfg := s.config()
	matched := s.matchBrowserURLs(urls)
	nonEmpty := len(matched) > 0
	active, _ := s.recordingValues()

	bs := s.browser
	bs.mu.Lock()
	client := bs.ensureClientLocked(clientID)
	client.lastSeen = time.Now()
	prev := client.nonEmpty
	client.nonEmpty = &nonEmpty

	wasEmpty := prev == nil || !*prev
	emptyToNonEmpty := nonEmpty && wasEmpty
	nonEmptyToEmpty := !nonEmpty && prev != nil && *prev

	stateChanged := false
	var startURL string
	isOwner := bs.owner == clientID

	if isOwner {
		if bs.scenario == scenarioConfirm && nonEmptyToEmpty {
			// An empty snapshot cancels the owner's pending question.
			bs.releaseLocked()
			stateChanged = true
		}
		// While a recording runs, ordinary snapshots neither create a second
		// meeting nor stop the current one.
	} else if bs.scenario == scenarioIdle && emptyToNonEmpty && !active {
		bs.owner = clientID
		bs.pendingURL = matched[0]
		if cfg.Integrations.Browser.Mode == "notify" {
			bs.scenario = scenarioConfirm
			bs.promptID = uuidv7.New()
		} else {
			bs.scenario = scenarioRecording
			bs.browserOwned = true
			startURL = matched[0]
		}
		stateChanged = true
	}
	bs.mu.Unlock()

	if startURL != "" {
		uid, err := s.startBrowserRecordingFor(clientID, startURL)
		bs.mu.Lock()
		if err == nil && bs.scenario == scenarioRecording && bs.owner == clientID {
			bs.meetingUID = uid
		} else if err != nil {
			bs.releaseLocked()
			stateChanged = true
		}
		bs.mu.Unlock()
	}

	if stateChanged {
		s.broadcastBrowserState()
	}
	s.sendBrowserAck(clientID, seq)
}

func (s *Server) handleBrowserConfirm(clientID, promptID string) {
	active, _ := s.recordingValues()

	bs := s.browser
	bs.mu.Lock()
	client := bs.ensureClientLocked(clientID)
	client.lastSeen = time.Now()
	stateChanged := false
	var startURL string
	if bs.owner == clientID && bs.scenario == scenarioConfirm && bs.promptID == promptID {
		if active {
			// A recording appeared while the question was open: no second meeting,
			// the question just ends.
			bs.releaseLocked()
		} else {
			bs.scenario = scenarioRecording
			bs.browserOwned = true
			startURL = bs.pendingURL
		}
		stateChanged = true
	}
	bs.mu.Unlock()

	if startURL != "" {
		uid, err := s.startBrowserRecordingFor(clientID, startURL)
		bs.mu.Lock()
		if err == nil && bs.scenario == scenarioRecording && bs.owner == clientID {
			bs.meetingUID = uid
		} else if err != nil {
			bs.releaseLocked()
			stateChanged = true
		}
		bs.mu.Unlock()
	}
	if stateChanged {
		s.broadcastBrowserState()
	}
}

// handleBrowserDecline closes the question. It does not block the GLOB: a new
// page may raise a new question after the usual detection delay.
func (s *Server) handleBrowserDecline(clientID, promptID string) {
	bs := s.browser
	bs.mu.Lock()
	client := bs.ensureClientLocked(clientID)
	client.lastSeen = time.Now()
	changed := false
	if bs.owner == clientID && bs.scenario == scenarioConfirm && bs.promptID == promptID {
		bs.releaseLocked()
		changed = true
	}
	bs.mu.Unlock()
	if changed {
		s.broadcastBrowserState()
	}
}

// browserOnRecordingState reacts to a real global recording transition. When the
// recording ends — by an application button, by stop_manual, by a failure or by
// the reconnect grace — the browser scenario is released so it never stays in
// `recording` while `recording_active` is false.
func (s *Server) browserOnRecordingState(active bool) {
	bs := s.browser
	bs.mu.Lock()
	release := false
	if !active {
		release = bs.scenario != scenarioIdle || bs.owner != ""
	} else if bs.scenario == scenarioConfirm {
		release = true
	}
	if release {
		bs.releaseLocked()
	}
	bs.mu.Unlock()
	s.broadcastBrowserState()
}

// browserLoop enforces the reconnect grace, liveness and the periodic ask poll.
func (s *Server) browserLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		// Publish transitions that did not go through the API, for example a
		// recording that ended on its own.
		s.syncRecording()

		cfg := s.config()
		poll := cfg.Integrations.Browser.PollIntervalSeconds
		if poll < 1 {
			poll = 20
		}
		now := time.Now()
		liveness := 2 * time.Duration(poll) * time.Second
		askInterval := time.Duration(poll) * time.Second

		bs := s.browser
		bs.mu.Lock()

		var dead []*wsConn
		for _, c := range bs.clients {
			if c.conn != nil && now.Sub(c.lastSeen) > liveness {
				dead = append(dead, c.conn)
			}
		}

		askDue := bs.lastAskAt.IsZero() || now.Sub(bs.lastAskAt) >= askInterval
		if askDue {
			bs.lastAskAt = now
		}
		type askTarget struct {
			conn  *wsConn
			owner bool
		}
		var asks []askTarget
		if askDue {
			for id, c := range bs.clients {
				if c.conn != nil {
					asks = append(asks, askTarget{conn: c.conn, owner: id == bs.owner})
				}
			}
		}
		bs.mu.Unlock()

		// Closing a socket only starts the grace: it never stops a recording by
		// itself.
		for _, c := range dead {
			_ = c.Close()
		}
		if askDue {
			active, revision := s.recordingValues()
			for _, t := range asks {
				raw, _ := json.Marshal(map[string]any{
					"type":               "ask",
					"what":               "tabs",
					"recording_active":   active,
					"recording_revision": revision,
					"owner":              t.owner,
				})
				_ = t.conn.WriteText(raw)
			}
		}
		s.expireReconnectGrace(now)
	}
}

// expireReconnectGrace releases the owner and stops a browser-owned recording
// once the reconnect grace has run out. An application recording is never
// stopped this way — only the browser scenario that started it is released.
func (s *Server) expireReconnectGrace(now time.Time) {
	bs := s.browser
	bs.mu.Lock()
	if !bs.detached || bs.detachAt.IsZero() || !now.After(bs.detachAt) {
		bs.mu.Unlock()
		return
	}
	var stopUID string
	if bs.scenario == scenarioRecording && bs.browserOwned {
		stopUID = bs.meetingUID
	}
	released := bs.scenario != scenarioIdle || bs.owner != ""
	bs.releaseLocked()
	bs.mu.Unlock()

	if stopUID != "" {
		if _, err := s.stopMeetingRecording(stopUID); err != nil {
			s.logger.Printf("browser reconnect grace: %v", err)
		}
	}
	if released {
		s.broadcastBrowserState()
	}
}

// releaseLocked returns the browser scenario to idle. The clients' known sets
// are deliberately preserved: a late snapshot must stay a repeat instead of
// looking like a fresh empty→non-empty transition.
func (bs *browserSession) releaseLocked() {
	bs.scenario = scenarioIdle
	bs.detached = false
	bs.owner = ""
	bs.browserOwned = false
	bs.detachAt = time.Time{}
	bs.promptID = ""
	bs.pendingURL = ""
	bs.meetingUID = ""
}

func (bs *browserSession) ensureClientLocked(clientID string) *browserClient {
	c := bs.clients[clientID]
	if c == nil {
		c = &browserClient{clientID: clientID}
		bs.clients[clientID] = c
	}
	return c
}

func (s *Server) browserConn(clientID string) *wsConn {
	bs := s.browser
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if c := bs.clients[clientID]; c != nil {
		return c.conn
	}
	return nil
}

// reconnectGrace is how long the server waits for the owner before stopping a
// browser-owned recording. It is counted in missed polls, so it scales with the
// configured rhythm: N × poll_interval_seconds.
func (s *Server) reconnectGrace() time.Duration {
	b := s.config().Integrations.Browser
	poll := b.PollIntervalSeconds
	if poll < 1 {
		poll = 20
	}
	missed := b.StopAfterMissedPolls
	if missed < 1 {
		missed = 4
	}
	return time.Duration(missed*poll) * time.Second
}

func (s *Server) matchBrowserURLs(urls []string) []string {
	masks := s.config().Integrations.Browser.Masks
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if matchBrowserMask(masks, u) != nil {
			out = append(out, u)
		}
	}
	return out
}

// startBrowserRecordingFor starts a browser-owned meeting for the given URL and
// returns its UID.
func (s *Server) startBrowserRecordingFor(clientID, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	title := url
	if mask := matchBrowserMask(s.config().Integrations.Browser.Masks, url); mask != nil {
		title = s.buildBrowserTitle(mask.Name, url)
	}
	m, err := s.startMeetingRecording(ctx, model.RecordingStart{Title: title}, "browser", map[string]string{"browser_url": url})
	if err != nil {
		return "", err
	}
	// Ради этого события аудит и ведётся: встреча создана по странице, которую
	// нашло расширение. Подключения и отключения не пишем — расширение
	// переподключается часто, и журнал превращался в мусор.
	s.auditBrowser("meeting_started", s.browserAuditInfoFor(clientID), clientID, map[string]any{
		"url":         url,
		"meeting_uid": m.UID,
	})
	return m.UID, nil
}

// browserOnConfigChanged is called after the browser settings are saved.
// Switching the integration off releases the browser scenario but deliberately
// does not stop a running recording, and resetting the known sets lets a
// re-enabled integration start from an already open page.
func (s *Server) browserOnConfigChanged() {
	cfg := s.config()
	if !cfg.Integrations.Browser.Enabled {
		bs := s.browser
		bs.mu.Lock()
		bs.releaseLocked()
		for _, c := range bs.clients {
			c.nonEmpty = nil
		}
		bs.mu.Unlock()
		s.broadcastBrowserState()
	}
	s.broadcastBrowserConfig()
}

// closeBrowserClientsForToken drops every connection authorized with the given
// token. Regenerating or revoking a token must not leave live sessions behind.
func (s *Server) closeBrowserClientsForToken(tokenID string) {
	bs := s.browser
	bs.mu.Lock()
	var conns []*wsConn
	for _, c := range bs.clients {
		if c.tokenID == tokenID && c.conn != nil {
			conns = append(conns, c.conn)
		}
	}
	bs.mu.Unlock()
	for _, conn := range conns {
		_ = conn.CloseWithCode(closePolicyViolation, "token revoked")
	}
}

// --- outbound messages ------------------------------------------------------

func (s *Server) sendBrowserConfig(conn *wsConn) {
	cfg := s.config()
	b := cfg.Integrations.Browser
	patterns := make([]string, 0)
	for _, mask := range parseBrowserMasks(b.Masks) {
		patterns = append(patterns, mask.Pattern)
	}
	msg := map[string]any{
		"type":                  "config",
		"protocol_version":      protocolVersion,
		"capabilities":          browserCapabilities,
		"enabled":               b.Enabled,
		"patterns":              patterns,
		"mode":                  b.Mode,
		"title_template":        b.TitleTemplate,
		"poll_interval_seconds": b.PollIntervalSeconds,
		// listen_port is the pinned setting and effective_port the running
		// listener; they differ until the application is restarted.
		"listen_port":    s.configuredListenPort(),
		"effective_port": s.effectivePort,
	}
	raw, _ := json.Marshal(msg)
	_ = conn.WriteText(raw)
}

// marshalBrowserState builds the `state` payload. `state` describes the browser
// story only; `recording_active` carries the actual global recording.
func marshalBrowserState(scenario, promptID string, owner, active bool, revision int64) []byte {
	msg := map[string]any{
		"type":               "state",
		"state":              scenario,
		"owner":              owner,
		"recording_active":   active,
		"recording_revision": revision,
	}
	if scenario == scenarioConfirm && promptID != "" {
		msg["prompt_id"] = promptID
	}
	raw, _ := json.Marshal(msg)
	return raw
}

func (s *Server) sendBrowserState(clientID string, conn *wsConn, owner bool) {
	bs := s.browser
	bs.mu.Lock()
	scenario, promptID := bs.scenario, bs.promptID
	active, revision := s.recordingValues()
	raw := marshalBrowserState(scenario, promptID, owner, active, revision)
	if c := bs.clients[clientID]; c != nil {
		c.lastState = string(raw)
	}
	bs.mu.Unlock()
	_ = conn.WriteText(raw)
}

func (s *Server) sendBrowserAck(clientID string, seq int64) {
	conn := s.browserConn(clientID)
	if conn == nil {
		return
	}
	raw, _ := json.Marshal(map[string]any{"type": "ack", "seq": seq})
	_ = conn.WriteText(raw)
}

// sendBrowserAckWithResult answers a command. A normal ack without command_id
// never completes a command, so the result is only reported here.
func (s *Server) sendBrowserAckWithResult(clientID string, seq int64, commandID, result string) {
	conn := s.browserConn(clientID)
	if conn == nil {
		return
	}
	msg := map[string]any{"type": "ack", "seq": seq}
	if commandID != "" {
		msg["command_id"] = commandID
		msg["command_result"] = result
	}
	raw, _ := json.Marshal(msg)
	_ = conn.WriteText(raw)
}

func (s *Server) sendWSError(conn *wsConn, msg string) error {
	raw, _ := json.Marshal(map[string]any{"type": "error", "error": msg})
	return conn.WriteText(raw)
}

func (s *Server) broadcastBrowserState() {
	bs := s.browser
	bs.mu.Lock()
	scenario, promptID, owner := bs.scenario, bs.promptID, bs.owner
	active, revision := s.recordingValues()
	type target struct {
		conn *wsConn
		raw  []byte
	}
	var targets []target
	for id, c := range bs.clients {
		if c.conn == nil {
			continue
		}
		raw := marshalBrowserState(scenario, promptID, id == owner, active, revision)
		// A scenario change and the recording transition behind it would
		// otherwise be published twice in a row.
		if string(raw) == c.lastState {
			continue
		}
		c.lastState = string(raw)
		targets = append(targets, target{conn: c.conn, raw: raw})
	}
	bs.mu.Unlock()

	for _, t := range targets {
		_ = t.conn.WriteText(t.raw)
	}
}

func (s *Server) broadcastBrowserConfig() {
	bs := s.browser
	bs.mu.Lock()
	var conns []*wsConn
	for _, c := range bs.clients {
		if c.conn != nil {
			conns = append(conns, c.conn)
		}
	}
	bs.mu.Unlock()
	for _, conn := range conns {
		s.sendBrowserConfig(conn)
	}
}
