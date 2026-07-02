---
name: Catalog app
about: Add an app to the malmo catalog using the agent-assisted authoring workflow
title: '[Catalog] Add <AppName>'
labels: catalog
assignees: ''
---

<!--
NOTICE: the repo-root catalog/ directory was removed (cloud #62, DECISIONS.md
2026-07-02). The box is now a thin client of the control plane's catalog API and
app artifacts live in the cloud-side store, not in this repo. Do not add
catalog/<id>/{manifest.yml,compose.yml} files here. This template + the
agent-assisted authoring how-to have not yet been rewritten for the store; the
authoring adaptations still apply, but the resulting files land in the store.
-->

**App:** <!-- the app's display name -->
**Repo:** <!-- upstream GitHub URL -->
**Docs:** <!-- upstream docs URL, if any — omit the line if none -->

<!-- One sentence: what the app does. -->

> **Before you open this:** search **open AND closed** issues for the same app first (`gh issue list --search "<AppName>" --state all`, and check the `catalog` label). If one already exists — even closed/rejected — comment there instead of filing a duplicate.

---

**How to do this:** follow [Authoring catalog apps with an agent](/malmoos/malmo/blob/main/docs/dev/authoring-apps-with-an-agent.md#the-prompt). Paste the prompt, append the inputs above, run it inside the malmo repo.

**Done when:**

- [ ] `catalog/<id>/manifest.yml` and `catalog/<id>/compose.yml` exist
- [ ] `go run ./cmd/malmo manifest check catalog/<id>/manifest.yml` passes — schema + admission in one (run it yourself, don't trust the agent's claim)
- [ ] `docker compose -f catalog/<id>/compose.yml config -q` passes
- [ ] `go run ./cmd/malmo manifest resolve catalog/<id>/manifest.yml` run to fill image digests/sizes — or `images:` omitted with a note if the registry was unreachable
- [ ] PR body includes `Closes #<N>`
