package hostagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/malmo/malmo/internal/protocol"
)

// --- stub verifier ---

type stubVerifier struct {
	valid bool
	err   error
}

func (s *stubVerifier) Verify(_, _ string) (bool, error) {
	return s.valid, s.err
}

// --- helpers ---

func newTestAgent(v PasswordVerifier) (*Agent, *http.ServeMux) {
	a := New(v, NewFakePublisher(".malmo.local"))
	mux := http.NewServeMux()
	a.Mount(mux)
	return a, mux
}

func post(t *testing.T, mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(w.Body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

// --- verify-password tests ---

func TestVerifyPasswordDelegatesToVerifier_HappyPath(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{valid: true})
	w := post(t, mux, "/v1/auth/verify-password", protocol.VerifyPasswordRequest{
		User: "alice", Password: "correct",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	resp := decodeBody[protocol.VerifyPasswordResponse](t, w)
	if !resp.Valid {
		t.Error("want valid=true")
	}
}

func TestVerifyPasswordDelegatesToVerifier_WrongCredentials(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{valid: false})
	w := post(t, mux, "/v1/auth/verify-password", protocol.VerifyPasswordRequest{
		User: "alice", Password: "wrong",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	resp := decodeBody[protocol.VerifyPasswordResponse](t, w)
	if resp.Valid {
		t.Error("want valid=false")
	}
}

// TestVerifyPasswordVerifierError checks that a transport/config error from the
// verifier returns {valid: false} (not 5xx) — per BRAIN_HOST_PROTOCOL.md:
// "never reveal why verification failed."
func TestVerifyPasswordVerifierError_ReturnsValidFalseNotFiveHundred(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{valid: false, err: errors.New("pam config broken")})
	w := post(t, mux, "/v1/auth/verify-password", protocol.VerifyPasswordRequest{
		User: "alice", Password: "anything",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (not 5xx), got %d", w.Code)
	}
	resp := decodeBody[protocol.VerifyPasswordResponse](t, w)
	if resp.Valid {
		t.Error("want valid=false on verifier error")
	}
}

// --- set-password / fake-map tests ---

func TestSetPasswordAndVerifyWithFakeVerifier(t *testing.T) {
	a := New(nil, NewFakePublisher(".malmo.local")) // verifier set after construction
	a.Verifier = NewFakeVerifier(a)
	mux := http.NewServeMux()
	a.Mount(mux)

	// Set a password.
	w := post(t, mux, "/v1/auth/set-password", protocol.SetPasswordRequest{
		User: "bob", Password: "s3cret",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("set-password: want 200, got %d", w.Code)
	}

	// Correct password → valid.
	w = post(t, mux, "/v1/auth/verify-password", protocol.VerifyPasswordRequest{
		User: "bob", Password: "s3cret",
	})
	resp := decodeBody[protocol.VerifyPasswordResponse](t, w)
	if !resp.Valid {
		t.Error("want valid=true after set-password")
	}

	// Wrong password → not valid.
	w = post(t, mux, "/v1/auth/verify-password", protocol.VerifyPasswordRequest{
		User: "bob", Password: "wrong",
	})
	resp = decodeBody[protocol.VerifyPasswordResponse](t, w)
	if resp.Valid {
		t.Error("want valid=false for wrong password")
	}
}

func TestSetPasswordMissingFields(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{})
	w := post(t, mux, "/v1/auth/set-password", protocol.SetPasswordRequest{
		User: "", Password: "",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// --- set-role tests ---

func TestSetRole_HappyPath(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{})
	w := post(t, mux, "/v1/auth/set-role", protocol.SetRoleRequest{
		User: "carol", Role: "admin",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestSetRole_InvalidRole(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{})
	w := post(t, mux, "/v1/auth/set-role", protocol.SetRoleRequest{
		User: "carol", Role: "superuser",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// --- delete-user tests ---

func TestDeleteUser_RemovesFromFakeMap(t *testing.T) {
	a := New(nil, NewFakePublisher(".malmo.local"))
	a.Verifier = NewFakeVerifier(a)
	mux := http.NewServeMux()
	a.Mount(mux)

	// Seed a user.
	post(t, mux, "/v1/auth/set-password", protocol.SetPasswordRequest{
		User: "dave", Password: "pw",
	})

	// Delete.
	w := post(t, mux, "/v1/auth/delete-user", protocol.DeleteUserRequest{User: "dave"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	// Verify no longer valid.
	w = post(t, mux, "/v1/auth/verify-password", protocol.VerifyPasswordRequest{
		User: "dave", Password: "pw",
	})
	resp := decodeBody[protocol.VerifyPasswordResponse](t, w)
	if resp.Valid {
		t.Error("want valid=false after delete-user")
	}
}

func TestDeleteUser_IdempotentOnUnknownUser(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{})
	w := post(t, mux, "/v1/auth/delete-user", protocol.DeleteUserRequest{User: "nobody"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (idempotent), got %d", w.Code)
	}
}

// --- stub publisher ---

type stubPublisher struct {
	publishErr   error
	unpublishErr error
	published    []string
}

func (s *stubPublisher) Publish(slug string) (string, error) {
	if s.publishErr != nil {
		return "", s.publishErr
	}
	s.published = append(s.published, slug)
	return slug + ".malmo.local", nil
}

func (s *stubPublisher) Unpublish(slug string) error {
	return s.unpublishErr
}

// --- discovery / system tests ---

func TestSystemStatus_ReturnsOK(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{})
	w := get(t, mux, "/v1/system/status")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var s protocol.SystemStatus
	if err := json.NewDecoder(w.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Hostname == "" {
		t.Error("want non-empty hostname")
	}
}

func TestPublishUnpublish_RoundTrip(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{})

	w := post(t, mux, "/v1/discovery/publish", protocol.PublishRequest{Slug: "whoami"})
	if w.Code != http.StatusOK {
		t.Fatalf("publish: want 200, got %d", w.Code)
	}
	pr := decodeBody[protocol.PublishResponse](t, w)
	if pr.Name != "whoami.malmo.local" {
		t.Errorf("want whoami.malmo.local, got %q", pr.Name)
	}

	w = post(t, mux, "/v1/discovery/unpublish", protocol.UnpublishRequest{Slug: "whoami"})
	if w.Code != http.StatusOK {
		t.Fatalf("unpublish: want 200, got %d", w.Code)
	}
}

// TestPublish_DelegatesToPublisher verifies that the publish handler calls the
// injected Publisher and surfaces errors as 500.
func TestPublish_DelegatesToPublisher(t *testing.T) {
	pub := &stubPublisher{}
	a := New(&stubVerifier{}, pub)
	mux := http.NewServeMux()
	a.Mount(mux)

	w := post(t, mux, "/v1/discovery/publish", protocol.PublishRequest{Slug: "photos"})
	if w.Code != http.StatusOK {
		t.Fatalf("publish: want 200, got %d", w.Code)
	}
	pr := decodeBody[protocol.PublishResponse](t, w)
	if pr.Name != "photos.malmo.local" {
		t.Errorf("name: want photos.malmo.local, got %q", pr.Name)
	}
	if pr.State != "established" {
		t.Errorf("state: want established, got %q", pr.State)
	}
	if len(pub.published) != 1 || pub.published[0] != "photos" {
		t.Errorf("publisher not called with slug: %v", pub.published)
	}
}

// TestPublish_PublisherError_Returns500 verifies that a Publisher failure
// returns 500 rather than silently succeeding.
func TestPublish_PublisherError_Returns500(t *testing.T) {
	pub := &stubPublisher{publishErr: errors.New("disk full")}
	a := New(&stubVerifier{}, pub)
	mux := http.NewServeMux()
	a.Mount(mux)

	w := post(t, mux, "/v1/discovery/publish", protocol.PublishRequest{Slug: "notes"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("publish with broken publisher: want 500, got %d", w.Code)
	}
}

// TestFakePublisher_MatchesCurrentBehavior verifies FakePublisher returns the
// expected name and doesn't error on valid slugs or Unpublish.
func TestFakePublisher_MatchesCurrentBehavior(t *testing.T) {
	fp := NewFakePublisher(".malmo.local")

	name, err := fp.Publish("whoami")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if name != "whoami.malmo.local" {
		t.Errorf("name: want whoami.malmo.local, got %q", name)
	}

	if err := fp.Unpublish("whoami"); err != nil {
		t.Fatalf("Unpublish: %v", err)
	}
}
