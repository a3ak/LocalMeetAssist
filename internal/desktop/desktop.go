// Package desktop provides the system tray icon and global hotkeys.
package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"golang.design/x/hotkey"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
)

// Manager owns the tray icon and registered global hotkeys.
type Manager struct {
	baseURL   string
	token     string
	openUI    func() error
	config    func() config.Config
	logger    *log.Logger
	client    *http.Client
	mu        sync.Mutex
	active    *model.Meeting
	bindings  []*hotkey.Hotkey
	shortcut  config.Desktop
	status    *systray.MenuItem
	startItem *systray.MenuItem
	stopItem  *systray.MenuItem
	iconState int
	renderCh  chan struct{}
}

// New creates the tray manager for the running server.
func New(baseURL, token string, openUI func() error, configProvider func() config.Config, logger *log.Logger) *Manager {
	return &Manager{baseURL: strings.TrimRight(baseURL, "/"), token: token, openUI: openUI, config: configProvider, logger: logger, client: &http.Client{Timeout: 8 * time.Second}, iconState: -1, renderCh: make(chan struct{}, 1)}
}

// Run owns the platform event loop and blocks until Quit is selected or a
// signal calls systray.Quit. It must be called from main.main on macOS.
func (m *Manager) Run() {
	systray.Run(m.ready, m.closeBindings)
}

// Quit stops the tray event loop.
func Quit() { systray.Quit() }

func (m *Manager) ready() {
	systray.SetIcon(trayIcon(false))
	m.iconState = 0
	systray.SetTooltip(m.tr("LocalMeetAssist — запись не ведётся", "LocalMeetAssist — not recording"))
	m.status = systray.AddMenuItem(m.tr("Запись не ведётся", "Not recording"), m.tr("Текущее состояние LocalMeetAssist", "Current LocalMeetAssist state"))
	m.status.Disable()
	m.startItem = systray.AddMenuItem(m.tr("Начать запись", "Start recording"), m.tr("Записать микрофон и системный звук", "Record microphone and system audio"))
	m.stopItem = systray.AddMenuItem(m.tr("Остановить запись", "Stop recording"), m.tr("Завершить текущую встречу", "Finish the current meeting"))
	m.stopItem.Disable()
	systray.AddSeparator()
	openItem := systray.AddMenuItem(m.tr("Открыть Web UI", "Open Web UI"), m.tr("Открыть LocalMeetAssist в браузере", "Open LocalMeetAssist in the browser"))
	quitItem := systray.AddMenuItem(m.tr("Завершить LocalMeetAssist", "Quit LocalMeetAssist"), m.tr("Остановить запись и закрыть приложение", "Stop recording and close the application"))

	go m.menuLoop(m.startItem.ClickedCh, m.stopItem.ClickedCh, openItem.ClickedCh, quitItem.ClickedCh)
	go m.presentationLoop()
	go m.stateLoop()
	m.reloadHotkeys()
}

func (m *Manager) menuLoop(start, stop, open, quit <-chan struct{}) {
	for {
		select {
		case <-start:
			go m.startRecording()
		case <-stop:
			go m.stopRecording()
		case <-open:
			m.openBrowser()
		case <-quit:
			systray.Quit()
			return
		}
	}
}

func (m *Manager) stateLoop() {
	stateTicker := time.NewTicker(2 * time.Second)
	clockTicker := time.NewTicker(time.Second)
	hotkeyTicker := time.NewTicker(5 * time.Second)
	defer stateTicker.Stop()
	defer clockTicker.Stop()
	defer hotkeyTicker.Stop()
	for {
		select {
		case <-stateTicker.C:
			m.refreshRecording()
		case <-clockTicker.C:
			m.requestPresentation()
		case <-hotkeyTicker.C:
			m.reloadHotkeys()
		}
	}
}

func (m *Manager) refreshRecording() {
	var meetings []model.Meeting
	if err := m.request(http.MethodGet, "/api/v1/meetings", nil, &meetings); err != nil {
		m.logger.Printf("tray refresh: %v", err)
		return
	}
	var active *model.Meeting
	for index := range meetings {
		if meetings[index].Status == "recording" || meetings[index].Status == "starting" {
			copy := meetings[index]
			active = &copy
			break
		}
	}
	m.mu.Lock()
	changed := (m.active == nil) != (active == nil)
	if m.active != nil && active != nil && m.active.UID != active.UID {
		changed = true
	}
	m.active = active
	m.mu.Unlock()
	if changed {
		m.requestPresentation()
	}
}

func (m *Manager) requestPresentation() {
	select {
	case m.renderCh <- struct{}{}:
	default:
	}
}

func (m *Manager) presentationLoop() {
	for range m.renderCh {
		m.updatePresentation()
	}
}

