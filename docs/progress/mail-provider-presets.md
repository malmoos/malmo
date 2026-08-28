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

**Every preset host was probed live**, using the same library the test-send uses (`net/smtp`): dial, STARTTLS with `ServerName` set, EHLO, read the AUTH extension. All sixteen hostnames — the nine presets plus all eight SES regions — resolve, connect on the port the preset carries, complete TLS with a certificate valid for that exact name, and offer `AUTH PLAIN`, which is the mechanism `sendTestMail` uses. That covers hostname, port, TLS and auth-mechanism compatibility. It does **not** cover the username rules or that mail actually leaves, and it ran from a developer machine, not a hosted box, so it says nothing about the 25/465 block.

`make test-nopam`, `gofmt`, `go vet`, `make openapi` and the web typecheck + production build are green. New tests: the preset table's invariants (a fixed username mode with no value, a region option with no host, a port a hosted box cannot reach), an old row migrating to `custom`, an unknown `provider_type` rejected at both the store and the API, `provider_type` round-tripping without the host being re-derived, `/mail-presets` refused to a member, and the blocked-port hint firing only on a hosted connect failure.

`make check` was not run end to end: this machine cannot build `github.com/msteinert/pam/v2` (`C.RTLD_NEXT`), which fails identically on unmodified `dev`. Everything `check` runs apart from that package was run individually.

## Self-review

A sonnet agent reviewed the diff against `docs/dev/code-review.md` and found one Block, now fixed: **editing a Postmark account wiped its username.** `startEdit` blanks the password field, because an empty password means "keep the stored one"; the update path then copied that empty value across to the username, which `same_as_password` keeps in step. Editing a Postmark account's label was enough to leave it with a stored password and no username, failing AUTH on its next send with nothing in the UI to say why. A blank password is no longer copied across, so the paired username is kept the same way the password is.

Two Notes were also acted on. `blockedPortHint` matched on the string prefix `"connect: "`, a silent coupling to `sendTestMail`'s error wording that no test would have caught breaking; dial failures are now a typed `*dialError` matched with `errors.As`, and a new test drives the real `sendTestMail` against a closed port to assert the live path still produces one. (Reverting the type makes that test fail, which is the point of it.) The `:open` binding on the two `<details>` disclosures is correct today only because both blocks remount on change; that assumption is now written next to the binding rather than left for the next person to rediscover.

## The form, second pass

The first version of the picker was a grid of bordered text boxes and a two-column form of bare placeholder inputs. Three things were wrong with it and all three are fixed:

- **The cards did not read as clickable.** They are now real buttons carrying the provider's logo above its name, with hover, focus-visible and active states.
- **The first field was confusing.** It said only "Name", and nothing said whose name. It is now labelled **Account name**, with a hint saying it is malmo's own label for the account and only the admin sees it. Every other input gained a real `<label>` too — no field relies on a placeholder to say what it is.
- **Two columns made the pairing look meaningful when it was not.** One field per line now.

Logos are bundled, not fetched: a box may have no internet, and the dashboard must not call a CDN on render. They are single-path monochrome marks drawn in `currentColor`, so they inherit the olive tokens. Three providers have no mark available (Postmark, SMTP2GO, Google Workspace) and fall back to a lettermark tile, which reads as a logo slot rather than a missing image; `custom` is not a brand and gets the Lucide server glyph. The paths come from simple-icons, whose icon data is CC0 — the trademarks stay with their owners and identify the provider being chosen.

Styling follows the repo's own idioms rather than new ad-hoc classes: the shared `<Button>` component for every action, and the labelled-stack field pattern (`fieldClass`, label, hint) lifted from `CustomInstallView`. The **Advanced settings** disclosure is now **Server settings**, which says what is inside it.

## Adding an account is two routes, not two states

The picker and the form were local state on one page, so the browser Back button did nothing where it looked like it should. They are now real URLs:

```
/settings/mail            the account list
/settings/mail/add        pick a provider
/settings/mail/add/:preset   fill in what that preset cannot know
```

Back walks form → picker → list, and a preset form can be linked or reloaded. The picked preset is read from the route rather than held in a ref — that is what makes Back work, not a listener bolted onto it. The **field values are deliberately not in the URL**: a credential must never land in browser history, so a reload re-seeds the form from the preset. A `:preset` that names nothing (a stale link, or a preset we withdraw later) replaces itself with the picker instead of rendering an empty form.

Editing stays inline on its row. No navigation happens there, so there is no Back to get wrong.

