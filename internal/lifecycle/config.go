package lifecycle

// User-supplied app configuration (APP_MANIFEST.md # D4). A manifest `config:`
// field declares a value only the user can provide (an API token, an external
// connection string, a provider/model selector). Unlike the MALMO_* injected
// family, the value lands DIRECTLY under its own app_env name in the target
// service's compose-override environment — no indirection, no mapping line.
// writeOverride stamps it at install; SetConfig re-stamps it on a post-install
// edit.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/malmoos/malmo/internal/manifest"
	"github.com/malmoos/malmo/internal/store"

	"gopkg.in/yaml.v3"
)

// validateConfigServices checks that every config field naming an explicit
// target service refers to a real compose service (APP_MANIFEST.md # D4). The
// manifest validator can't do this (it has no compose), so install calls it as a
// fail-fast backstop before any state is written. A field with no `service:`
// targets main_service and is not checked here.
func validateConfigServices(man *manifest.Manifest, composeBytes []byte) error {
	needs := false
	for _, f := range man.Config {
		if f.Service != "" {
			needs = true
			break
		}
	}
	if !needs {
		return nil
	}
	svcs, err := parseComposeServices(composeBytes)
	if err != nil {
		return err
	}
	for _, f := range man.Config {
		if f.Service != "" {
			if _, ok := svcs[f.Service]; !ok {
				return fmt.Errorf("config[%s]: service %q is not defined in the compose", f.AppEnv, f.Service)
			}
		}
	}
	return nil
}

// configEnvByService buckets an instance's stored config values by their target
// compose service, keyed by app_env (APP_MANIFEST.md # D4). The target service
// comes from the manifest field (`service:` or main_service); the value comes
// from the store. A field with no stored value (an optional field left unset)
// injects nothing — it is skipped, so the app keeps its own compose default.
func (m *Manager) configEnvByService(id string, man *manifest.Manifest) (map[string]map[string]string, error) {
	if len(man.Config) == 0 {
		return nil, nil
	}
	stored, err := m.store.GetInstanceConfig(id)
	if err != nil {
		return nil, err
	}
	valueByEnv := make(map[string]string, len(stored))
	for _, c := range stored {
		valueByEnv[c.AppEnv] = c.Value
	}
	byService := map[string]map[string]string{}
	for _, f := range man.Config {
		val, ok := valueByEnv[f.AppEnv]
		if !ok {
			continue
		}
		svc := f.Service
		if svc == "" {
			svc = man.MainService
		}
		if byService[svc] == nil {
			byService[svc] = map[string]string{}
		}
		byService[svc][f.AppEnv] = val
	}
	return byService, nil
}

// SetConfig replaces an instance's user-supplied config values and re-stamps
// them into its compose override (APP_MANIFEST.md # D4). A running instance's
// containers are recreated with `compose up -d` — env is read only at container
// create — so the change takes effect immediately; a stopped instance picks it
// up on its next Start. Brain commits first: if the recreate fails the store and
// override already hold the desired state and the reconcile pass converges the
// containers (same posture as RebindMail). The caller (API) validates the values
// against the manifest before this runs.
func (m *Manager) SetConfig(ctx context.Context, id string, cfg []store.InstanceConfig) error {
	defer m.lockInstance(id)()
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	man, err := m.loadInstanceManifest(id)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	if err := m.store.SetInstanceConfig(id, cfg); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}
	if err := m.restampConfigEnv(id, man); err != nil {
		return fmt.Errorf("rewrite override: %w", err)
	}
	if inst.State != "running" {
		slog.Info("app config updated (applies at next start)",
			"instance_id", id, "name", inst.Name)
		return nil
	}
	upCtx, cancel := context.WithTimeout(ctx, m.healthWait)
	defer cancel()
	if out, err := m.docker.ComposeUp(upCtx, m.instanceDir(id), "malmo-"+id); err != nil {
		return fmt.Errorf("compose up: %w\n%s", err, out)
	}
	slog.Info("app config updated", "instance_id", id, "name", inst.Name)
	return nil
}

// restampConfigEnv patches an instance's compose.override.yml so each service's
// `environment:` block matches the current stored config values, leaving the
// rest of the brain-generated override intact. The override's environment block
// is wholly config-owned (writeOverride sets it from nothing else), so a service
// with no config value has its environment key removed — a value cleared on edit
// stops being injected. Unlike reapplyResourceLimits this needs no change
// detection: SetConfig is an explicit edit and always wants the result applied.
func (m *Manager) restampConfigEnv(id string, man *manifest.Manifest) error {
	envByService, err := m.configEnvByService(id, man)
	if err != nil {
		return err
	}
	ovPath := filepath.Join(m.instanceDir(id), "compose.override.yml")
	raw, err := os.ReadFile(ovPath)
	if err != nil {
		return fmt.Errorf("read override: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse override: %w", err)
	}
	services, _ := doc["services"].(map[string]any)
	if services == nil {
		return fmt.Errorf("override for %s has no services", id)
	}
	for name, svcAny := range services {
		svc, _ := svcAny.(map[string]any)
		if svc == nil {
			continue
		}
		if env := envByService[name]; len(env) > 0 {
			svc["environment"] = env
		} else {
			delete(svc, "environment")
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(ovPath, out, 0o644); err != nil {
		return fmt.Errorf("write override: %w", err)
	}
	return nil
}
