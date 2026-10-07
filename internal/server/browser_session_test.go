package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/recording"
	"localmeetassist/internal/store"
)

// --- helpers -----------------------------------------------------------------

func browserScenario(s *Server) string {
	s.browser.mu.Lock()
	defer s.browser.mu.Unlock()
	return s.browser.scenario
}

func browserOwner(s *Server) string {
	s.browser.mu.Lock()
	defer s.browser.mu.Unlock()
	return s.browser.owner
}

func setDetachedExpired(s *Server) {
	s.browser.mu.Lock()
	s.browser.detached = true
	s.browser.detachAt = time.Now().Add(-time.Second)
	s.browser.mu.Unlock()
}

// --- browser session state machine -------------------------------------------

func TestBrowserSessionAutoFlow(t *testing.T) {
	s := newBrowserServer(t, "auto")

	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	if browserScenario(s) != scenarioRecording || browserOwner(s) != "c1" {
		t.Fatalf("expected recording owned by c1, got state=%s owner=%s", browserScenario(s), browserOwner(s))
	}
	active, revision := s.recordingValues()
	if !active {
		t.Fatal("a browser start must publish recording_active")
	}
	if revision == 0 {
		t.Fatal("a real transition must increase recording_revision")
	}

	// An empty set must NOT stop the recording: the plugin owns that delay and
	// sends stop_manual itself.
	s.handleBrowserTabs("c1", []string{}, 2, "")
	if browserScenario(s) != scenarioRecording {
		t.Fatalf("empty set must keep the recording, got %s", browserScenario(s))
	}
	if active, _ := s.recordingValues(); !active {
		t.Fatal("empty set stopped the recording")
	}

	// A content change keeps the same meeting.
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1", "https://zoom.us/wc/2"}, 3, "")
	if browserScenario(s) != scenarioRecording {
		t.Fatalf("content change must not affect the recording, got %s", browserScenario(s))
	}
}

func TestBrowserSessionReleasesWhenRecordingEndsExternally(t *testing.T) {
	s := newBrowserServer(t, "auto")
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")

	s.browser.mu.Lock()
	uid := s.browser.meetingUID
	s.browser.mu.Unlock()
	if uid == "" {
		t.Fatal("no meeting was created")
	}

	// The application button ends the recording.
	if _, err := s.stopMeetingRecording(uid); err != nil {
		t.Fatal(err)
	}
	if browserScenario(s) != scenarioIdle || browserOwner(s) != "" {
		t.Fatalf("external stop must release the browser session, got %s owner=%s", browserScenario(s), browserOwner(s))
	}
	if active, _ := s.recordingValues(); active {
		t.Fatal("recording_active must be false after the stop")
	}
}

func TestBrowserSessionNotifyFlow(t *testing.T) {
	s := newBrowserServer(t, "notify")

	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	if browserScenario(s) != scenarioConfirm || browserOwner(s) != "c1" {
		t.Fatalf("expected a pending question owned by c1, got %s owner=%s", browserScenario(s), browserOwner(s))
	}
	s.browser.mu.Lock()
	prompt := s.browser.promptID
	s.browser.mu.Unlock()
	if prompt == "" {
		t.Fatal("awaiting_confirmation must carry a prompt_id")
	}

	// A decline closes the question and releases the owner; it does not block
	// the GLOB with a temporary state.
	s.handleBrowserDecline("c1", prompt)
	if browserScenario(s) != scenarioIdle || browserOwner(s) != "" {
		t.Fatalf("decline must close the question, got %s owner=%s", browserScenario(s), browserOwner(s))
	}
	if active, _ := s.recordingValues(); active {
		t.Fatal("a declined question must not start a recording")
	}

	// The plugin then sends an empty set, and a new page may ask again.
	s.handleBrowserTabs("c1", []string{}, 2, "")
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/2"}, 3, "")
	if browserScenario(s) != scenarioConfirm {
		t.Fatalf("a new page after decline must raise a new question, got %s", browserScenario(s))
	}
}

