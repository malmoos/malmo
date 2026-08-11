package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/malmoos/malmo/internal/audit"
	"github.com/malmoos/malmo/internal/hostclient"
	"github.com/malmoos/malmo/internal/protocol"
	"github.com/malmoos/malmo/internal/store"
)

// updateHarness is a brain Server wired to a canned host-agent job surface. It
// is deliberately small: these tests are about the brain's gate, its mapping of
// host answers onto status codes, and its audit trail — the transaction itself
// is covered in internal/hostagent/cpupdate.
type updateHarness struct {
	srv *Server
	st  *store.Store
	// requests records the bodies host-agent received.
	requests []protocol.SystemUpdateRequest
	// startStatus, when non-zero, is the status the fake start route answers.
	startStatus int
}

func newUpdateHarness(t *testing.T) *updateHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedUpdateUsers(t, st)

	h := &updateHarness{st: st}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs/system-update", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SystemUpdateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		h.requests = append(h.requests, req)
		if h.startStatus != 0 {
			w.WriteHeader(h.startStatus)
			_ = json.NewEncoder(w).Encode(protocol.Error{Code: "job-running", Message: "busy"})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(protocol.Job{
			ID: "j_abc123", Kind: protocol.JobKindSystemUpdate, Status: protocol.JobStatusRunning,
			StartedAt: "2026-08-11T10:00:00Z",
		})
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "j_abc123" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.Error{Code: "unknown-job", Message: "no such job"})
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.Job{
			ID: "j_abc123", Kind: protocol.JobKindSystemUpdate, Status: protocol.JobStatusFailed,
			StartedAt: "2026-08-11T10:00:00Z", FinishedAt: "2026-08-11T10:01:00Z",
			Error: &protocol.Error{Code: protocol.JobErrorFailed, Message: "brain health check"},
			Result: &protocol.SystemUpdateResult{
				BrainChanged: true, Reverted: true, FailureMode: "health",
			},
		})
	})
	hostHTTP := &http.Server{Handler: mux}
	go func() { _ = hostHTTP.Serve(ln) }()
	t.Cleanup(func() { _ = hostHTTP.Close() })

	h.srv = &Server{host: hostclient.New(sock), auditor: audit.New(st)}
	return h
}

// seedUpdateUsers inserts the two principals the tests act as. The audit table
// has a foreign key on actor_user_id, so an unseeded actor makes every audit
// insert fail silently — and a test asserting "one row" would then be asserting
// the FK, not the handler.
func seedUpdateUsers(t *testing.T, st *store.Store) {
	t.Helper()
	for _, u := range []store.User{
		{ID: "u_admin", Username: "alice", Role: store.RoleAdmin, CreatedAt: time.Now()},
		{ID: "u_bob", Username: "bob", Role: store.RoleMember, CreatedAt: time.Now()},
	} {
		if err := st.CreateUser(u); err != nil {
			t.Fatalf("seed %s: %v", u.ID, err)
		}
	}
}

func (h *updateHarness) start(ctx context.Context, brain, ui string) (*struct{ Body SystemUpdateJobDTO }, error) {
	return h.srv.startSystemUpdate(ctx, &struct {
		Body SystemUpdateRequestDTO
	}{Body: SystemUpdateRequestDTO{BrainImage: brain, UIImage: ui}})
}

