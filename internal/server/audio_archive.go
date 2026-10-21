package server

import (
	"context"
	"os"
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
				s.removeExpiredMeetings(ctx, time.Now())
				timer.Reset(time.Hour)
			}
		}
	}()
}

// removeExpiredMeetings deletes meetings older than storage.meeting_retention_days
// together with their files. It waits until nothing is recorded or processed.
func (s *Server) removeExpiredMeetings(ctx context.Context, now time.Time) {
	days := s.config().Storage.MeetingRetentionDays
	if days <= 0 {
		return
	}
	if s.recorder.AnyActive() || s.pipeline.AnyRunning() {
		return
	}
	meetings, err := s.store.ListMeetings(nil, nil)
	if err != nil {
		s.logger.Printf("retention: cannot list meetings: %v", err)
		return
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	for _, meeting := range meetings {
		if ctx.Err() != nil {
			return
		}
		finished := meeting.StartedAt
		if meeting.FinishedAt != nil {
			finished = *meeting.FinishedAt
		}
		if !finished.Before(cutoff) {
			continue
		}
		artifacts, err := s.store.DeleteMeeting(meeting.UID)
		if err != nil {
			s.logger.Printf("retention: delete meeting %s: %v", meeting.UID, err)
			continue
		}
		for _, artifact := range artifacts {
			if s.safeManagedPath(artifact.Path) {
				_ = os.Remove(artifact.Path)
			}
		}
		if dir := s.meetingDir(meeting); s.safeManagedPath(dir) {
			_ = os.RemoveAll(dir)
		}
		s.logger.Printf("retention removed meeting uid=%s started=%s", meeting.UID, meeting.StartedAt.Format(time.RFC3339))
	}
}
