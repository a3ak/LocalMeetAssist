package main

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

	"localmeetassist/internal/config"
	"localmeetassist/internal/desktop"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/server"
	"localmeetassist/internal/store"
	"localmeetassist/internal/version"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to TOML configuration")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("localmeetassist %s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(absConfig)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	cfg.ConfigFile = absConfig
	config.ResolvePaths(&cfg, absConfig)
	if err := os.MkdirAll(cfg.Logging.Directory, 0o700); err != nil {
		log.Fatal(err)
	}
	logPath := filepath.Join(cfg.Logging.Directory, "localmeetassist.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
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
		log.Fatalf("database: %v", err)
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
		log.Fatal(err)
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