func (m *Manager) updatePresentation() {
	m.mu.Lock()
	active := m.active
	desiredIcon := 0
	if active != nil {
		desiredIcon = 1
	}
	iconChanged := m.iconState != desiredIcon
	m.iconState = desiredIcon
	m.mu.Unlock()
	if active == nil {
		m.status.SetTitle(m.tr("Запись не ведётся", "Not recording"))
		m.startItem.Enable()
		m.stopItem.Disable()
		if iconChanged {
			systray.SetIcon(trayIcon(false))
		}
		systray.SetTooltip(m.tr("LocalMeetAssist — запись не ведётся", "LocalMeetAssist — not recording"))
		return
	}
	duration := time.Since(active.StartedAt)
	if duration < 0 {
		duration = 0
	}
	label := fmt.Sprintf(m.tr("Идёт запись %02d:%02d:%02d", "Recording %02d:%02d:%02d"), int(duration.Hours()), int(duration.Minutes())%60, int(duration.Seconds())%60)
	m.status.SetTitle(label)
	m.startItem.Disable()
	m.stopItem.Enable()
	if iconChanged {
		systray.SetIcon(trayIcon(true))
	}
	systray.SetTooltip("LocalMeetAssist — " + label)
}

func (m *Manager) startRecording() {
	m.mu.Lock()
	active := m.active != nil
	m.mu.Unlock()
	if active {
		return
	}
	var meeting model.Meeting
	if err := m.request(http.MethodPost, "/api/v1/recordings", map[string]any{}, &meeting); err != nil {
		m.showError("Could not start recording", err)
		return
	}
	m.mu.Lock()
	m.active = &meeting
	m.mu.Unlock()
	m.requestPresentation()
}

func (m *Manager) stopRecording() {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active == nil {
		m.refreshRecording()
		m.mu.Lock()
		active = m.active
		m.mu.Unlock()
	}
	if active == nil {
		return
	}
	if err := m.request(http.MethodPost, "/api/v1/recordings/"+active.UID+"/stop", map[string]any{}, nil); err != nil {
		m.showError("Could not stop recording", err)
		return
	}
	m.mu.Lock()
	m.active = nil
	m.mu.Unlock()
	m.requestPresentation()
}

func (m *Manager) toggleRecording() {
	m.mu.Lock()
	active := m.active != nil
	m.mu.Unlock()
	if active {
		m.stopRecording()
	} else {
		m.startRecording()
	}
}

func (m *Manager) openBrowser() {
	if m.openUI != nil {
		if err := m.openUI(); err != nil {
			m.showError("Could not open the Web UI", err)
		}
	}
}

func (m *Manager) showError(prefix string, err error) {
	m.logger.Printf("tray: %s: %v", prefix, err)
	if m.status != nil {
		m.status.SetTitle(prefix + ": " + err.Error())
	}
}