func TestBrowserSessionConfirm(t *testing.T) {
	s := newBrowserServer(t, "notify")
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	s.browser.mu.Lock()
	prompt := s.browser.promptID
	s.browser.mu.Unlock()

	s.handleBrowserConfirm("c1", prompt)
	if browserScenario(s) != scenarioRecording {
		t.Fatalf("expected recording after confirm, got %s", browserScenario(s))
	}
	if active, _ := s.recordingValues(); !active {
		t.Fatal("confirm must start a recording")
	}
}

func TestBrowserSessionManualRecordingIgnored(t *testing.T) {
	s := newBrowserServer(t, "auto")
	if _, err := s.startMeetingRecording(context.Background(), model.RecordingStart{Title: "manual"}, "recorded", nil); err != nil {
		t.Fatal(err)
	}
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	if browserScenario(s) != scenarioIdle || browserOwner(s) != "" {
		t.Fatalf("an application recording must keep the browser scenario idle, got %s owner=%s", browserScenario(s), browserOwner(s))
	}
}

func TestBrowserSessionConfirmWithManualRecording(t *testing.T) {
	s := newBrowserServer(t, "notify")
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	s.browser.mu.Lock()
	prompt := s.browser.promptID
	s.browser.mu.Unlock()

	if _, err := s.startMeetingRecording(context.Background(), model.RecordingStart{Title: "manual"}, "recorded", nil); err != nil {
		t.Fatal(err)
	}
	s.handleBrowserConfirm("c1", prompt)
	if browserScenario(s) != scenarioIdle || browserOwner(s) != "" {
		t.Fatalf("confirm during an application recording must release, got %s owner=%s", browserScenario(s), browserOwner(s))
	}
	if active, _ := s.recordingValues(); !active {
		t.Fatal("the application recording must survive the released question")
	}
}

// --- manual commands through service URLs ------------------------------------

func TestBrowserManualCommandRecordAndStop(t *testing.T) {
	s := newBrowserServer(t, "auto")

	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 1, "cmd-record")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("record_manual must start a recording")
	}
	// record_manual is the application button: no tab binding, and the browser
	// scenario stays idle.
	if browserScenario(s) != scenarioIdle || browserOwner(s) != "" {
		t.Fatalf("record_manual must not bind the browser scenario, got %s owner=%s", browserScenario(s), browserOwner(s))
	}

	// A second start is a no-op and keeps the existing meeting.
	meeting, err := s.activeRecording()
	if err != nil || meeting == nil {
		t.Fatal("no active recording")
	}
	first := meeting.UID
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 2, "cmd-record-again")
	meeting, err = s.activeRecording()
	if err != nil || meeting == nil {
		t.Fatal("no active recording after the repeat")
	}
	if meeting.UID != first {
		t.Fatalf("a repeated start created a second meeting: %s → %s", first, meeting.UID)
	}

	s.handleBrowserTabs("c1", []string{serviceStopURL}, 3, "cmd-stop")
	if active, _ := s.recordingValues(); active {
		t.Fatal("stop_manual must end the recording")
	}

	// Stopping again is a no-op.
	s.handleBrowserTabs("c1", []string{serviceStopURL}, 4, "cmd-stop-again")
	if active, _ := s.recordingValues(); active {
		t.Fatal("a repeated stop must not start anything")
	}
}

func TestBrowserManualCommandValidation(t *testing.T) {
	s := newBrowserServer(t, "auto")

	// Both markers in one snapshot are rejected and do nothing.
	s.handleBrowserTabs("c1", []string{serviceRecordURL, serviceStopURL}, 1, "cmd-both")
	if active, _ := s.recordingValues(); active {
		t.Fatal("a snapshot with both markers must not act")
	}

	// A marker without a command_id is rejected.
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 2, "")
	if active, _ := s.recordingValues(); active {
		t.Fatal("a marker without command_id must not act")
	}

	// An over-long command_id is rejected.
	long := strings.Repeat("x", maxCommandIDLength+1)
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 3, long)
	if active, _ := s.recordingValues(); active {
		t.Fatal("an over-long command_id must not act")
	}
}

