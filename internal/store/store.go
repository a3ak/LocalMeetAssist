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
	bMeta      = []byte("meta")
	bMeetings  = []byte("meetings")
	bArtifacts = []byte("artifacts")
	bJobs      = []byte("jobs")
	bSpeakers  = []byte("speakers")
	bSegments  = []byte("transcript_segments")
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
		for _, name := range [][]byte{bMeta, bMeetings, bArtifacts, bJobs, bSpeakers, bSegments} {
			if _, e := tx.CreateBucketIfNotExists(name); e != nil {
				return e
			}
		}
		return tx.Bucket(bMeta).Put([]byte("schema_version"), []byte("2"))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

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
