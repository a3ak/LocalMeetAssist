package server

import (
	"context"
	"time"
)

// StartAudioArchiver periodically applies storage.audio_archive_days: meetings
// that finished longer ago than the delay lose their source WAVs and keep the
// compressed archive. With 0 the pipeline archives immediately, so this loop
// does nothing.
func (s *Server) StartAudioArchiver(ctx context.Context) {
	go func() {
		// Даём приложению подняться, затем проверяем раз в час: задержка
		// измеряется днями, чаще нет смысла.
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.pipeline.ArchiveDue(ctx, time.Now())
				timer.Reset(time.Hour)
			}
		}
	}()
}