func TestBrowserManualCommandMarkerSuppressesURLStart(t *testing.T) {
	s := newBrowserServer(t, "auto")

	// A stop marker next to the source URL must not start a new meeting.
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("the first snapshot must start a recording")
	}
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1", serviceStopURL}, 2, "cmd-stop")
	if active, _ := s.recordingValues(); active {
		t.Fatal("stop_manual with the source URL must leave nothing recording")
	}
	// Give the URL-start path a chance to misfire before asserting.
	time.Sleep(10 * time.Millisecond)
	if active, _ := s.recordingValues(); active {
		t.Fatal("the ordinary URL start must not run in a snapshot carrying a marker")
	}
}

func TestBrowserManualCommandIdempotency(t *testing.T) {
	s := newBrowserServer(t, "auto")

	// First start, then stop, then start a new meeting.
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 1, "cmd-a")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("start failed")
	}
	s.handleBrowserTabs("c1", []string{serviceStopURL}, 2, "cmd-b")
	if active, _ := s.recordingValues(); active {
		t.Fatal("stop failed")
	}
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 3, "cmd-c")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("second start failed")
	}
	newMeeting, err := s.activeRecording()
	if err != nil || newMeeting == nil {
		t.Fatal("no active recording")
	}

	// The repeated old stop must not finish the meeting that started later.
	s.handleBrowserTabs("c1", []string{serviceStopURL}, 4, "cmd-b")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("a repeated old stop_manual ended the next meeting")
	}
	current, err := s.activeRecording()
	if err != nil || current == nil || current.UID != newMeeting.UID {
		t.Fatal("a repeated old stop_manual replaced the current meeting")
	}

	// The repeated old start must not create anything either.
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 5, "cmd-a")
	current, err = s.activeRecording()
	if err != nil || current == nil || current.UID != newMeeting.UID {
		t.Fatal("a repeated old record_manual replaced the current meeting")
	}
}

// --- recording revision ------------------------------------------------------

func TestRecordingRevisionNormalisation(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRecordingState(7, true); err != nil {
		t.Fatal(err)
	}
	s := newServerWithStore(t, st)
	active, revision := s.recordingValues()
	if active {
		t.Fatal("a recording cannot survive a restart")
	}
	if revision != 8 {
		t.Fatalf("a stored active state must be normalised to a new revision, got %d", revision)
	}
	st.Close()

	// A stored inactive state must not grow the revision again.
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	s2 := newServerWithStore(t, st2)
	if active, revision := s2.recordingValues(); active || revision != 8 {
		t.Fatalf("inactive restart changed the pair: active=%t revision=%d", active, revision)
	}
}

func TestRecordingRevisionGrowsOnRealTransitions(t *testing.T) {
	s := newBrowserServer(t, "auto")
	_, before := s.recordingValues()

	if _, err := s.startMeetingRecording(context.Background(), model.RecordingStart{Title: "one"}, "recorded", nil); err != nil {
		t.Fatal(err)
	}
	active, afterStart := s.recordingValues()
	if !active || afterStart != before+1 {
		t.Fatalf("start must grow the revision once: active=%t %d → %d", active, before, afterStart)
	}

	meeting, err := s.activeRecording()
	if err != nil || meeting == nil {
		t.Fatal("no active recording")
	}
	if _, err := s.stopMeetingRecording(meeting.UID); err != nil {
		t.Fatal(err)
	}
	active, afterStop := s.recordingValues()
	if active || afterStop != afterStart+1 {
		t.Fatalf("stop must grow the revision once: active=%t %d → %d", active, afterStart, afterStop)
	}
}

// --- reconnect grace ---------------------------------------------------------

func TestReconnectGraceStopsBrowserRecordingOnly(t *testing.T) {
	s := newBrowserServer(t, "auto")
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("the browser start did not record")
	}

	setDetachedExpired(s)
	s.expireReconnectGrace(time.Now())
	if active, _ := s.recordingValues(); active {
		t.Fatal("the grace must stop a browser-owned recording")
	}
	if browserScenario(s) != scenarioIdle {
		t.Fatalf("the session must be released, got %s", browserScenario(s))
	}
}

