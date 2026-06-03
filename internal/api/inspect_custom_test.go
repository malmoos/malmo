package api

import (
	"net/http"
	"testing"
)

type inspectResp struct {
	Services []string `json:"services"`
	MainPort int      `json:"main_port"`
}

func TestInspectCustomSingleService(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")

	resp := h.do("POST", "/api/v1/apps/custom/inspect", map[string]any{
		"compose": "services:\n  web:\n    image: nginx\n    expose: [\"8080\"]\n",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("inspect = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[inspectResp](t, resp)
	if len(body.Services) != 1 || body.Services[0] != "web" {
		t.Fatalf("services = %v, want [web]", body.Services)
	}
	// Single service → port prefilled from expose: without the form picking one.
	if body.MainPort != 8080 {
		t.Fatalf("main_port = %d, want 8080", body.MainPort)
	}
}

func TestInspectCustomMultiServiceNeedsServicePick(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")

	compose := "services:\n  web:\n    image: nginx\n    expose: [\"80\"]\n  db:\n    image: postgres\n    expose: [\"5432\"]\n"

	// No service chosen yet: the form forces a dropdown, so no port is inferred.
	resp := h.do("POST", "/api/v1/apps/custom/inspect", map[string]any{"compose": compose})
	if resp.StatusCode != 200 {
		t.Fatalf("inspect = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[inspectResp](t, resp)
	if len(body.Services) != 2 {
		t.Fatalf("services = %v, want both web and db", body.Services)
	}
	if body.MainPort != 0 {
		t.Fatalf("main_port = %d, want 0 (ambiguous until a service is picked)", body.MainPort)
	}

	// Once a service is picked, its own expose: prefills the port.
	resp = h.do("POST", "/api/v1/apps/custom/inspect", map[string]any{
		"compose": compose, "main_service": "db",
	})
	body = decodeJSON[inspectResp](t, resp)
	if body.MainPort != 5432 {
		t.Fatalf("main_port after picking db = %d, want 5432", body.MainPort)
	}
}

func TestInspectCustomAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")
	h.addMember("u_bob001", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("POST", "/api/v1/apps/custom/inspect", map[string]any{
		"compose": "services: {web: {image: nginx}}",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member inspect = %d, want 403 (Door 2 is admin-only)", resp.StatusCode)
	}
}

func TestInspectCustomRejectsEmptyCompose(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")

	resp := h.do("POST", "/api/v1/apps/custom/inspect", map[string]any{
		"compose": "services: {}",
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no-services inspect = %d, want 422", resp.StatusCode)
	}
}