// auditRows returns every system.update row, newest first.
func (h *updateHarness) auditRows(t *testing.T) []store.AuditEvent {
	t.Helper()
	all, err := h.st.ListAuditEvents(store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	var out []store.AuditEvent
	for _, e := range all {
		if e.Action == audit.ActionSystemUpdate {
			out = append(out, e)
		}
	}
	return out
}

// TestStartUpdate_Accepted: the happy path. The refs reach host-agent, the job
// id comes back, and the start is audited as a success.
func TestStartUpdate_Accepted(t *testing.T) {
	h := newUpdateHarness(t)
	out, err := h.start(adminCtx("u_admin"), "brain:v2", "ui:v2")
	if err != nil {
		t.Fatalf("startSystemUpdate: %v", err)
	}
	if out.Body.JobID != "j_abc123" || out.Body.Status != protocol.JobStatusRunning {
		t.Fatalf("body = %+v", out.Body)
	}
	if len(h.requests) != 1 || h.requests[0].BrainImage != "brain:v2" || h.requests[0].UIImage != "ui:v2" {
		t.Fatalf("host saw %+v", h.requests)
	}
	rows := h.auditRows(t)
	if len(rows) != 1 || !rows[0].Success || rows[0].ActorUserID != "u_admin" {
		t.Fatalf("audit rows = %+v, want one successful start by the admin", rows)
	}
}

// TestStartUpdate_MemberForbidden: a member cannot move the box's control
// plane, and the attempt is recorded. "Did someone unauthorized try to update
// this box" is exactly what the Activity view is for.
func TestStartUpdate_MemberForbidden(t *testing.T) {
	h := newUpdateHarness(t)
	_, err := h.start(memberCtx("u_bob"), "brain:v2", "")
	assertStatus(t, err, http.StatusForbidden)
	if len(h.requests) != 0 {
		t.Fatalf("a member's update reached host-agent: %+v", h.requests)
	}
	rows := h.auditRows(t)
	if len(rows) != 1 || rows[0].Success || rows[0].ActorUserID != "u_bob" {
		t.Fatalf("audit rows = %+v, want one failed attempt by the member", rows)
	}
}

// TestStartUpdate_NoRefs_422: an update with no target is refused before it
// reaches host-agent. It does **not** audit: CLAUDE.md exempts validation 422s,
// and an admin sending a malformed body is not the question the audit trail
// answers.
func TestStartUpdate_NoRefs_422(t *testing.T) {
	h := newUpdateHarness(t)
	_, err := h.start(adminCtx("u_admin"), "  ", "")
	assertStatus(t, err, http.StatusUnprocessableEntity)
	if len(h.requests) != 0 {
		t.Fatalf("an empty update reached host-agent: %+v", h.requests)
	}
	if rows := h.auditRows(t); len(rows) != 0 {
		t.Fatalf("a validation 422 audited: %+v", rows)
	}
}

// TestStartUpdate_ControlCharRef_422: a ref with a newline in it is a mistake
// in every case, and it must not reach the box's declaration.
func TestStartUpdate_ControlCharRef_422(t *testing.T) {
	h := newUpdateHarness(t)
	_, err := h.start(adminCtx("u_admin"), "brain:v2", "ui:v2\n    image: evil")
	assertStatus(t, err, http.StatusUnprocessableEntity)
	if len(h.requests) != 0 {
		t.Fatalf("a malformed ref reached host-agent: %+v", h.requests)
	}
	if rows := h.auditRows(t); len(rows) != 0 {
		t.Fatalf("a validation 422 audited: %+v", rows)
	}
}

// TestStartUpdate_Conflict_409: host-agent already has one running. The admin
// gets a 409 they can understand, not a generic bad-gateway, and the refusal is
// audited.
func TestStartUpdate_Conflict_409(t *testing.T) {
	h := newUpdateHarness(t)
	h.startStatus = http.StatusConflict
	_, err := h.start(adminCtx("u_admin"), "brain:v2", "")
	assertStatus(t, err, http.StatusConflict)
	if rows := h.auditRows(t); len(rows) != 1 || rows[0].Success {
		t.Fatalf("audit rows = %+v, want one failed attempt", rows)
	}
}

// TestStartUpdate_HostDown_502: host-agent unreachable. The admin is told the
// update did not start, and the attempt is on the record.
func TestStartUpdate_HostDown_502(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	seedUpdateUsers(t, st)
	s := &Server{host: hostclient.New(filepath.Join(t.TempDir(), "absent.sock")), auditor: audit.New(st)}

	_, err = s.startSystemUpdate(adminCtx("u_admin"), &struct {
		Body SystemUpdateRequestDTO
	}{Body: SystemUpdateRequestDTO{BrainImage: "brain:v2"}})
	assertStatus(t, err, http.StatusBadGateway)

	rows, listErr := st.ListAuditEvents(store.AuditFilter{Limit: 50})
	if listErr != nil {
		t.Fatalf("ListAuditEvents: %v", listErr)
	}
	if len(rows) != 1 || rows[0].Action != audit.ActionSystemUpdate || rows[0].Success {
		t.Fatalf("audit rows = %+v, want one failed start", rows)
	}
}

// TestGetUpdate_ReportsFailureDetail: the status read carries the failure mode
// and the revert flag through to the admin. "It broke at the health check and
// the box was put back" is the answer the dashboard has to be able to show.
func TestGetUpdate_ReportsFailureDetail(t *testing.T) {
	h := newUpdateHarness(t)
	out, err := h.srv.getSystemUpdate(adminCtx("u_admin"), &struct {
		JobID string `path:"job_id"`
	}{JobID: "j_abc123"})
	if err != nil {
		t.Fatalf("getSystemUpdate: %v", err)
	}
	if out.Body.Status != protocol.JobStatusFailed || out.Body.Error == nil {
		t.Fatalf("body = %+v", out.Body)
	}
	if out.Body.Result == nil || out.Body.Result.FailureMode != "health" || !out.Body.Result.Reverted {
		t.Fatalf("failure detail lost: %+v", out.Body.Result)
	}
	if out.Body.FinishedAt == "" {
		t.Error("finished_at lost")
	}
	// A pure read: nothing to audit.
	if rows := h.auditRows(t); len(rows) != 0 {
		t.Errorf("a status read audited: %+v", rows)
	}
}

// TestGetUpdate_Unknown_404 / _MemberForbidden: the two gates on the read.
func TestGetUpdate_Unknown_404(t *testing.T) {
	h := newUpdateHarness(t)
	_, err := h.srv.getSystemUpdate(adminCtx("u_admin"), &struct {
		JobID string `path:"job_id"`
	}{JobID: "j_nope"})
	assertStatus(t, err, http.StatusNotFound)
}

func TestGetUpdate_MemberForbidden(t *testing.T) {
	h := newUpdateHarness(t)
	_, err := h.srv.getSystemUpdate(memberCtx("u_bob"), &struct {
		JobID string `path:"job_id"`
	}{JobID: "j_abc123"})
	assertStatus(t, err, http.StatusForbidden)
}
