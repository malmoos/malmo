---
name: Catalog app
about: Add an app to the molma catalog using the agent-assisted authoring workflow
title: '[Catalog] Add <AppName>'
labels: catalog
assignees: ''
---

**App:** <!-- the app's display name -->
**Repo:** <!-- upstream GitHub URL -->
**Docs:** <!-- upstream docs URL, if any — omit the line if none -->

<!-- One sentence: what the app does. -->

---

**How to do this:** follow [Authoring catalog apps with an agent](/molmaos/molma/blob/main/docs/dev/authoring-apps-with-an-agent.md#the-prompt). Paste the prompt, append the inputs above, run it inside the molma repo.

**Done when:**

- [ ] `catalog/<id>/manifest.yml` and `catalog/<id>/compose.yml` exist
- [ ] `go run ./cmd/molma manifest lint catalog/<id>/manifest.yml` passes (run it yourself — don't trust the agent's claim)
- [ ] `docker compose -f catalog/<id>/compose.yml config -q` passes
- [ ] Compose eyeballed against `internal/admission/admission.go` — no ports, named volumes, absolute binds, privileged, cap_add, build:, host namespaces
- [ ] PR body includes `Closes #<N>`
