package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func retryVoiceStage(t *testing.T, s *Server, uid, stage string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/meetings/"+uid+"/jobs/"+stage+"/retry", nil)
	req.Header.Set("X-Meeting-Token", s.token)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	return recorder
}

func waitForPipeline(t *testing.T, s *Server, uid string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.pipeline.IsRunning(uid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.pipeline.IsRunning(uid) {
		t.Fatal("pipeline did not finish")
	}
}

func TestRetryVoiceStagesAreAccepted(t *testing.T) {
	for _, stage := range []string{"matching", "enrolling"} {
		t.Run(stage, func(t *testing.T) {
			s, st, meeting := newSpeakerServer(t)
			recorder := retryVoiceStage(t, s, meeting.UID, stage)
			if recorder.Code != http.StatusAccepted {
				t.Fatalf("retry %s failed: %d %s", stage, recorder.Code, recorder.Body.String())
			}
			waitForPipeline(t, s, meeting.UID)
			jobs, err := st.Jobs(meeting.UID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, job := range jobs {
				if job.Stage != stage {
					continue
				}
				found = true
				if job.Status != "skipped" {
					t.Fatalf("stage %s = %s (%s), want skipped", stage, job.Status, job.LastError)
				}
			}
			if !found {
				t.Fatalf("no job for stage %s", stage)
			}
		})
	}
}
