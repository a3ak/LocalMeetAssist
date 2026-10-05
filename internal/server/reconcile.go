package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"localmeetassist/internal/audio"
	"localmeetassist/internal/config"
	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

// ReconcileReport describes the non-destructive startup scan of data/meetings.
// Existing user edits in bbolt always win; the filesystem is used to restore
// missing meetings and artifact records after an old data directory is copied.
type ReconcileReport struct {
	Scanned   int
	Imported  int
	Refreshed int
	Artifacts int
	Warnings  []string
}

type diskMeetingMetadata struct {
	MeetingUID string     `json:"meeting_uid"`
	Title      string     `json:"title"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
}

type recoveredFile struct {
	Path       string
	Type       string
	MIME       string
	ModifiedAt time.Time
	Content    string
	Segments   []model.Segment
}

var (
	meetingFileTimePattern = regexp.MustCompile(`(?i)(\d{4})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})[-_](\d{2})`)
	transcriptLinePattern  = regexp.MustCompile(`^\[(\d+):(\d{2})(?::(\d{2}))?\]\s+([^:]+):\s*(.*)$`)
)

// ReconcileMeetingFiles imports meetings copied from another LocalMeetAssist data
// directory. It can run on every startup: known artifacts are matched by their
// absolute path and are not duplicated.
func (s *Server) ReconcileMeetingFiles() ReconcileReport {
	cfg := s.config()
	root := filepath.Join(cfg.App.DataDir, "meetings")
	report := ReconcileReport{}
	years, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return report
	}
	if err != nil {
		report.Warnings = append(report.Warnings, err.Error())
		return report
	}
	for _, year := range years {
		if !year.IsDir() {
			continue
		}
		yearPath := filepath.Join(root, year.Name())
		months, readErr := os.ReadDir(yearPath)
		if readErr != nil {
			report.Warnings = append(report.Warnings, readErr.Error())
			continue
		}
		for _, month := range months {
			if !month.IsDir() {
				continue
			}
			monthPath := filepath.Join(yearPath, month.Name())
			meetings, readErr := os.ReadDir(monthPath)
			if readErr != nil {
				report.Warnings = append(report.Warnings, readErr.Error())
				continue
			}
			for _, entry := range meetings {
				if !entry.IsDir() {
					continue
				}
				dir := filepath.Join(monthPath, entry.Name())
				report.Scanned++
				imported, artifacts, reconcileErr := s.reconcileMeetingDirectory(dir, entry.Name(), year.Name(), month.Name())
				if reconcileErr != nil {
					report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", dir, reconcileErr))
					continue
				}
				if imported {
					report.Imported++
				} else if artifacts > 0 {
					report.Refreshed++
				}
				report.Artifacts += artifacts
			}
		}
	}
	return report
}

func (s *Server) reconcileMeetingDirectory(dir, directoryUID, year, month string) (bool, int, error) {
	metadata := diskMeetingMetadata{}
	metadataPath := filepath.Join(dir, "metadata.json")
	if data, err := os.ReadFile(metadataPath); err == nil {
		_ = json.Unmarshal(data, &metadata)
	}
	uid := strings.TrimSpace(metadata.MeetingUID)
	if uid == "" {
		uid = strings.TrimSpace(directoryUID)
	}
	if uid == "" {
		return false, 0, errors.New("meeting UID is empty")
	}
	files, err := collectRecoveredFiles(dir)
	if err != nil {
		return false, 0, err
	}
	if len(files) == 0 {
		return false, 0, nil
	}

	meeting, loadErr := s.store.Meeting(uid)
	imported := errors.Is(loadErr, os.ErrNotExist)
	if loadErr != nil && !imported {
		return false, 0, loadErr
	}
	startedAt := metadata.StartedAt
	if startedAt.IsZero() {
		startedAt = inferMeetingStart(files, year, month)
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if imported {
		title := strings.TrimSpace(metadata.Title)
		if title == "" {
			title = config.Localized(s.config().App.Language, "Восстановленная встреча ", "Recovered meeting ") + startedAt.Format("02.01.2006 15:04")
		}
		meeting = model.Meeting{
			UID:           uid,
			Title:         title,
			StartedAt:     startedAt,
			FinishedAt:    metadata.FinishedAt,
			Status:        recoveredMeetingStatus(metadata.Status, files),
			Source:        "recovered",
			SummaryStatus: "pending",
			CreatedAt:     startedAt,
			UpdatedAt:     time.Now(),
			Metadata:      make(map[string]string),
		}
	} else {
		if meeting.Metadata == nil {
			meeting.Metadata = make(map[string]string)
		}
		if meeting.StartedAt.IsZero() {
			meeting.StartedAt = startedAt
		}
		if meeting.FinishedAt == nil && metadata.FinishedAt != nil {
			meeting.FinishedAt = metadata.FinishedAt
		}
	}

	var newestTranscript, newestSummary *recoveredFile
	var maxDuration int64
	var microphoneSegments, systemSegments, mixedSegments []model.Segment
	availableSources := make(map[string]bool)
	for index := range files {
		file := &files[index]
		switch file.Type {
		case "transcript":
			if newestTranscript == nil || file.ModifiedAt.After(newestTranscript.ModifiedAt) {
				newestTranscript = file
			}
		case "summary":
			if newestSummary == nil || file.ModifiedAt.After(newestSummary.ModifiedAt) {
				newestSummary = file
			}
		case "mic_wav":
			availableSources["microphone"] = true
		case "system_wav":
			availableSources["system"] = true
		case "mixed_wav", "imported_mixed_wav", "mixed_opus", "mixed_mp3":
			availableSources["mixed"] = true
		}
		switch file.Type {
		case "microphone_transcript_json":
			microphoneSegments = append(microphoneSegments, file.Segments...)
		case "system_transcript_json":
			systemSegments = append(systemSegments, file.Segments...)
		case "mixed_transcript_json":
			mixedSegments = append(mixedSegments, file.Segments...)
		}
		if strings.EqualFold(filepath.Ext(file.Path), ".wav") {
			if duration, durationErr := audio.PCM16WAVDurationMS(file.Path); durationErr == nil && duration > maxDuration {
				maxDuration = duration
			}
		}
	}
	recoveredSegments := []model.Segment(nil)
	if newestTranscript != nil {
		recoveredSegments = parseRecoveredTranscript(newestTranscript.Content, s.ownerName())
		if meeting.Transcript == "" {
			meeting.Transcript = newestTranscript.Content
		}
	} else if len(mixedSegments) > 0 {
		recoveredSegments = prepareRecoveredSegments(mixedSegments, false)
		if meeting.Transcript == "" {
			meeting.Transcript = formatRecoveredSegments(recoveredSegments, s.ownerName())
		}
	} else if len(microphoneSegments)+len(systemSegments) > 0 {
		recoveredSegments = append(recoveredSegments, prepareRecoveredSegments(microphoneSegments, true)...)
		recoveredSegments = append(recoveredSegments, prepareRecoveredSegments(systemSegments, false)...)
		sort.SliceStable(recoveredSegments, func(i, j int) bool { return recoveredSegments[i].StartMS < recoveredSegments[j].StartMS })
		if meeting.Transcript == "" {
			meeting.Transcript = formatRecoveredSegments(recoveredSegments, s.ownerName())
		}
	}
	if meeting.Summary == "" && newestSummary != nil {
		meeting.Summary = newestSummary.Content
		meeting.SummaryStatus = "completed"
	}
	if meeting.DurationMS == 0 {
		meeting.DurationMS = maxDuration
		if meeting.DurationMS == 0 && meeting.FinishedAt != nil {
			meeting.DurationMS = meeting.FinishedAt.Sub(meeting.StartedAt).Milliseconds()
		}
	}
	if imported {
		meeting.Metadata["audio_import_sources"] = recoveredSources(availableSources)
		if availableSources["mixed"] && !availableSources["microphone"] && !availableSources["system"] {
			meeting.Metadata["transcription_source_mode"] = "mixed"
		}
	}
	if err := s.store.SaveMeeting(meeting); err != nil {
		return false, 0, err
	}

	known, _ := s.store.Artifacts(uid)
	knownPaths := make(map[string]model.Artifact, len(known))
	for _, artifact := range known {
		knownPaths[filepath.Clean(artifact.Path)] = artifact
	}
	added := 0
	for _, file := range files {
		cleanPath := filepath.Clean(file.Path)
		if artifact, ok := knownPaths[cleanPath]; ok {
			if stat, statErr := os.Stat(cleanPath); statErr == nil && stat.Size() == artifact.SizeBytes {
				continue
			}
		}
		if _, artifactErr := s.makeArtifact(uid, file.Type, file.Path, file.MIME, "recovered"); artifactErr != nil {
			return imported, added, artifactErr
		}
		added++
	}

	if imported {
		if len(recoveredSegments) > 0 {
			speakers := speakersFromRecoveredSegments(uid, recoveredSegments, s.ownerName())
			meeting.SpeakerCount = len(speakers)
			_ = s.store.ReplaceSpeakersAndSegments(uid, speakers, recoveredSegments)
			_ = s.store.SaveMeeting(meeting)
		}
	}
	return imported, added, nil
}

func collectRecoveredFiles(root string) ([]recoveredFile, error) {
	files := make([]recoveredFile, 0, 8)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".previous") || strings.HasSuffix(name, ".before-import") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		file := recoveredFile{Path: path, ModifiedAt: info.ModTime()}
		file.Type, file.MIME, file.Content, file.Segments = classifyRecoveredFile(path)
		files = append(files, file)
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].ModifiedAt.Before(files[j].ModifiedAt) })
	return files, err
}

func classifyRecoveredFile(path string) (string, string, string, []model.Segment) {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	dir := strings.ToLower(filepath.ToSlash(filepath.Dir(path)))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	switch {
	case base == "metadata.json":
		return "metadata", "application/json", "", nil
	case ext == ".wav" && (base == "microphone.wav" || strings.Contains(base, "microphone") || strings.Contains(base, "mic-")):
		return "mic_wav", "audio/wav", "", nil
	case ext == ".wav" && (base == "system.wav" || strings.Contains(base, "system")):
		return "system_wav", "audio/wav", "", nil
	case ext == ".wav" && strings.Contains(base, "mixed"):
		return "mixed_wav", "audio/wav", "", nil
	case ext == ".wav":
		return "mixed_wav", "audio/wav", "", nil
	case ext == ".opus" || ext == ".ogg":
		return "mixed_opus", "audio/ogg", "", nil
	case ext == ".mp3":
		return "mixed_mp3", "audio/mpeg", "", nil
	case ext == ".json":
		data, _ := os.ReadFile(path)
		var envelope struct {
			ArtifactType string          `json:"artifact_type"`
			Segments     []model.Segment `json:"segments"`
		}
		_ = json.Unmarshal(data, &envelope)
		kind := strings.TrimSpace(envelope.ArtifactType)
		if kind == "diarization" {
			kind = "diarization_json"
		} else if strings.HasSuffix(kind, "_transcript") {
			kind += "_json"
		} else if kind == "" {
			kind = "recovered_json"
		}
		return kind, "application/json", "", envelope.Segments
	case ext == ".md" || ext == ".txt":
		data, _ := os.ReadFile(path)
		content, kind := stripRecoveredFrontMatter(string(data))
		if kind == "" {
			if strings.Contains(dir, "/summary") || strings.Contains(base, "summary") {
				kind = "summary"
			} else if strings.Contains(dir, "/transcript") || strings.Contains(base, "transcript") {
				kind = "transcript"
			} else {
				kind = "recovered_text"
			}
		}
		return kind, "text/plain; charset=utf-8", content, nil
	default:
		return "recovered_file", mimeType, "", nil
	}
}

func stripRecoveredFrontMatter(value string) (string, string) {
	value = strings.TrimPrefix(value, "\ufeff")
	if !strings.HasPrefix(value, "---\n") && !strings.HasPrefix(value, "---\r\n") {
		return strings.TrimSpace(value), ""
	}
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	kind, end := "", -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
		if key, val, ok := strings.Cut(lines[index], ":"); ok && strings.TrimSpace(key) == "artifact_type" {
			kind = strings.TrimSpace(val)
		}
	}
	if end < 0 {
		return strings.TrimSpace(value), kind
	}
	return strings.TrimSpace(strings.Join(lines[end+1:], "\n")), kind
}

func recoveredMeetingStatus(status string, files []recoveredFile) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "recording" || status == "processing" || status == "starting" || status == "" {
		status = "created"
	}
	if status == "failed" || status == "warning" || status == "canceled" || status == "completed" {
		return status
	}
	for _, file := range files {
		if file.Type == "transcript" || file.Type == "summary" {
			return "completed"
		}
	}
	return status
}

func recoveredSources(sources map[string]bool) string {
	values := make([]string, 0, 3)
	order := []string{"microphone", "system"}
	if !sources["microphone"] && !sources["system"] {
		order = []string{"mixed"}
	}
	for _, value := range order {
		if sources[value] {
			values = append(values, value)
		}
	}
	return strings.Join(values, ",")
}

func inferMeetingStart(files []recoveredFile, year, month string) time.Time {
	for _, file := range files {
		match := meetingFileTimePattern.FindStringSubmatch(filepath.Base(file.Path))
		if len(match) == 7 {
			values := make([]int, 6)
			valid := true
			for index := range values {
				values[index], _ = strconv.Atoi(match[index+1])
				valid = valid && values[index] >= 0
			}
			if valid {
				return time.Date(values[0], time.Month(values[1]), values[2], values[3], values[4], values[5], 0, time.Local)
			}
		}
	}
	y, yearErr := strconv.Atoi(year)
	m, monthErr := strconv.Atoi(month)
	if yearErr == nil && monthErr == nil && y > 2000 && m >= 1 && m <= 12 {
		return time.Date(y, time.Month(m), 1, 12, 0, 0, 0, time.Local)
	}
	return time.Time{}
}

func parseRecoveredTranscript(content, ownerName string) []model.Segment {
	segments := make([]model.Segment, 0)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		match := transcriptLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 6 {
			continue
		}
		first, _ := strconv.ParseInt(match[1], 10, 64)
		second, _ := strconv.ParseInt(match[2], 10, 64)
		third, _ := strconv.ParseInt(match[3], 10, 64)
		startMS := (first*60 + second) * 1000
		if match[3] != "" {
			startMS = (first*3600 + second*60 + third) * 1000
		}
		name := strings.TrimSpace(match[4])
		source := "system"
		id := recoveredSpeakerID(name)
		if strings.EqualFold(name, strings.TrimSpace(ownerName)) || strings.EqualFold(name, "Владелец микрофона") || strings.EqualFold(name, "Microphone owner") {
			id, source = "microphone_owner", "microphone"
		}
		segments = append(segments, model.Segment{ID: uuidv7.New(), StartMS: startMS, EndMS: startMS + 10000, SpeakerID: id, Source: source, Text: strings.TrimSpace(match[5])})
	}
	for index := 0; index+1 < len(segments); index++ {
		if segments[index+1].StartMS > segments[index].StartMS && segments[index+1].StartMS < segments[index].EndMS {
			segments[index].EndMS = segments[index+1].StartMS
		}
	}
	return segments
}

func prepareRecoveredSegments(input []model.Segment, microphone bool) []model.Segment {
	segments := append([]model.Segment(nil), input...)
	for index := range segments {
		if segments[index].ID == "" {
			segments[index].ID = uuidv7.New()
		}
		if microphone {
			segments[index].SpeakerID = "microphone_owner"
			segments[index].Source = "microphone"
		} else {
			if segments[index].SpeakerID == "" {
				segments[index].SpeakerID = "SPEAKER_00"
			}
			if segments[index].Source == "" {
				segments[index].Source = "system"
			}
		}
	}
	return segments
}

func formatRecoveredSegments(segments []model.Segment, ownerName string) string {
	if strings.TrimSpace(ownerName) == "" {
		ownerName = config.DefaultMicrophoneOwnerName("")
	}
	var output strings.Builder
	for _, segment := range segments {
		name := segment.SpeakerID
		if name == "microphone_owner" {
			name = ownerName
		}
		fmt.Fprintf(&output, "[%02d:%02d] %s: %s\n", segment.StartMS/60000, (segment.StartMS/1000)%60, name, strings.TrimSpace(segment.Text))
	}
	return strings.TrimSpace(output.String())
}

func recoveredSpeakerID(name string) string {
	trimmed := strings.TrimSpace(name)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "SPEAKER_") {
		return upper
	}
	if trimmed != "" {
		return trimmed
	}
	hash := sha256.Sum256([]byte(strings.ToLower(trimmed)))
	return "RECOVERED_" + strings.ToUpper(hex.EncodeToString(hash[:4]))
}

func speakersFromRecoveredSegments(meetingUID string, segments []model.Segment, ownerName string) []model.Speaker {
	byID := make(map[string]*model.Speaker)
	for _, segment := range segments {
		speaker := byID[segment.SpeakerID]
		if speaker == nil {
			displayName := segment.SpeakerID
			if segment.SpeakerID == "microphone_owner" {
				displayName = strings.TrimSpace(ownerName)
				if displayName == "" {
					displayName = config.DefaultMicrophoneOwnerName("")
				}
			}
			speaker = &model.Speaker{MeetingUID: meetingUID, ID: segment.SpeakerID, DisplayName: displayName, Source: segment.Source}
			byID[segment.SpeakerID] = speaker
		}
		fragment := model.Sample{ID: segment.ID, StartMS: segment.StartMS, EndMS: segment.EndMS, Source: segment.Source, Text: segment.Text}
		speaker.Fragments = append(speaker.Fragments, fragment)
		if len(speaker.Samples) < 3 && segment.EndMS > segment.StartMS {
			speaker.Samples = append(speaker.Samples, fragment)
		}
	}
	result := make([]model.Speaker, 0, len(byID))
	for _, speaker := range byID {
		result = append(result, *speaker)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
