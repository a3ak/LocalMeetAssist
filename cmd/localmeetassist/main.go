package main

/*
#cgo windows LDFLAGS: -luser32
#include <stdlib.h>

#ifdef _WIN32
#include <windows.h>

// lm_startup_error_box показывает системное окно с текстом ошибки.
//
// На Windows приложение собирается для GUI-подсистемы (-H=windowsgui), поэтому
// stderr никуда не выводится, и без этого окна неудачный запуск выглядел бы как
// «ничего не произошло». Строка приходит в UTF-8, поэтому перед MessageBoxW она
// переводится в UTF-16.
static void lm_startup_error_box(const char* text) {
    if (text == NULL) return;
    int length = MultiByteToWideChar(CP_UTF8, 0, text, -1, NULL, 0);
    if (length <= 0) return;
    wchar_t* wide = (wchar_t*) malloc((size_t) length * sizeof(wchar_t));
    if (wide == NULL) return;
    MultiByteToWideChar(CP_UTF8, 0, text, -1, wide, length);
    MessageBoxW(NULL, wide, L"LocalMeetAssist", MB_ICONERROR | MB_OK);
    free(wide);
}
#else
// На остальных платформах процесс запускается из терминала: ошибку печатает
// fatal в stderr.
static void lm_startup_error_box(const char* text) { (void) text; }
#endif
*/
import "C"

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"localmeetassist/internal/config"
	"localmeetassist/internal/desktop"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/server"
	"localmeetassist/internal/store"
	"localmeetassist/internal/version"
)

// startupErrorFile is set as soon as the configuration path is known. Errors
// that happen before the log file is open are appended here, because a Windows
// GUI build has no console at all.
var startupErrorFile string

// fatal reports an unrecoverable startup problem and exits.
//
// A plain log.Fatal is not enough for the packaged application: on Windows the
// binary is built for the GUI subsystem, so stderr goes nowhere and a user who
// double-clicks the icon would see nothing at all when, for example, the data
// directory is not writable.
func fatal(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, message)
	if startupErrorFile != "" {
		file, err := os.OpenFile(startupErrorFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%s\n", message)
			_ = file.Close()
		}
	}
	text := C.CString(message)
	defer C.free(unsafe.Pointer(text))
	C.lm_startup_error_box(text)
	os.Exit(1)
}

// setStartupErrorFile enables the startup error log next to the configuration.
func setStartupErrorFile(configPath string) {
	if configPath == "" {
		return
	}
	startupErrorFile = filepath.Join(filepath.Dir(configPath), "startup-error.log")
}

func main() {
	configPath := flag.String("config", "", "path to TOML configuration (default: ./config.toml, otherwise the user configuration directory)")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("localmeetassist %s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	absConfig, err := filepath.Abs(config.ResolvePath(*configPath))
	if err != nil {
		fatal("configuration path: %v", err)
	}
	setStartupErrorFile(absConfig)
	// Каталог конфигурации может отсутствовать при первом запуске из .app:
	// относительные пути данных, моделей и логов считаются от него.
	if err := os.MkdirAll(filepath.Dir(absConfig), 0o700); err != nil {
		fatal("configuration directory: %v", err)
	}
	cfg, err := config.Load(absConfig)
	if err != nil {
		fatal("configuration: %v", err)
	}
	cfg.ConfigFile = absConfig
	config.ResolvePaths(&cfg, absConfig)
	if err := os.MkdirAll(cfg.Logging.Directory, 0o700); err != nil {
		fatal("log directory: %v", err)
	}
	logPath := filepath.Join(cfg.Logging.Directory, "localmeetassist.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fatal("log file: %v", err)
	}
	defer logFile.Close()
	logController := appLogging.NewController(cfg.Logging.Level)
	filtered := appLogging.FilterWriter{Output: io.MultiWriter(os.Stdout, logFile), Controller: logController}
	logger := log.New(filtered, "localmeetassist ", log.Ldate|log.Ltime|log.Lmicroseconds)
	appLogging.Infof(logger, "startup version=%s os=%s arch=%s config=%s", version.Version, runtime.GOOS, runtime.GOARCH, absConfig)
	appLogging.Debugf(logger, "paths data=%s database=%s logs=%s", cfg.App.DataDir, cfg.Storage.DatabasePath, logPath)
	appLogging.Debugf(logger, "audio backend=%s microphone=%q system=%q", cfg.Audio.Backend, cfg.Audio.InputDeviceName, cfg.Audio.OutputDeviceName)

	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		fatal("database: %v", err)
	}
	defer st.Close()
	app := server.NewWithLogging(cfg, st, logger, logController)
	reconcile := app.ReconcileMeetingFiles()
	if reconcile.Imported > 0 || reconcile.Refreshed > 0 || reconcile.Artifacts > 0 {
		appLogging.Infof(logger, "meeting files reconciled scanned=%d imported=%d refreshed=%d artifacts=%d", reconcile.Scanned, reconcile.Imported, reconcile.Refreshed, reconcile.Artifacts)
	} else {
		appLogging.Debugf(logger, "meeting files reconciled scanned=%d no_changes=true", reconcile.Scanned)
	}
	for _, warning := range reconcile.Warnings {
		appLogging.Warnf(logger, "meeting files reconciliation warning=%q", warning)
	}
	app.PrepareInference()
	listener, err := app.Listen()
	if err != nil {
		fatal("http server: %v", err)
	}
	httpServer := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	url := "http://" + listener.Addr().String() + "/"
	fmt.Printf("LocalMeetAssist %s запущен: %s\n", version.Version, url)
	fmt.Printf("Данные: %s\n", cfg.App.DataDir)
	fmt.Printf("Лог: %s\n", logPath)
	logger.Printf("started version=%s url=%s", version.Version, url)
	if count := app.RecoverProcessing(); count > 0 {
		logger.Printf("pipeline recovery started count=%d", count)
	}
	if cfg.App.OpenBrowser {
		go func() {
			time.Sleep(250 * time.Millisecond)
			if err := openBrowser(app.IssueUIURL()); err != nil {
				logger.Printf("open browser: %v", err)
			}
		}()
	}
	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Printf("http server: %v", err)
		}
	}()
	detectionCtx, stopDetection := context.WithCancel(context.Background())
	app.StartConferenceDetection(detectionCtx)
	app.StartAudioArchiver(detectionCtx)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	useTray := cfg.Desktop.TrayEnabled
	if runtime.GOOS == "linux" && strings.TrimSpace(os.Getenv("DISPLAY")) == "" && strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) == "" {
		useTray = false
		logger.Print("system tray disabled: no graphical session detected")
	}
	if useTray {
		go func() {
			<-ch
			desktop.Quit()
		}()
		tray := desktop.New(url, app.SessionToken(), func() error { return openBrowser(app.IssueUIURL()) }, app.CurrentConfig, logger)
		tray.Run()
	} else {
		<-ch
	}
	stopDetection()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.StopActiveRecording(ctx); err != nil {
		logger.Printf("active recording finalization: %v", err)
	}
	_ = httpServer.Shutdown(ctx)
	logger.Print("stopped")
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
