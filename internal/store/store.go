// Package store persists meetings, artifacts, jobs and speakers in bbolt.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"localmeetassist/internal/model"
)

var (
	bMeta        = []byte("meta")
	bMeetings    = []byte("meetings")
	bArtifacts   = []byte("artifacts")
	bJobs        = []byte("jobs")
	bSpeakers    = []byte("speakers")
	bSegments    = []byte("transcript_segments")
	bTokens      = []byte("tokens")
	bIntegration = []byte("integration_state")
	bPeople      = []byte("people")
	bVoice       = []byte("voice_samples")
)

// Store is a bbolt-backed repository. All access goes through short
// read/write transactions.
type Store struct{ db *bolt.DB }

// Open creates or opens the database and its buckets.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bMeta, bMeetings, bArtifacts, bJobs, bSpeakers, bSegments, bTokens, bIntegration, bPeople, bVoice} {
			if _, e := tx.CreateBucketIfNotExists(name); e != nil {
				return e
			}
		}
		return tx.Bucket(bMeta).Put([]byte("schema_version"), []byte("3"))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// SaveToken inserts or replaces an API token by ID.
func (s *Store) SaveToken(token model.Token) error {
	if token.ID == "" {
		return errors.New("token id is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bTokens), token.ID, token) })
}

// Tokens returns every stored token, oldest first.
func (s *Store) Tokens() ([]model.Token, error) {
	out := make([]model.Token, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bTokens).ForEach(func(_, v []byte) error {
			var token model.Token
			if err := json.Unmarshal(v, &token); err != nil {
				return err
			}
			out = append(out, token)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// FindTokenByHash returns the token whose stored hash matches hash.
func (s *Store) FindTokenByHash(hash string) (model.Token, error) {
	var found model.Token
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bTokens).ForEach(func(_, v []byte) error {
			var token model.Token
			if err := json.Unmarshal(v, &token); err != nil {
				return err
			}
			if token.Hash == hash {
				found = token
			}
			return nil
		})
	})
	if err != nil {
		return model.Token{}, err
	}
	if found.ID == "" {
		return model.Token{}, os.ErrNotExist
	}
	return found, nil
}

// DeleteToken removes a token by ID.
func (s *Store) DeleteToken(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bTokens).Get([]byte(id)) == nil {
			return os.ErrNotExist
		}
		return tx.Bucket(bTokens).Delete([]byte(id))
	})
}

// TouchTokenLastUsed records the last time a token was used.
func (s *Store) TouchTokenLastUsed(id string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bTokens)
		data := b.Get([]byte(id))
		if data == nil {
			return os.ErrNotExist
		}
		var token model.Token
		if err := json.Unmarshal(data, &token); err != nil {
			return err
		}
		token.LastUsedAt = &at
		return putJSON(b, id, token)
	})
}

