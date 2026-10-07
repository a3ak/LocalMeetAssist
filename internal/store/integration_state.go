package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// integrationRecordingKey holds the recording revision together with the
// activity value it belongs to, so a restart can tell whether the previous
// process was recording when it disappeared.
var integrationRecordingKey = []byte("recording")

const commandPrefix = "cmd:"

type integrationRecordingState struct {
	Revision int64 `json:"revision"`
	Active   bool  `json:"active"`
}

type commandCacheEntry struct {
	Result string    `json:"result"`
	At     time.Time `json:"at"`
}

// RecordingState returns the persisted revision and the activity value it was
// stored with. A missing record means "never recorded".
func (s *Store) RecordingState() (int64, bool, error) {
	var revision int64
	var active bool
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bIntegration).Get(integrationRecordingKey)
		if raw == nil {
			return nil
		}
		var stored integrationRecordingState
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil // a damaged record is treated as absent
		}
		revision, active = stored.Revision, stored.Active
		return nil
	})
	return revision, active, err
}

// SaveRecordingState persists the revision together with its activity value.
func (s *Store) SaveRecordingState(revision int64, active bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		raw, err := json.Marshal(integrationRecordingState{Revision: revision, Active: active})
		if err != nil {
			return err
		}
		return tx.Bucket(bIntegration).Put(integrationRecordingKey, raw)
	})
}

// CommandResult returns the cached result of an integration command. Entries
// older than ttl are treated as absent, so a repeated command_id never runs the
// action twice.
func (s *Store) CommandResult(clientID, commandID string, now time.Time, ttl time.Duration) (string, bool, error) {
	var result string
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bIntegration).Get(commandKey(clientID, commandID))
		if raw == nil {
			return nil
		}
		var entry commandCacheEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil
		}
		if now.Sub(entry.At) > ttl {
			return nil
		}
		result, found = entry.Result, true
		return nil
	})
	return result, found, err
}

// SaveCommandResult caches a command result and prunes entries that fell out of
// the retention window.
func (s *Store) SaveCommandResult(clientID, commandID, result string, at time.Time, ttl time.Duration) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bIntegration)
		raw, err := json.Marshal(commandCacheEntry{Result: result, At: at})
		if err != nil {
			return err
		}
		if err := b.Put(commandKey(clientID, commandID), raw); err != nil {
			return err
		}
		// bbolt keys are only valid inside the transaction, so collect copies
		// before deleting anything.
		var stale [][]byte
		_ = b.ForEach(func(k, v []byte) error {
			if !strings.HasPrefix(string(k), commandPrefix) {
				return nil
			}
			var entry commandCacheEntry
			if json.Unmarshal(v, &entry) != nil || at.Sub(entry.At) > ttl {
				stale = append(stale, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range stale {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// commandKey keeps the (client_id, command_id) pair unambiguous: client ids may
// contain any character, so the client part is length-prefixed.
func commandKey(clientID, commandID string) []byte {
	var builder strings.Builder
	builder.WriteString(commandPrefix)
	builder.WriteString(strconv.Itoa(len(clientID)))
	builder.WriteByte(':')
	builder.WriteString(clientID)
	builder.WriteString(commandID)
	return []byte(builder.String())
}
