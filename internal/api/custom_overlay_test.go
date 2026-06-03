package api

import (
	"net/http"
	"strings"
	"testing"
)

type overlayResp struct {
	Overlay string `json:"overlay"`
}

type permsResp struct {
	Permissions struct {
		Internet bool `json:"internet"`
		LAN      bool `json:"lan"`
		GPU      bool `json:"gpu"`
		Folders  []struct {
			Folder string `json:"folder"`
			Mode   string `json:"mode"`
			Target string `json:"target"`
		} `json:"folders"`
		Devices []string `json:"devices"`
	} `json:"permissions"`
}

func TestRenderCustomOverlay(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")

	resp := h.do("POST", "/api/v1/apps/custom/overlay/render", map[string]any{
		"permissions": map[string]any{
			"internet": true,
			"gpu":      true,
			"folders": []map[string]any{
				{"folder": "photos", "mode": "write", "target": "/photoprism/originals"},
			},
		},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("render = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[overlayResp](t, resp)
	for _, want := range []string{"permissions:", "internet: true", "gpu: true", "/photoprism/originals"} {
		if !strings.Contains(body.Overlay, want) {
			t.Fatalf("overlay missing %q:\n%s", want, body.Overlay)
		}
	}
}

func TestParseCustomOverlay(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")

	// Includes devices — the field the form omits, reachable only via YAML.
	overlay := "permissions:\n  internet: false\n  gpu: true\n  devices: [/dev/dri]\n  folders:\n    - { folder: photos, mode: write, target: /photoprism/originals }\n"
	resp := h.do("POST", "/api/v1/apps/custom/overlay/parse", map[string]any{"overlay": overlay})
	if resp.StatusCode != 200 {
		t.Fatalf("parse = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[permsResp](t, resp)
	p := body.Permissions
	if p.Internet || !p.GPU || len(p.Devices) != 1 || len(p.Folders) != 1 {
		t.Fatalf("parsed perms wrong: %+v", p)
	}
	if f := p.Folders[0]; f.Folder != "photos" || f.Mode != "write" || f.Target != "/photoprism/originals" {
		t.Fatalf("parsed folder wrong: %+v", f)
	}
}

func TestParseCustomOverlayRejectsBadTarget(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")

	// A relative target is the same rejection the form path gets — coached inline.
	overlay := "permissions:\n  folders:\n    - { folder: photos, target: rel/path }\n"
	resp := h.do("POST", "/api/v1/apps/custom/overlay/parse", map[string]any{"overlay": overlay})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad-target parse = %d, want 422", resp.StatusCode)
	}
}

func TestCustomOverlayAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "hunter2")
	h.addMember("u_bob001", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	cases := []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/apps/custom/overlay/render", map[string]any{"permissions": map[string]any{}}},
		{"/api/v1/apps/custom/overlay/parse", map[string]any{"overlay": "permissions: {}"}},
	}
	for _, c := range cases {
		resp := h.do("POST", c.path, c.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("member %s = %d, want 403 (Door 2 is admin-only)", c.path, resp.StatusCode)
		}
	}
}
