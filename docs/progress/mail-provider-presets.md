# Outgoing email: pick a provider instead of typing seven fields

**Issue:** #426. Builds on [byo-outgoing-mail.md](byo-outgoing-mail.md) (#122) and on the port measurement recorded in `SERVICE_PROVISIONING.md` # BYO outgoing mail (#425).

## What was done

Adding an email account meant typing seven fields — host, port, encryption, username, password, from address, label — each of which the admin had to look up in their provider's docs. For every provider worth presetting, four of those are constants. So the add flow is now two steps: pick the provider, then fill in only the credential, the from address, a username when the provider does not fix one, and a region for the two providers whose region changes the host.

**`internal/mailpreset`** (new leaf package, no malmo dependencies) holds the table plus `List`, `Get`, `Valid` and `LabelFor`. Nine entries: `ses`, `sendgrid`, `mailgun`, `postmark`, `brevo`, `resend`, `smtp2go`, `google_workspace`, `custom`. Every one is STARTTLS on a port a hosted box can reach — 587, except SMTP2GO's 2525, the port it recommends as open in the most places. Hosts, ports and username rules were each checked against that provider's own docs on 2026-08-27; the package comment says so and asks the next person to re-check.

Two of the issue's constants were wrong, and both changed the design:

- **Mailgun's EU host is `smtp.eu.mailgun.org`** — a prefix, not a region code — so the issue's single `{region}` host template could not serve both it and SES. Each region option now names the host it resolves to. One mechanism, no substitution, and adding a provider with an odd regional host later costs nothing.
- **Brevo's username is the SMTP login `xxx@smtp-brevo.com`**, not the account email. That is help text, not structure, but it is exactly the kind of thing that turns into a failed test-send and a support message.

**Store.** `mail_providers.provider_type` via the existing idempotent ALTER path, `DEFAULT 'custom'`. No `CHECK` — one cannot ride an ALTER, so it would apply only to fresh DBs; validated in Go instead, the way `scope` and `exposure` are, on both write paths. Rows that predate this change load as `custom`, which is right: they were typed by hand.

**API.** `GET /api/v1/mail-presets`, admin-only, matching `listMailProviders` — only an admin can register a provider. `MailProviderDTO` and `MailProviderBody` gain `provider_type`; an unknown value is a 422, an empty one normalizes to `custom`. The DTO also carries a derived `provider_label`, so a row can render "Amazon SES" without the UI re-deriving it and without breaking if a preset is ever withdrawn. The test-send now **names the blocked port**: a hosted box that fails to *connect* on 25 or 465 gets "this box cannot reach port 465, use 587 with STARTTLS" appended instead of a bare timeout. Only a connect failure triggers it — an auth rejection means the port was reachable, and naming it would mislead.

**Web UI.** `OutgoingEmailSection.vue`: a provider grid, then a short form with the credential labelled per provider ("Server API token", not "Password"), the provider's one-line "what you need and where to get it" plus a docs link, and an **Advanced settings** disclosure holding host, port and encryption — prefilled and editable. `custom` opens that disclosure with nothing prefilled, which is the old form. The label defaults to the provider name so it stops being a field the admin must invent. A hosted admin who selects TLS/465 sees the warning before saving. Rows show the provider name rather than the raw host; a hand-typed row still shows the host, because it has no name to show.

**Unchanged on purpose:** `internal/lifecycle/mail.go`, the install plan, `PUT /apps/{id}/mail-binding`, every audit action. `mailEnvLines` is the seam the credential broker replaces later, and keeping it out is what makes the two features independent (`DECISIONS.md` 2026-08-27, D4).

## Decisions

`DECISIONS.md` 2026-08-27 records D1 (presets in the brain, not the catalog), D2 (`provider_type` is persisted, because the broker needs the identity), D3 (the server stores what the client sends and never re-derives host/port from the preset, so the advanced override survives) and D4 (no change to env injection).

The one deviation from the issue text is the region mechanism, above. The other is `provider_label` on the DTO, which the issue did not list: the alternative was for the UI to resolve the label from the preset list, which works only for admins and breaks on a withdrawn preset.

## Verification

`make test-nopam`, `gofmt`, `go vet`, `make openapi` and the web typecheck + production build are green. New tests: the preset table's invariants (a fixed username mode with no value, a region option with no host, a port a hosted box cannot reach), an old row migrating to `custom`, an unknown `provider_type` rejected at both the store and the API, `provider_type` round-tripping without the host being re-derived, `/mail-presets` refused to a member, and the blocked-port hint firing only on a hosted connect failure.

`make check` was not run end to end: this machine cannot build `github.com/msteinert/pam/v2` (`C.RTLD_NEXT`), which fails identically on unmodified `dev`. Everything `check` runs apart from that package was run individually.

## Known gaps

- **No live test-send was performed against any provider.** That is the issue's real acceptance gate — a hostname or username rule can be wrong in a way no unit test sees — and it needs a provisioned hosted box plus a real account at each of the eight providers. The constants are verified against current vendor docs and nothing more.
- **The SES region list is a common subset** (8 of ~19 regions with an SMTP endpoint). Anyone outside them has to type the host into Advanced settings.
- **Edit does not restore the region select.** Only the resolved host is stored, so editing a SES account shows the host in the advanced fields rather than the region that produced it. Re-picking a region means editing the host.
- **`web-ui` has no test runner**, so the new form is typechecked and built but not exercised by a test.
- A provider whose preset is later withdrawn keeps its `provider_type` and renders by id. Nothing reclassifies it.

## What's next

- Run one live test-send per preset from a provisioned hosted box and correct or drop any preset that fails.
- The credential broker (`NEXT.md` # On-box credential broker) is the consumer `provider_type` was persisted for. It is where per-provider Go logic lands.
- Microsoft 365, Proton, Fastmail and iCloud stay excluded; the appliance profile is where the 465-only two become possible.
