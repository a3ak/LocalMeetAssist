package desktop

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"localmeetassist/internal/config"
)

// TestRequestSendsTokenForReads guards the auth rule where every API path
// requires the session token, reads included. The tray used to send the token
// only for mutating requests, which broke once GET became authenticated.
func TestRequestSendsTokenForReads(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Meeting-Token")
		if gotToken != "secret-token" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	m := New(
		server.URL,
		"secret-token",
		func() error { return nil },
		func() config.Config { return config.Defaults() },
		log.New(io.Discard, "", 0),
	)
	if err := m.request(http.MethodGet, "/api/v1/meetings", nil, nil); err != nil {
		t.Fatalf("read request must be authorized: %v (token sent: %q)", err, gotToken)
	}
	if gotToken != "secret-token" {
		t.Fatalf("read request did not send the token, got %q", gotToken)
	}
}