The split put the form rules in one place, `src/mailProviderForm.ts` — the shape, the preset seeding, the region→host resolution, the `same_as_password` pairing, the port warning. The add form and the inline edit form have to agree field for field, and they now do so by construction rather than by two copies staying in step.

## Checking a config before it is saved

`POST /api/v1/mail-providers/verify` takes the same body as create rather than an id, and that is the point: it runs before the provider exists, so a config that cannot connect never becomes an account the admin has to find and delete. It connects, does STARTTLS, authenticates, and hangs up. The add form runs it by default, behind a "Test configuration when adding" checkbox, and runs it *before* the elevation prompt — no point asking for a password to save settings that do not work.

It shares the dial-and-auth path with the test-send (`connectMail`) rather than approximating it, so the check cannot drift from what a real send does; it simply stops before `MAIL FROM`. That is also its limit, stated in the code: it cannot prove the provider will accept the **from address**, because most providers only judge that at `MAIL FROM` or at queue time. The per-row test-send stays the stronger check for exactly that reason.

Sending the check mail to a malmo-owned address was considered and rejected. A new Amazon SES account is in sandbox mode and may only send to verified addresses, as is a free Mailgun domain — so a fixed external recipient would fail for the very admins whose config is fine. It would also spend the user's sender reputation on our test (a hard bounce counts against SES's 5% ceiling) and would put malmo in the business of receiving mail, which this doc rules out.

Not audited, and admin-only rather than elevation-class: it stores nothing and sends nothing.

## The picker inside the install dialog

Choosing an account at install time was a plain radio list of account names. An account name is whatever the admin typed ("Work mail"), so the list did not say which provider actually sends the mail. The dialog now uses the same stacked cards the rest of the dashboard uses for a choice like this: one card per account, the provider logo on the left, the whole card as the hit target, and the selected card carrying an outline. "None" is the first card and stays a normal choice, with "Email features stay off." under it.

This needed one field on the wire. `MailProviderOption`, the id+label shape the install plan and `GET /api/v1/mail-providers/options` both return, gains `provider_type`, which is the preset id the logo is looked up by. It names a brand, not a host or a credential, so it is safe on both non-admin surfaces. `MailProviderLogo` already maps a preset id to a bundled file, so no new asset work was needed.

## Two holes an automated review found

Both are in the add and edit forms, and both fail the same way: quietly.

**A failed preset load left no way forward.** The provider list comes from the server, and "Custom SMTP server" is one of its entries, so a failed `GET /api/v1/mail-presets` rendered an empty grid with no message and nothing to click. A deep link into `/settings/mail/add/<preset>` was worse: it sat on "Loading…" forever, because the "unknown preset" redirect only fires once the list has arrived. Both now say the list could not be loaded and offer a retry.

**The Advanced username could diverge from a paired credential.** For a `same_as_password` preset (Postmark), the username *is* the server token. On the add form an edit there was silently overwritten; on the edit form, where a blank password means "keep the stored one", it was saved on its own and the account started failing AUTH with nothing in the UI to explain it. The field is now shown but disabled for that mode, so the pairing rule is visible instead of enforceable-in-theory. `fixed` presets (SendGrid's literal `apikey`) stay editable: there the username is a real value an admin might need to override.

## Known gaps

- **No live test-send was performed against any provider.** That is the issue's real acceptance gate — a *username* rule can be wrong in a way neither a unit test nor the connectivity probe sees — and it needs a provisioned hosted box plus a real account at each provider. The hosts and ports are now verified live (above); the username rules are verified against current vendor docs and nothing more. The three worth an account are SendGrid (the fixed `apikey`), Brevo (the `xxx@smtp-brevo.com` login, the correction most likely to be wrong) and Postmark (`same_as_password`, the only structurally unusual mode).
- **The SES region list is a common subset** (8 of ~19 regions with an SMTP endpoint). Anyone outside them has to type the host into Advanced settings.
- **Edit does not restore the region select.** Only the resolved host is stored, so editing a SES account shows the host in the advanced fields rather than the region that produced it. Re-picking a region means editing the host.
- **`web-ui` has no test runner**, so the new form is typechecked and built but not exercised by a test.
- A provider whose preset is later withdrawn keeps its `provider_type` and renders by id. Nothing reclassifies it.

## What's next

- Run one live test-send per preset from a provisioned hosted box and correct or drop any preset that fails.
- The credential broker (`NEXT.md` # On-box credential broker) is the consumer `provider_type` was persisted for. It is where per-provider Go logic lands.
- Microsoft 365, Proton, Fastmail and iCloud stay excluded; the appliance profile is where the 465-only two become possible.
