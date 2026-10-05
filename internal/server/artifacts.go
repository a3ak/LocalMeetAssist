package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"localmeetassist/internal/model"
	"localmeetassist/internal/uuidv7"
)

// artifacts lists the artifacts of a meeting or imports a new file.
func (s *Server) artifacts(w http.ResponseWriter, r *http.Request, uid string) {
	if r.Method == http.MethodGet {
		a, err := s.liveArtifacts(uid)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, a)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	file, h, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "file is required")
		return
	}
	defer file.Close()
	m, err := s.store.Meeting(uid)
	if err != nil {
		writeError(w, 404, "meeting not found")
		return
	}
	kind := r.FormValue("type")
	if kind == "" {
		kind = "imported"
	}
	if _, isAudioSource := audioImportTypes[kind]; isAudioSource {
		artifact, importErr := s.importAudio(m, file, kind)
		if importErr != nil {
			writeError(w, http.StatusBadRequest, importErr.Error())
			return
		}
		writeJSON(w, http.StatusCreated, artifact)
		return
	}
	dest := filepath.Join(s.meetingDir(m), "imports", sanitizeName(h.Filename))
	if err := saveMultipart(file, dest); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	a, err := s.makeArtifact(uid, kind, dest, h.Header.Get("Content-Type"), "imported")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, a)
}

// liveArtifacts returns stored artifacts and prunes records whose file is gone.
func (s *Server) liveArtifacts(meetingUID string) ([]model.Artifact, error) {
	artifacts, err := s.store.Artifacts(meetingUID)
	if err != nil {
		return nil, err
	}
	live := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		_, statErr := os.Stat(artifact.Path)
		if errors.Is(statErr, os.ErrNotExist) && s.safeManagedPath(artifact.Path) {
			_, _ = s.store.DeleteArtifact(artifact.UID)
			s.logger.Printf("stale artifact pruned meeting_uid=%s artifact_uid=%s path=%q", meetingUID, artifact.UID, artifact.Path)
			continue
		}
		live = append(live, artifact)
	}
	return live, nil
}

// artifactDownload serves or deletes a single artifact.
func (s *Server) artifactDownload(w http.ResponseWriter, r *http.Request, meetingUID, artifactUID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	a, err := s.store.Artifact(artifactUID)
	if err != nil || a.MeetingUID != meetingUID || !s.safeManagedPath(a.Path) {
		writeError(w, 404, "artifact not found")
		return
	}
	if r.Method == http.MethodDelete {
		meeting, meetingErr := s.store.Meeting(meetingUID)
		if meetingErr != nil {
			writeError(w, 404, "meeting not found")
			return
		}
		if meeting.Status == "recording" || meeting.Status == "processing" || s.pipeline.IsRunning(meetingUID) {
			writeError(w, http.StatusConflict, "cannot delete an artifact while recording or processing")
			return
		}
		if removeErr := os.Remove(a.Path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			writeError(w, 500, removeErr.Error())
			return
		}
		if _, deleteErr := s.store.DeleteArtifact(artifactUID); deleteErr != nil {
			writeError(w, 500, deleteErr.Error())
			return
		}
		s.logger.Printf("artifact deleted meeting_uid=%s artifact_uid=%s type=%s path=%q", meetingUID, artifactUID, a.Type, a.Path)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	contentType := a.MIMEType
	if (strings.HasPrefix(contentType, "text/") || contentType == "application/json") && !strings.Contains(strings.ToLower(contentType), "charset=") {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(a.Path)))
	http.ServeFile(w, r, a.Path)
}

// makeArtifact registers a file and computes its size and SHA-256.
func (s *Server) makeArtifact(uid, kind, path, mime, source string) (model.Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return model.Artifact{}, err
	}
	a := model.Artifact{UID: uuidv7.New(), MeetingUID: uid, Type: kind, Path: path, MIMEType: mime, SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil)), Source: source, CreatedAt: time.Now()}
	_, _ = s.store.DeleteArtifactsByPaths(uid, path)
	return a, s.store.SaveArtifact(a)
}

// saveMultipart writes an upload to dest enforcing the size limit.
func saveMultipart(src multipart.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	const maxUpload = int64(256 << 20)
	written, err := io.Copy(f, io.LimitReader(src, maxUpload+1))
	if err != nil {
		return err
	}
	if written > maxUpload {
		return errors.New("file exceeds 256 MiB upload limit")
	}
	return nil
}

// sanitizeName makes an uploaded file name safe for the filesystem.
func sanitizeName(v string) string {
	v = filepath.Base(v)
	var b strings.Builder
	for _, r := range v {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 127 {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "artifact.bin"
	}
	return b.String()
}