func putJSON(b *bolt.Bucket, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

func getJSON[T any](b *bolt.Bucket, key string) (T, error) {
	var out T
	data := b.Get([]byte(key))
	if data == nil {
		return out, os.ErrNotExist
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

// deletePrefix removes every key in the bucket that starts with prefix.
func deletePrefix(b *bolt.Bucket, prefix string) error {
	var keys [][]byte
	if err := b.ForEach(func(k, _ []byte) error {
		if strings.HasPrefix(string(k), prefix) {
			keys = append(keys, append([]byte(nil), k...))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// SaveMeeting inserts or replaces a meeting by UID.
func (s *Store) SaveMeeting(m model.Meeting) error {
	if m.UID == "" {
		return errors.New("meeting uid is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bMeetings), m.UID, m) })
}

// Meeting loads a meeting and fills its artifact UIDs.
func (s *Store) Meeting(uid string) (model.Meeting, error) {
	var m model.Meeting
	err := s.db.View(func(tx *bolt.Tx) error {
		var e error
		m, e = getJSON[model.Meeting](tx.Bucket(bMeetings), uid)
		return e
	})
	if err != nil {
		return m, err
	}
	arts, _ := s.Artifacts(uid)
	m.ArtifactUIDs = make([]string, 0, len(arts))
	for _, a := range arts {
		m.ArtifactUIDs = append(m.ArtifactUIDs, a.UID)
	}
	return m, nil
}

// ListMeetings returns meetings inside the optional [from, to) range,
// newest first.
func (s *Store) ListMeetings(from, to *time.Time) ([]model.Meeting, error) {
	out := make([]model.Meeting, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bMeetings).ForEach(func(_, v []byte) error {
			var m model.Meeting
			if err := json.Unmarshal(v, &m); err != nil {
				return err
			}
			if from != nil && m.StartedAt.Before(*from) {
				return nil
			}
			if to != nil && !m.StartedAt.Before(*to) {
				return nil
			}
			out = append(out, m)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, err
}

// DeleteMeeting removes a meeting with its artifacts, jobs and speakers and
// returns the artifacts so the caller can delete their files.
func (s *Store) DeleteMeeting(uid string) ([]model.Artifact, error) {
	arts, err := s.Artifacts(uid)
	if err != nil {
		return nil, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bMeetings).Get([]byte(uid)) == nil {
			return os.ErrNotExist
		}
		if err := tx.Bucket(bMeetings).Delete([]byte(uid)); err != nil {
			return err
		}
		for _, a := range arts {
			if err := tx.Bucket(bArtifacts).Delete([]byte(a.UID)); err != nil {
				return err
			}
		}
		for _, bucket := range []*bolt.Bucket{tx.Bucket(bJobs), tx.Bucket(bSpeakers)} {
			_ = deletePrefix(bucket, uid+"/")
		}
		if err := tx.Bucket(bSegments).Delete([]byte(uid)); err != nil {
			return err
		}
		return nil
	})
	return arts, err
}

// SplitMeetingState atomically replaces one meeting with two parts. The caller
// prepares audio, transcript files and artifact records; either both parts and
// all their state appear in bbolt, or the original meeting stays untouched.
func (s *Store) SplitMeetingState(originalUID string, first, second model.Meeting, artifacts []model.Artifact, speakers []model.Speaker, segments map[string][]model.Segment) error {
	if originalUID == "" || first.UID == "" || second.UID == "" || first.UID == second.UID {
		return errors.New("invalid split meeting state")
	}
	if first.UID == originalUID || second.UID == originalUID {
		return errors.New("split parts must have new uids")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		meetings := tx.Bucket(bMeetings)
		if meetings.Get([]byte(originalUID)) == nil {
			return os.ErrNotExist
		}
		// Remove the original meeting and every record owned by it.
		if err := meetings.Delete([]byte(originalUID)); err != nil {
			return err
		}
		artifactBucket := tx.Bucket(bArtifacts)
		var artifactKeys [][]byte
		if err := artifactBucket.ForEach(func(k, v []byte) error {
			var artifact model.Artifact
			if err := json.Unmarshal(v, &artifact); err != nil {
				return err
			}
			if artifact.MeetingUID == originalUID {
				artifactKeys = append(artifactKeys, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range artifactKeys {
			if err := artifactBucket.Delete(key); err != nil {
				return err
			}
		}
		for _, bucket := range []*bolt.Bucket{tx.Bucket(bJobs), tx.Bucket(bSpeakers)} {
			if err := deletePrefix(bucket, originalUID+"/"); err != nil {
				return err
			}
		}
		if err := tx.Bucket(bSegments).Delete([]byte(originalUID)); err != nil {
			return err
		}
		for _, meeting := range []model.Meeting{first, second} {
			if err := putJSON(meetings, meeting.UID, meeting); err != nil {
				return err
			}
			if err := putJSON(tx.Bucket(bSegments), meeting.UID, segments[meeting.UID]); err != nil {
				return err
			}
		}
		for _, artifact := range artifacts {
			if artifact.UID == "" || (artifact.MeetingUID != first.UID && artifact.MeetingUID != second.UID) {
				return errors.New("invalid split artifact")
			}
			if err := putJSON(artifactBucket, artifact.UID, artifact); err != nil {
				return err
			}
		}
		for _, speaker := range speakers {
			if speaker.ID == "" || (speaker.MeetingUID != first.UID && speaker.MeetingUID != second.UID) {
				return errors.New("invalid split speaker")
			}
			if err := putJSON(tx.Bucket(bSpeakers), speaker.MeetingUID+"/"+speaker.ID, speaker); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveArtifact inserts or replaces an artifact by UID.
func (s *Store) SaveArtifact(a model.Artifact) error {
	if a.UID == "" || a.MeetingUID == "" {
		return errors.New("artifact uid or meeting uid is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bArtifacts), a.UID, a) })
}

// Artifact loads a single artifact by UID.
func (s *Store) Artifact(uid string) (model.Artifact, error) {
	var a model.Artifact
	err := s.db.View(func(tx *bolt.Tx) error {
		var e error
		a, e = getJSON[model.Artifact](tx.Bucket(bArtifacts), uid)
		return e
	})
	return a, err
}

// DeleteArtifact removes an artifact and returns the deleted record.
func (s *Store) DeleteArtifact(uid string) (model.Artifact, error) {
	var artifact model.Artifact
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		artifact, err = getJSON[model.Artifact](tx.Bucket(bArtifacts), uid)
		if err != nil {
			return err
		}
		return tx.Bucket(bArtifacts).Delete([]byte(uid))
	})
	return artifact, err
}

// DeleteArtifactsByPaths removes the artifacts of one meeting whose path
// matches any of the given paths.
func (s *Store) DeleteArtifactsByPaths(meetingUID string, paths ...string) ([]model.Artifact, error) {
	targets := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path != "" {
			targets[filepath.Clean(path)] = struct{}{}
		}
	}
	deleted := make([]model.Artifact, 0)
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bArtifacts)
		var keys [][]byte
		if err := bucket.ForEach(func(key, value []byte) error {
			var artifact model.Artifact
			if err := json.Unmarshal(value, &artifact); err != nil {
				return err
			}
			if artifact.MeetingUID == meetingUID {
				if _, ok := targets[filepath.Clean(artifact.Path)]; ok {
					keys = append(keys, append([]byte(nil), key...))
					deleted = append(deleted, artifact)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range keys {
			if err := bucket.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
	return deleted, err
}

// Artifacts returns the artifacts of a meeting in creation order.
func (s *Store) Artifacts(meetingUID string) ([]model.Artifact, error) {
	out := make([]model.Artifact, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bArtifacts).ForEach(func(_, v []byte) error {
			var a model.Artifact
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			if a.MeetingUID == meetingUID {
				out = append(out, a)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// SaveJob inserts or replaces the job of a meeting stage.
func (s *Store) SaveJob(j model.Job) error {
	key := fmt.Sprintf("%s/%s", j.MeetingUID, j.Stage)
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bJobs), key, j) })
}

// Jobs returns the jobs of a meeting in creation order.
func (s *Store) Jobs(meetingUID string) ([]model.Job, error) {
	out := make([]model.Job, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bJobs).ForEach(func(k, v []byte) error {
			if !strings.HasPrefix(string(k), meetingUID+"/") {
				return nil
			}
			var j model.Job
			if err := json.Unmarshal(v, &j); err != nil {
				return err
			}
			out = append(out, j)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// SaveSpeaker inserts or replaces one speaker.
func (s *Store) SaveSpeaker(sp model.Speaker) error {
	key := fmt.Sprintf("%s/%s", sp.MeetingUID, sp.ID)
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bSpeakers), key, sp) })
}

// Speakers returns the speakers of a meeting ordered by ID.
func (s *Store) Speakers(meetingUID string) ([]model.Speaker, error) {
	out := make([]model.Speaker, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bSpeakers).ForEach(func(k, v []byte) error {
			if !strings.HasPrefix(string(k), meetingUID+"/") {
				return nil
			}
			var sp model.Speaker
			if err := json.Unmarshal(v, &sp); err != nil {
				return err
			}
			out = append(out, sp)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

// Speaker loads a single speaker.
func (s *Store) Speaker(meetingUID, speakerID string) (model.Speaker, error) {
	var sp model.Speaker
	key := meetingUID + "/" + speakerID
	err := s.db.View(func(tx *bolt.Tx) error {
		var e error
		sp, e = getJSON[model.Speaker](tx.Bucket(bSpeakers), key)
		return e
	})
	return sp, err
}

// DeleteSpeaker removes a single speaker.
func (s *Store) DeleteSpeaker(meetingUID, speakerID string) error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bSpeakers).Delete([]byte(meetingUID + "/" + speakerID)) })
}

// DeleteSpeakers removes every speaker of a meeting.
func (s *Store) DeleteSpeakers(meetingUID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return deletePrefix(tx.Bucket(bSpeakers), meetingUID+"/")
	})
}

// ReplaceSpeakers atomically replaces the speaker list of a meeting.
func (s *Store) ReplaceSpeakers(meetingUID string, speakers []model.Speaker) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bSpeakers)
		prefix := meetingUID + "/"
		if err := deletePrefix(bucket, prefix); err != nil {
			return err
		}
		for _, speaker := range speakers {
			if speaker.MeetingUID != meetingUID || speaker.ID == "" {
				return errors.New("invalid speaker state")
			}
			if err := putJSON(bucket, prefix+speaker.ID, speaker); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceSpeakersAndSegments atomically stores transcript segments and the
// matching speaker list.
func (s *Store) ReplaceSpeakersAndSegments(meetingUID string, speakers []model.Speaker, segments []model.Segment) error {
	if meetingUID == "" {
		return errors.New("meeting uid is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := putJSON(tx.Bucket(bSegments), meetingUID, segments); err != nil {
			return err
		}
		bucket := tx.Bucket(bSpeakers)
		prefix := meetingUID + "/"
		if err := deletePrefix(bucket, prefix); err != nil {
			return err
		}
		for _, speaker := range speakers {
			if speaker.MeetingUID != meetingUID || speaker.ID == "" {
				return errors.New("invalid speaker state")
			}
			if err := putJSON(bucket, prefix+speaker.ID, speaker); err != nil {
				return err
			}
		}
		return nil
	})
}

// TranscriptSegments stores the current, user-editable view of the merged
// transcript. Raw transcription and diarization artifacts remain immutable;
// speaker corrections are applied only to this promoted representation.
func (s *Store) TranscriptSegments(meetingUID string) ([]model.Segment, error) {
	var segments []model.Segment
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bSegments).Get([]byte(meetingUID))
		if data == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(data, &segments)
	})
	return segments, err
}

// SaveTranscriptSegments stores the editable transcript layer.
func (s *Store) SaveTranscriptSegments(meetingUID string, segments []model.Segment) error {
	if meetingUID == "" {
		return errors.New("meeting uid is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bSegments), meetingUID, segments)
	})
}

// ReplaceSpeakerState promotes one complete speaker edit atomically in bbolt.
// Either the meeting, segments and all speakers change together, or none do.
func (s *Store) ReplaceSpeakerState(meeting model.Meeting, segments []model.Segment, speakers []model.Speaker) error {
	if meeting.UID == "" {
		return errors.New("meeting uid is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := putJSON(tx.Bucket(bMeetings), meeting.UID, meeting); err != nil {
			return err
		}
		if err := putJSON(tx.Bucket(bSegments), meeting.UID, segments); err != nil {
			return err
		}
		bucket := tx.Bucket(bSpeakers)
		prefix := meeting.UID + "/"
		if err := deletePrefix(bucket, prefix); err != nil {
			return err
		}
		for _, speaker := range speakers {
			if speaker.MeetingUID != meeting.UID || speaker.ID == "" {
				return errors.New("invalid speaker state")
			}
			if err := putJSON(bucket, prefix+speaker.ID, speaker); err != nil {
				return err
			}
		}
		return nil
	})
}

// MergeMeetingState atomically replaces source meetings with one fully built
// meeting state. Files are prepared by the caller before this transaction; the
// returned artifacts identify source files that may be removed afterwards.
func (s *Store) MergeMeetingState(meeting model.Meeting, artifacts []model.Artifact, speakers []model.Speaker, segments []model.Segment, sourceUIDs []string) ([]model.Artifact, error) {
	if meeting.UID == "" || len(sourceUIDs) < 2 {
		return nil, errors.New("invalid meeting merge state")
	}
	sourceSet := make(map[string]struct{}, len(sourceUIDs))
	for _, uid := range sourceUIDs {
		if uid == "" || uid == meeting.UID {
			return nil, errors.New("invalid source meeting uid")
		}
		sourceSet[uid] = struct{}{}
	}
	removed := make([]model.Artifact, 0)
	err := s.db.Update(func(tx *bolt.Tx) error {
		meetings := tx.Bucket(bMeetings)
		for uid := range sourceSet {
			if meetings.Get([]byte(uid)) == nil {
				return os.ErrNotExist
			}
		}
		artifactBucket := tx.Bucket(bArtifacts)
		var artifactKeys [][]byte
		if err := artifactBucket.ForEach(func(key, value []byte) error {
			var artifact model.Artifact
			if err := json.Unmarshal(value, &artifact); err != nil {
				return err
			}
			if _, ok := sourceSet[artifact.MeetingUID]; ok {
				removed = append(removed, artifact)
				artifactKeys = append(artifactKeys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range artifactKeys {
			if err := artifactBucket.Delete(key); err != nil {
				return err
			}
		}
		for uid := range sourceSet {
			if err := meetings.Delete([]byte(uid)); err != nil {
				return err
			}
			if err := deletePrefix(tx.Bucket(bJobs), uid+"/"); err != nil {
				return err
			}
			if err := deletePrefix(tx.Bucket(bSpeakers), uid+"/"); err != nil {
				return err
			}
			if err := tx.Bucket(bSegments).Delete([]byte(uid)); err != nil {
				return err
			}
		}
		if err := putJSON(meetings, meeting.UID, meeting); err != nil {
			return err
		}
		for _, artifact := range artifacts {
			if artifact.UID == "" || artifact.MeetingUID != meeting.UID {
				return errors.New("invalid merged artifact")
			}
			if err := putJSON(artifactBucket, artifact.UID, artifact); err != nil {
				return err
			}
		}
		if err := putJSON(tx.Bucket(bSegments), meeting.UID, segments); err != nil {
			return err
		}
		for _, speaker := range speakers {
			if speaker.ID == "" || speaker.MeetingUID != meeting.UID {
				return errors.New("invalid merged speaker")
			}
			if err := putJSON(tx.Bucket(bSpeakers), meeting.UID+"/"+speaker.ID, speaker); err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}

// SavePerson inserts or replaces a known speaker profile.
func (s *Store) SavePerson(p model.Person) error {
	if p.ID == "" {
		return errors.New("person id is empty")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bPeople), p.ID, p)
	})
}

// Person loads one known speaker profile.
func (s *Store) Person(id string) (model.Person, error) {
	var out model.Person
	err := s.db.View(func(tx *bolt.Tx) error {
		var e error
		out, e = getJSON[model.Person](tx.Bucket(bPeople), id)
		return e
	})
	return out, err
}

// People returns every known speaker ordered by name and ID.
func (s *Store) People() ([]model.Person, error) {
	out := make([]model.Person, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bPeople).ForEach(func(_, v []byte) error {
			var p model.Person
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			out = append(out, p)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Name, out[j].Name) {
			return out[i].ID < out[j].ID
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, err
}

// DeletePerson removes a profile and its labeled samples. Reference clips are
// files owned by the caller and must be removed separately.
func (s *Store) DeletePerson(id string) error {
	if id == "" {
		return os.ErrNotExist
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bPeople).Delete([]byte(id)); err != nil {
			return err
		}
		return deletePrefix(tx.Bucket(bVoice), id+"/")
	})
}

// DeletePeople removes every known speaker profile and all samples.
func (s *Store) DeletePeople() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := deletePrefix(tx.Bucket(bPeople), ""); err != nil {
			return err
		}
		return deletePrefix(tx.Bucket(bVoice), "")
	})
}

// SaveVoiceSample inserts or replaces one enrolled voice sample.
func (s *Store) SaveVoiceSample(v model.VoiceSample) error {
	if v.PersonID == "" || v.ID == "" {
		return errors.New("voice sample needs person and sample ids")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bVoice), v.PersonID+"/"+v.ID, v)
	})
}

// VoiceSamples returns the samples of one person, oldest first.
func (s *Store) VoiceSamples(personID string) ([]model.VoiceSample, error) {
	out := make([]model.VoiceSample, 0)
	if personID == "" {
		return out, nil
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bVoice).ForEach(func(k, v []byte) error {
			if !strings.HasPrefix(string(k), personID+"/") {
				return nil
			}
			var sample model.VoiceSample
			if err := json.Unmarshal(v, &sample); err != nil {
				return err
			}
			out = append(out, sample)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// AllVoiceSamples returns every enrolled sample. It is used by matching and by
// the people list, so a single pass over the bucket is enough.
func (s *Store) AllVoiceSamples() ([]model.VoiceSample, error) {
	out := make([]model.VoiceSample, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bVoice).ForEach(func(_, v []byte) error {
			var sample model.VoiceSample
			if err := json.Unmarshal(v, &sample); err != nil {
				return err
			}
			out = append(out, sample)
			return nil
		})
	})
	return out, err
}

// DeleteVoiceSample removes one sample by person and sample ID.
func (s *Store) DeleteVoiceSample(personID, sampleID string) error {
	if personID == "" || sampleID == "" {
		return os.ErrNotExist
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bVoice)
		key := []byte(personID + "/" + sampleID)
		if bucket.Get(key) == nil {
			return os.ErrNotExist
		}
		return bucket.Delete(key)
	})
}

// DeleteVoiceSamplesForPerson removes every sample of one person.
func (s *Store) DeleteVoiceSamplesForPerson(personID string) error {
	if personID == "" {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return deletePrefix(tx.Bucket(bVoice), personID+"/")
	})
}

// UnlinkPerson clears profile links in meeting speaker cards when a profile is
// deleted. Display names stay as historical records.
func (s *Store) UnlinkPerson(personID string) error {
	if personID == "" {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bSpeakers)
		type update struct {
			key     []byte
			speaker model.Speaker
		}
		var updates []update
		if err := bucket.ForEach(func(k, v []byte) error {
			var speaker model.Speaker
			if err := json.Unmarshal(v, &speaker); err != nil {
				return err
			}
			changed := false
			if speaker.PersonID == personID {
				speaker.PersonID = ""
				speaker.MatchScore = 0
				speaker.NameSource = ""
				changed = true
			}
			// Suggestions may point at a deleted profile without being linked.
			if len(speaker.MatchCandidates) > 0 {
				filtered := speaker.MatchCandidates[:0]
				for _, candidate := range speaker.MatchCandidates {
					if candidate.PersonID != personID {
						filtered = append(filtered, candidate)
					}
				}
				if len(filtered) != len(speaker.MatchCandidates) {
					speaker.MatchCandidates = filtered
					changed = true
				}
			}
			if !changed {
				return nil
			}
			updates = append(updates, update{key: append([]byte(nil), k...), speaker: speaker})
			return nil
		}); err != nil {
			return err
		}
		for _, item := range updates {
			if err := putJSON(bucket, string(item.key), item.speaker); err != nil {
				return err
			}
		}
		return nil
	})
}