func (m *Manager) request(method, path string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, m.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Every API path requires the session token, reads included.
	req.Header.Set("X-Meeting-Token", m.token)
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

type hotkeySpec struct {
	name   string
	value  string
	action func()
}

func (m *Manager) reloadHotkeys() {
	if m.config == nil {
		return
	}
	next := m.config().Desktop
	m.mu.Lock()
	if next == m.shortcut {
		m.mu.Unlock()
		return
	}
	m.shortcut = next
	old := m.bindings
	m.bindings = nil
	m.mu.Unlock()
	for _, binding := range old {
		_ = binding.Unregister()
	}
	var specs []hotkeySpec
	toggle := strings.TrimSpace(next.ToggleRecord)
	start, stop := strings.TrimSpace(next.StartRecording), strings.TrimSpace(next.StopRecording)
	if toggle == "" && start != "" && strings.EqualFold(start, stop) {
		toggle, start, stop = start, "", ""
	}
	if toggle != "" {
		specs = append(specs, hotkeySpec{"toggle_record", toggle, m.toggleRecording})
	} else {
		if start != "" {
			specs = append(specs, hotkeySpec{"start_recording", start, m.startRecording})
		}
		if stop != "" {
			specs = append(specs, hotkeySpec{"stop_recording", stop, m.stopRecording})
		}
	}
	if value := strings.TrimSpace(next.OpenUI); value != "" {
		specs = append(specs, hotkeySpec{"open_ui", value, m.openBrowser})
	}
	for _, spec := range specs {
		binding, err := m.parseHotkey(spec.value)
		if err != nil {
			m.logger.Printf("hotkey %q ignored: %v", spec.value, err)
			m.showError("Hotkey was not applied", fmt.Errorf("%s (%s): %w", spec.value, spec.name, err))
			continue
		}
		if err := binding.Register(); err != nil {
			m.logger.Printf("hotkey %q registration failed: %v", spec.value, err)
			m.showError("Hotkey was not registered", fmt.Errorf("%s (%s): %w", spec.value, spec.name, err))
			continue
		}
		m.mu.Lock()
		m.bindings = append(m.bindings, binding)
		m.mu.Unlock()
		m.logger.Printf("hotkey registered action=%s shortcut=%q", spec.name, spec.value)
		go func(h *hotkey.Hotkey, action func()) {
			for range h.Keydown() {
				go action()
			}
		}(binding, spec.action)
	}
}

func (m *Manager) closeBindings() {
	m.mu.Lock()
	bindings := m.bindings
	m.bindings = nil
	m.mu.Unlock()
	for _, binding := range bindings {
		_ = binding.Unregister()
	}
}

func (m *Manager) parseHotkey(value string) (*hotkey.Hotkey, error) {
	parts := strings.Split(value, "+")
	if len(parts) < 2 {
		return nil, errors.New("a modifier and a key are required, for example Ctrl+Alt+R")
	}
	mods := make([]hotkey.Modifier, 0, len(parts)-1)
	for _, part := range parts[:len(parts)-1] {
		modifier, ok := platformModifier(strings.ToLower(strings.TrimSpace(part)))
		if !ok {
			return nil, fmt.Errorf("unknown modifier %q", part)
		}
		mods = append(mods, modifier)
	}
	key, ok := shortcutKey(strings.ToUpper(strings.TrimSpace(parts[len(parts)-1])))
	if !ok {
		return nil, fmt.Errorf("unknown key %q", parts[len(parts)-1])
	}
	return hotkey.New(mods, key), nil
}

func shortcutKey(value string) (hotkey.Key, bool) {
	keys := map[string]hotkey.Key{
		"A": hotkey.KeyA, "B": hotkey.KeyB, "C": hotkey.KeyC, "D": hotkey.KeyD, "E": hotkey.KeyE, "F": hotkey.KeyF, "G": hotkey.KeyG, "H": hotkey.KeyH, "I": hotkey.KeyI, "J": hotkey.KeyJ, "K": hotkey.KeyK, "L": hotkey.KeyL, "M": hotkey.KeyM, "N": hotkey.KeyN, "O": hotkey.KeyO, "P": hotkey.KeyP, "Q": hotkey.KeyQ, "R": hotkey.KeyR, "S": hotkey.KeyS, "T": hotkey.KeyT, "U": hotkey.KeyU, "V": hotkey.KeyV, "W": hotkey.KeyW, "X": hotkey.KeyX, "Y": hotkey.KeyY, "Z": hotkey.KeyZ,
		"0": hotkey.Key0, "1": hotkey.Key1, "2": hotkey.Key2, "3": hotkey.Key3, "4": hotkey.Key4, "5": hotkey.Key5, "6": hotkey.Key6, "7": hotkey.Key7, "8": hotkey.Key8, "9": hotkey.Key9,
		"F1": hotkey.KeyF1, "F2": hotkey.KeyF2, "F3": hotkey.KeyF3, "F4": hotkey.KeyF4, "F5": hotkey.KeyF5, "F6": hotkey.KeyF6, "F7": hotkey.KeyF7, "F8": hotkey.KeyF8, "F9": hotkey.KeyF9, "F10": hotkey.KeyF10, "F11": hotkey.KeyF11, "F12": hotkey.KeyF12,
		"SPACE": hotkey.KeySpace,
	}
	key, ok := keys[value]
	return key, ok
}

func trayIcon(recording bool) []byte {
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{0, 0, 0, 0}}, image.Point{}, draw.Src)
	blue := color.RGBA{35, 101, 165, 255}
	white := color.RGBA{255, 255, 255, 255}
	for y := 4; y < 60; y++ {
		for x := 4; x < 60; x++ {
			dx, dy := x-32, y-32
			if dx*dx+dy*dy <= 28*28 {
				img.Set(x, y, blue)
			}
		}
	}
	// Compact LM monogram remains visible when the recording badge is shown.
	draw.Draw(img, image.Rect(17, 18, 22, 43), &image.Uniform{C: white}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(21, 38, 31, 43), &image.Uniform{C: white}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(34, 20, 39, 43), &image.Uniform{C: white}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(48, 20, 53, 43), &image.Uniform{C: white}, image.Point{}, draw.Src)
	for i := 0; i < 8; i++ {
		img.Set(39+i, 22+i, white)
		img.Set(47-i, 22+i, white)
	}
	if recording {
		for y := 38; y < 64; y++ {
			for x := 38; x < 64; x++ {
				dx, dy := x-51, y-51
				if dx*dx+dy*dy <= 12*12 {
					img.Set(x, y, color.RGBA{224, 42, 42, 255})
				}
			}
		}
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, img)
	if runtime.GOOS != "windows" {
		return pngData.Bytes()
	}
	// Windows notification icons accept an ICO container with PNG payload.
	data := pngData.Bytes()
	var ico bytes.Buffer
	_ = binary.Write(&ico, binary.LittleEndian, uint16(0))
	_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
	_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
	ico.Write([]byte{size, size, 0, 0})
	_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
	_ = binary.Write(&ico, binary.LittleEndian, uint16(32))
	_ = binary.Write(&ico, binary.LittleEndian, uint32(len(data)))
	_ = binary.Write(&ico, binary.LittleEndian, uint32(22))
	ico.Write(data)
	return ico.Bytes()
}
