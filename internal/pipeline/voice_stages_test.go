package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/store"
)

func voiceStageFixture(t *testing.T, remember bool) (*Runner, *store.Store, model.Meeting) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	meeting := model.Meeting{UID: "meeting-stages", Title: "Stages", StartedAt: now, Status: "completed", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Diarization.RememberSpeakers = remember
	cfg.App.DataDir = dir
	runner := New(cfg, st)
	artifactPath := filepath.Join(dir, "diarization.json")
	body := `{"schema_version":3,"audio_source":"system","turns":[{"start_ms":0,"end_ms":5000,"speaker_id":"SPEAKER_00"}],"candidates":[]}`
	if err := os.WriteFile(artifactPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveArtifact(model.Artifact{UID: "diarization", MeetingUID: meeting.UID, Type: "diarization_json", Path: artifactPath, MIMEType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	return runner, st, meeting
}

func TestRecognitionSkipsWithoutEmbeddingsOrAudio(t *testing.T) {
	runner, st, meeting := voiceStageFixture(t, true)
	if err := runner.retryRecognition(context.Background(), meeting.UID); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.Jobs(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	var matching *model.Job
	for index := range jobs {
		if jobs[index].Stage == "matching" {
			matching = &jobs[index]
		}
	}
	if matching == nil || matching.Status != "skipped" {
		t.Fatalf("expected a skipped matching job, got %+v", jobs)
	}
	updated, err := st.Meeting(meeting.UID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "completed" {
		t.Fatalf("recognition skip must restore completed status: %+v", updated)
	}
}

func TestRecognitionSkipsWhenDisabled(t *testing.T) {
	runner, st, meeting := voiceStageFixture(t, false)
	if err := runner.retryRecognition(context.Background(), meeting.UID); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.Jobs(meeting.UID)
	for _, job := range jobs {
		if job.Stage == "matching" {
			if job.Status != "skipped" || job.LastError != "speaker memory is disabled" {
				t.Fatalf("unexpected matching job: %+v", job)
			}
			return
		}
	}
	t.Fatal("matching job was not recorded")
}

func TestEnrollmentSkipsWithoutManualNames(t *testing.T) {
	runner, st, meeting := voiceStageFixture(t, true)
	if err := st.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Иван", Source: "system", NameSource: "auto"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.retryEnrollment(context.Background(), meeting.UID); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.Jobs(meeting.UID)
	for _, job := range jobs {
		if job.Stage == "enrolling" {
			if job.Status != "skipped" || job.LastError != "no speakers named by hand" {
				t.Fatalf("unexpected enrolling job: %+v", job)
			}
			return
		}
	}
	t.Fatal("enrolling job was not recorded")
}

func TestEnrollmentSkipsWhenAudioArchived(t *testing.T) {
	runner, st, meeting := voiceStageFixture(t, true)
	if err := st.SaveSpeaker(model.Speaker{MeetingUID: meeting.UID, ID: "SPEAKER_00", DisplayName: "Иван", Source: "system", NameSource: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.retryEnrollment(context.Background(), meeting.UID); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.Jobs(meeting.UID)
	for _, job := range jobs {
		if job.Stage == "enrolling" {
			if job.Status != "skipped" || job.LastError != "audio is archived; enrollment is unavailable" {
				t.Fatalf("unexpected enrolling job: %+v", job)
			}
			return
		}
	}
	t.Fatal("enrolling job was not recorded")
}

func TestManualSpeakerIDsExcludesAutomaticNames(t *testing.T) {
	speakers := []model.Speaker{
		{ID: "SPEAKER_00", DisplayName: "Иван", NameSource: "manual", Source: "system"},
		{ID: "SPEAKER_01", DisplayName: "Пётр", NameSource: "auto", Source: "system"},
		{ID: "SPEAKER_02", DisplayName: "Анна", NameSource: "", Source: "system"},
		{ID: "microphone_owner", DisplayName: "Владелец", Source: "microphone"},
	}
	ids := manualSpeakerIDs(speakers)
	if len(ids) != 2 || ids[0] != "SPEAKER_00" || ids[1] != "SPEAKER_02" {
		t.Fatalf("unexpected manual ids: %+v", ids)
	}
}

func TestProcessingStageOrder(t *testing.T) {
	want := []string{"transcribing", "diarizing", "deduplicating", "merging", "matching", "summarizing", "enrolling", "encoding"}
	if len(processingStageOrder) != len(want) {
		t.Fatalf("stage order = %+v", processingStageOrder)
	}
	for index := range want {
		if processingStageOrder[index] != want[index] {
			t.Fatalf("stage %d = %q, want %q", index, processingStageOrder[index], want[index])
		}
	}
}
