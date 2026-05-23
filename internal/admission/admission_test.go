package admission

import (
	"context"
	"strings"
	"testing"
)

// TestCheckStructure is the table-driven core of the admission policy
// (APP_LIFECYCLE.md). Each row carries a compose snippet + the substrings the
// rejection message must contain (service name, field). validateSyntax is
// skipped here so the test stays hermetic — Check (with daemon) is exercised
// via integration lanes.
func TestCheckStructure(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
		wantSub []string // substrings the error message must contain
	}{
		{
			name: "happy",
			yaml: `
services:
  web:
    image: nginx:1.27
`,
			wantErr: false,
		},
		{
			name: "ports rejected",
			yaml: `
services:
  web:
    image: nginx
    ports: ["8080:80"]
`,
			wantErr: true, wantSub: []string{`"web"`, "ports"},
		},
		{
			name: "privileged rejected",
			yaml: `
services:
  web:
    image: nginx
    privileged: true
`,
			wantErr: true, wantSub: []string{`"web"`, "privileged"},
		},
		{
			name: "cap_add rejected",
			yaml: `
services:
  web:
    image: nginx
    cap_add: [SYS_ADMIN]
`,
			wantErr: true, wantSub: []string{`"web"`, "cap_add"},
		},
		{
			name: "build rejected",
			yaml: `
services:
  web:
    image: nginx
    build: .
`,
			wantErr: true, wantSub: []string{`"web"`, "build"},
		},
		{
			name: "extends rejected",
			yaml: `
services:
  web:
    image: nginx
    extends:
      service: base
`,
			wantErr: true, wantSub: []string{`"web"`, "extends"},
		},
		{
			name: "network_mode host rejected",
			yaml: `
services:
  web:
    image: nginx
    network_mode: host
`,
			wantErr: true, wantSub: []string{`"web"`, "network_mode"},
		},
		{
			name: "pid host rejected",
			yaml: `
services:
  web:
    image: nginx
    pid: host
`,
			wantErr: true, wantSub: []string{`"web"`, "pid"},
		},
		{
			name: "ipc host rejected",
			yaml: `
services:
  web:
    image: nginx
    ipc: host
`,
			wantErr: true, wantSub: []string{`"web"`, "ipc"},
		},
		{
			name: "userns_mode host rejected",
			yaml: `
services:
  web:
    image: nginx
    userns_mode: host
`,
			wantErr: true, wantSub: []string{`"web"`, "userns_mode"},
		},
		{
			name: "absolute bind path rejected",
			yaml: `
services:
  web:
    image: nginx
    volumes: ["/etc/passwd:/etc/passwd:ro"]
`,
			wantErr: true, wantSub: []string{`"web"`, "absolute"},
		},
		{
			name: "named volume rejected (short form)",
			yaml: `
services:
  web:
    image: nginx
    volumes: ["data:/var/data"]
`,
			wantErr: true, wantSub: []string{`"web"`, "named volume"},
		},
		{
			name: "named volume rejected (long form)",
			yaml: `
services:
  web:
    image: nginx
    volumes:
      - type: volume
        source: data
        target: /var/data
`,
			wantErr: true, wantSub: []string{`"web"`, "named volume"},
		},
		{
			name: "relative bind allowed",
			yaml: `
services:
  web:
    image: nginx
    volumes: ["./data:/var/data"]
`,
			wantErr: false,
		},
		{
			name:    "no services",
			yaml:    `services: {}`,
			wantErr: true, wantSub: []string{"no services"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckStructure(context.Background(), []byte(tc.yaml))
			if tc.wantErr && err == nil {
				t.Fatalf("want rejection, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if err != nil {
				for _, sub := range tc.wantSub {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q missing %q", err.Error(), sub)
					}
				}
			}
		})
	}
}