func TestReconnectGraceKeepsApplicationRecording(t *testing.T) {
	s := newBrowserServer(t, "auto")
	// record_manual is an application recording, so the grace must not touch it.
	s.handleBrowserTabs("c1", []string{serviceRecordURL}, 1, "cmd-record")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("record_manual did not record")
	}
	setDetachedExpired(s)
	s.expireReconnectGrace(time.Now())
	if active, _ := s.recordingValues(); !active {
		t.Fatal("the grace stopped an application recording")
	}
}

// TestReconnectGraceFollowsMissedPolls pins the window to N × poll interval.
func TestReconnectGraceFollowsMissedPolls(t *testing.T) {
	s := newBrowserServer(t, "auto")

	cfg := s.config()
	cfg.Integrations.Browser.StopAfterMissedPolls = 2
	cfg.Integrations.Browser.PollIntervalSeconds = 10
	s.setConfig(cfg)
	if got, want := s.reconnectGrace(), 20*time.Second; got != want {
		t.Fatalf("reconnectGrace = %v, want %v", got, want)
	}

	cfg.Integrations.Browser.StopAfterMissedPolls = 10
	cfg.Integrations.Browser.PollIntervalSeconds = 25
	s.setConfig(cfg)
	if got, want := s.reconnectGrace(), 250*time.Second; got != want {
		t.Fatalf("reconnectGrace = %v, want %v", got, want)
	}
}

// --- WebSocket transport -----------------------------------------------------

type testWSConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialTestWS(t *testing.T, baseURL string) *testWSConn {
	t.Helper()
	host := strings.TrimPrefix(baseURL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	req := "GET /api/v1/ws HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("handshake failed: %q %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return &testWSConn{conn: conn, br: br}
}

func (c *testWSConn) writeText(t *testing.T, b []byte) {
	t.Helper()
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	frame := []byte{0x81}
	n := len(b)
	switch {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	case n <= 0xFFFF:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	default:
		frame = append(frame, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		frame = append(frame, ext[:]...)
	}
	frame = append(frame, mask[:]...)
	for i := 0; i < n; i++ {
		frame = append(frame, b[i]^mask[i%4])
	}
	if _, err := c.conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func (c *testWSConn) writeJSON(t *testing.T, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	c.writeText(t, raw)
}

func (c *testWSConn) readJSON() map[string]any {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.br, header); err != nil {
		return nil
	}
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		_, _ = io.ReadFull(c.br, ext)
		length = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		_, _ = io.ReadFull(c.br, ext)
		length = int(binary.BigEndian.Uint64(ext))
	}
	payload := make([]byte, length)
	_, _ = io.ReadFull(c.br, payload)
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	return m
}

func (c *testWSConn) close() { _ = c.conn.Close() }

// helloWithVersion returns a hello message for the given protocol version; a
// zero version omits the field entirely.
func helloWithVersion(token, clientID string, version int) map[string]any {
	msg := map[string]any{"type": "hello", "token": token, "client_id": clientID}
	if version > 0 {
		msg["protocol_version"] = version
	}
	return msg
}

func TestWebSocketHelloAndStateFlow(t *testing.T) {
	s := newBrowserServer(t, "auto")
	_, secret, err := s.ensurePluginsToken()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c := dialTestWS(t, ts.URL)
	defer c.close()
	c.writeJSON(t, helloWithVersion(secret, "test-client", protocolVersion))

	first := c.readJSON()
	if first["type"] != "config" {
		t.Fatalf("expected config first, got %v", first["type"])
	}
	if first["protocol_version"] != float64(protocolVersion) {
		t.Fatalf("config must announce the protocol version, got %v", first["protocol_version"])
	}
	caps, ok := first["capabilities"].([]any)
	if !ok || len(caps) != 2 {
		t.Fatalf("config must announce both capabilities, got %v", first["capabilities"])
	}
	// The delay after leaving a page belongs to the plugin and is configured
	// there; the server must not push it back into the contract.
	if _, present := first["stop_delay_seconds"]; present {
		t.Fatalf("config must not carry stop_delay_seconds, got %v", first)
	}
	second := c.readJSON()
	if second["type"] != "state" || second["state"] != "idle" {
		t.Fatalf("expected idle state, got %v", second)
	}
	if _, present := second["recording_active"]; !present {
		t.Fatalf("state must carry recording_active, got %v", second)
	}
	if _, present := second["recording_revision"]; !present {
		t.Fatalf("state must carry recording_revision, got %v", second)
	}

	// A non-empty set in auto mode publishes state and then acks.
	c.writeJSON(t, map[string]any{"type": "tabs", "urls": []string{"https://zoom.us/wc/123"}, "seq": 1})
	state := c.readJSON()
	if state["type"] != "state" || state["state"] != "recording" || state["recording_active"] != true {
		t.Fatalf("expected a recording state, got %v", state)
	}
	ack := c.readJSON()
	if ack["type"] != "ack" || ack["seq"] != float64(1) {
		t.Fatalf("expected ack with seq 1, got %v", ack)
	}
}

func TestWebSocketRejectsOldProtocol(t *testing.T) {
	s := newBrowserServer(t, "auto")
	_, secret, err := s.ensurePluginsToken()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c := dialTestWS(t, ts.URL)
	defer c.close()
	// No protocol_version at all: the server must refuse instead of running the
	// new rules silently.
	c.writeJSON(t, helloWithVersion(secret, "old-client", 0))
	msg := c.readJSON()
	if msg == nil || msg["type"] != "error" || msg["error"] != "unsupported_protocol" {
		t.Fatalf("expected unsupported_protocol, got %v", msg)
	}
}

func TestWebSocketRejectsBadToken(t *testing.T) {
	s := newBrowserServer(t, "auto")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c := dialTestWS(t, ts.URL)
	defer c.close()
	c.writeJSON(t, helloWithVersion("lma_pl_bogus", "x", protocolVersion))
	msg := c.readJSON()
	if msg == nil || msg["type"] != "error" || msg["error"] != "forbidden" {
		t.Fatalf("expected forbidden, got %v", msg)
	}
}

func TestWebSocketManualCommandAck(t *testing.T) {
	s := newBrowserServer(t, "auto")
	_, secret, err := s.ensurePluginsToken()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c := dialTestWS(t, ts.URL)
	defer c.close()
	c.writeJSON(t, helloWithVersion(secret, "cmd-client", protocolVersion))
	_ = c.readJSON() // config
	_ = c.readJSON() // state

	// record_manual: the published state comes first, then the result.
	c.writeJSON(t, map[string]any{"type": "tabs", "urls": []string{serviceRecordURL}, "seq": 10, "command_id": "c-start"})
	state := c.readJSON()
	if state["type"] != "state" || state["recording_active"] != true {
		t.Fatalf("expected an active state before the ack, got %v", state)
	}
	ack := c.readJSON()
	if ack["type"] != "ack" || ack["command_id"] != "c-start" || ack["command_result"] != commandApplied {
		t.Fatalf("expected an applied ack, got %v", ack)
	}

	// stop_manual ends it.
	c.writeJSON(t, map[string]any{"type": "tabs", "urls": []string{serviceStopURL}, "seq": 11, "command_id": "c-stop"})
	state = c.readJSON()
	if state["type"] != "state" || state["recording_active"] != false {
		t.Fatalf("expected an inactive state before the ack, got %v", state)
	}
	ack = c.readJSON()
	if ack["command_result"] != commandApplied {
		t.Fatalf("expected an applied stop ack, got %v", ack)
	}

	// A repeated command_id reports the cached result and changes nothing.
	c.writeJSON(t, map[string]any{"type": "tabs", "urls": []string{serviceRecordURL}, "seq": 12, "command_id": "c-start"})
	ack = c.readJSON()
	if ack["type"] != "ack" || ack["command_id"] != "c-start" || ack["command_result"] != commandApplied {
		t.Fatalf("expected the cached ack, got %v", ack)
	}
	if active, _ := s.recordingValues(); active {
		t.Fatal("a repeated command executed the action again")
	}
}

func TestWebSocketTokenRegenerationClosesConnection(t *testing.T) {
	s := newBrowserServer(t, "auto")
	token, secret, err := s.ensurePluginsToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == nil {
		t.Fatal("no plugins token")
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c := dialTestWS(t, ts.URL)
	defer c.close()
	c.writeJSON(t, helloWithVersion(secret, "revoke-client", protocolVersion))
	_ = c.readJSON() // config
	_ = c.readJSON() // state

	s.closeBrowserClientsForToken(token.ID)

	// The server sends a close frame and then drops the socket, so the reader
	// eventually hits EOF.
	closed := false
	for i := 0; i < 4 && !closed; i++ {
		if c.readJSON() == nil {
			closed = true
		}
	}
	if !closed {
		t.Fatal("regenerating a plugin token must close its connections")
	}
}

func TestDisablingIntegrationKeepsRecording(t *testing.T) {
	s := newBrowserServer(t, "auto")
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/1"}, 1, "")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("the browser start did not record")
	}

	cfg := s.config()
	cfg.Integrations.Browser.Enabled = false
	s.setConfig(cfg)
	s.browserOnConfigChanged()

	if active, _ := s.recordingValues(); !active {
		t.Fatal("disabling the integration must not stop a running recording")
	}
	if browserScenario(s) != scenarioIdle || browserOwner(s) != "" {
		t.Fatalf("disabling must release the session, got %s owner=%s", browserScenario(s), browserOwner(s))
	}
	s.browser.mu.Lock()
	reset := true
	for _, c := range s.browser.clients {
		if c.nonEmpty != nil {
			reset = false
		}
	}
	s.browser.mu.Unlock()
	if !reset {
		t.Fatal("disabling must reset the known tab sets so a re-enabled integration can start")
	}

	// Snapshots are ignored while the integration is off.
	s.handleBrowserTabs("c1", []string{"https://zoom.us/wc/2"}, 2, "")
	if browserScenario(s) != scenarioIdle {
		t.Fatalf("a snapshot while disabled must be ignored, got %s", browserScenario(s))
	}
	// ... and commands are rejected.
	s.handleBrowserTabs("c1", []string{serviceStopURL}, 3, "cmd-off")
	if active, _ := s.recordingValues(); !active {
		t.Fatal("a command while disabled must not act")
	}
}

func TestWebSocketAskCarriesRecordingState(t *testing.T) {
	s := newBrowserServer(t, "auto")
	_, secret, err := s.ensurePluginsToken()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c := dialTestWS(t, ts.URL)
	defer c.close()
	c.writeJSON(t, helloWithVersion(secret, "ask-client", protocolVersion))

	// The session loop sends the first poll on its next tick.
	var ask map[string]any
	for i := 0; i < 6 && ask == nil; i++ {
		msg := c.readJSON()
		if msg == nil {
			break
		}
		if msg["type"] == "ask" {
			ask = msg
		}
	}
	if ask == nil {
		t.Fatal("no ask message arrived")
	}
	for _, field := range []string{"recording_active", "recording_revision", "owner"} {
		if _, present := ask[field]; !present {
			t.Fatalf("ask must carry %s, got %v", field, ask)
		}
	}
	if ask["what"] != "tabs" {
		t.Fatalf("ask must request tabs, got %v", ask["what"])
	}
}

// newServerWithStore builds a server over an existing store so a restart can be
// simulated without reopening the database file twice.
func newServerWithStore(t *testing.T, st *store.Store) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	recorder := recording.NewManagerWithBackend(cfg.Audio, log.New(io.Discard, "", 0), serverSyntheticBackend{})
	return NewWithRecorder(cfg, st, log.New(io.Discard, "", 0), recorder)
}
