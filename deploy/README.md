# Deploying the Slack Q&A bot (`cmd/slackbot`)

Unlike the rest of this repo, `cmd/slackbot` is not a GitHub Action step -- it's a
standalone, always-on process that stays connected to Slack over Socket Mode,
answering `@mention`s inside a PR's existing Slack thread (see `internal/notify`
for how that thread gets created) using that PR's diff. It needs to keep running
continuously, so it's deployed as a systemd service on a machine you control --
here, the same self-hosted box that already runs this repo's GitHub Actions
runner.

## 1. Slack app configuration (one-time, in the Slack app admin UI)

These steps are manual -- there's no API/CLI path for them:

1. Open your existing Slack app at <https://api.slack.com/apps> (the same one
   `SLACK_BOT_TOKEN`/`SLACK_CHANNEL_ID` already use for the notification side).
2. **Socket Mode** (left sidebar) -> enable it.
3. **Basic Information** -> **App-Level Tokens** -> generate a new token with the
   `connections:write` scope. This is `SLACK_APP_TOKEN` below (`xapp-...`) --
   distinct from the bot token.
4. **OAuth & Permissions** -> **Bot Token Scopes** -> add `app_mentions:read`
   (keep `chat:write`, already required for the notification side).
5. **Event Subscriptions** -> enable, and subscribe to the bot event
   `app_mention`. Socket Mode event subscriptions don't need a Request URL.
6. **Install App** -> reinstall to the workspace (any scope change requires
   this). Slack may issue a refreshed Bot User OAuth Token here -- if so, that's
   your `SLACK_BOT_TOKEN` going forward, for both this bot and the existing
   notification integration.
7. Confirm the bot is a member of the target channel (`/invite @your-bot-name`
   in that channel) -- unchanged from the notification setup.

## 2. Build the binary

On the deployment machine (or cross-compiled and copied over):

```
go build -o /usr/local/bin/ai-ci-agent-slackbot ./cmd/slackbot
```

## 3. Configure credentials

Create `/etc/ai-ci-agent/slackbot.env` (root-readable only, since it holds
secrets):

```
SLACK_BOT_TOKEN=xoxb-...
SLACK_APP_TOKEN=xapp-...
SLACK_CHANNEL=C...
GITHUB_TOKEN=ghp_...          # read-only is enough -- this daemon never writes to GitHub
GITHUB_REPOSITORY=owner/repo
LLM_PROVIDER=claude           # or openai; matches the GitHub Action's own llm-provider input
LLM_API_KEY=sk-...
```

`GITHUB_TOKEN` only needs read access to pull requests, files, and issue
comments -- narrower than the token the GitHub Action step gets, since this
daemon only ever reads diffs/marker-comments and posts to Slack.

## 4. Install and start the systemd service

```
sudo useradd --system --no-create-home ai-ci-agent   # if it doesn't already exist
sudo cp deploy/slackbot.service /etc/systemd/system/
sudo chown ai-ci-agent:ai-ci-agent /usr/local/bin/ai-ci-agent-slackbot
sudo chmod 600 /etc/ai-ci-agent/slackbot.env
sudo systemctl daemon-reload
sudo systemctl enable --now slackbot.service
```

## 5. Verify

```
sudo systemctl status slackbot.service
sudo journalctl -u slackbot.service -f
```

Then, in Slack, reply inside an existing PR thread with `@your-bot-name why did
this fail?` (or any question) and confirm a reply comes back referencing the
diff. Also worth checking the two fallback paths: mentioning the bot in a
thread whose PR has since been closed/merged (should reply "couldn't find an
open PR for this thread") and a message that isn't inside any thread at all
(should be silently ignored, not answered).

# Deploying the inbound GitHub webhook server (`cmd/webhookserver`)

Like `cmd/slackbot`, `cmd/webhookserver` is an always-on process, not a GitHub
Action step -- it listens for `POST /webhooks/github` deliveries and, for a
`pull_request` `opened`/`reopened`/`synchronize` event, runs the same review
pipeline (`internal/orchestrate`) the Actions-triggered path uses. See
`internal/webhook`'s package doc comment for the request-handling details.

**One deployment serves any number of repositories**, not just one: each
delivery's own `repository.full_name` in the payload determines which repo a
`*ghclient.Client` is built for (see `internal/webhook.Handler.client`) --
there is no per-repo configuration here. Register the same webhook URL and
`GITHUB_WEBHOOK_SECRET` on every repository that should get reviews/Slack
notifications from this instance.

**This binary authenticates with a plain PAT (`GITHUB_TOKEN`) today, the same
as every other entrypoint in this repo -- one token shared across every
repository it serves, so it must have read/write access to all of them.**
`internal/githubauth` has a complete, tested GitHub App JWT +
installation-token implementation ready to swap in once an App exists (see
that package's doc comment), which would resolve a distinct token per
installation instead of sharing one PAT.

## 1. Build the binary

```
go build -o /usr/local/bin/ai-ci-agent-webhookserver ./cmd/webhookserver
```

## 2. Configure credentials

Create `/etc/ai-ci-agent/webhookserver.env` (root-readable only):

```
GITHUB_WEBHOOK_SECRET=whsec_...        # shared secret configured on every repo's webhook
WEBHOOK_LISTEN_ADDR=:8080              # optional, defaults to :8080
GITHUB_TOKEN=ghp_...                   # needs access to every repo this instance serves
LLM_PROVIDER=claude                    # or openai
LLM_API_KEY=sk-...
SLACK_BOT_TOKEN=xoxb-...               # optional -- omit both to disable Slack notifications
SLACK_CHANNEL=C...
```

## 3. Install and start the systemd service

```
sudo useradd --system --no-create-home ai-ci-agent   # if it doesn't already exist
sudo cp deploy/webhookserver.service /etc/systemd/system/
sudo chown ai-ci-agent:ai-ci-agent /usr/local/bin/ai-ci-agent-webhookserver
sudo chmod 600 /etc/ai-ci-agent/webhookserver.env
sudo systemctl daemon-reload
sudo systemctl enable --now webhookserver.service
```

## 4. What still requires an organization administrator

This service can run today against a plain PAT and manual webhook configuration
(e.g. a repo-level webhook pointed at this server's public URL, with the same
secret as `GITHUB_WEBHOOK_SECRET`), which is enough to develop and test against.
Moving to a real, organization-owned **GitHub App** -- the intended production
setup -- additionally requires an org admin to:

- **Create the organization-owned GitHub App** in the org's GitHub settings.
- **Generate the App's private key** (downloaded once as a `.pem` file) and
  get it to whoever deploys this service, via the org's existing secret-storage
  process -- not committed to this repo.
- **Install the App** on whichever repositories should trigger reviews, and
  record the resulting installation ID.
- **Configure the App's webhook URL** to point at wherever this server is
  publicly reachable (this repo has no reverse proxy/TLS/DNS configuration of
  its own -- see the top-level README's architecture audit for why).
- **Configure production secrets** (the App's private key, `GITHUB_WEBHOOK_SECRET`,
  and this service's other env vars) in whatever the org's production secret
  manager is, rather than the plain env file above (fine for development only).

None of this blocks development or testing today -- see `internal/githubauth`'s
tests, which exercise the full App-JWT/installation-token flow against a
locally generated key and a stub server, with no real App required.

# Deploying the admin dashboard (`cmd/dashboard`)

Also an always-on process, like `cmd/slackbot`/`cmd/webhookserver`. It gives a
`dashlytix` org admin a safe front door for installing the org's GitHub App on
whichever repos they choose -- GitHub's own native install flow does the
actual repo picking -- and shows which installations/repos currently exist,
read live from GitHub on every page load. No local database: access is gated
by a "Sign in with GitHub" OAuth flow that re-checks live org-admin membership
on **every** request, not just at login, so a demoted admin loses access on
their very next click. See `internal/dashboard`'s package doc comment for the
full design rationale.

**This requires a real, organization-owned GitHub App to already exist** --
see "What still requires an organization administrator" above for creating
one. Two credential pairs come off that same App's settings page, and they
are not interchangeable:

- The **App ID + private key** (`GITHUB_APP_ID`/`GITHUB_APP_PRIVATE_KEY`) --
  the same pair `cmd/webhookserver` is meant to eventually use, authenticates
  as the App itself for `ListInstallations`/`ListInstallationRepositories`.
- The App's built-in **OAuth client ID + secret**
  (`GITHUB_OAUTH_CLIENT_ID`/`GITHUB_OAUTH_CLIENT_SECRET`, found further down
  the same settings page under "Sign in with GitHub App") -- authenticates the
  *visiting admin*, never the App.

## 1. Build the binary

```
go build -o /usr/local/bin/ai-ci-agent-dashboard ./cmd/dashboard
```

## 2. Configure credentials

Create `/etc/ai-ci-agent/dashboard.env` (root-readable only):

```
GITHUB_APP_ID=123456
GITHUB_APP_PRIVATE_KEY=...             # base64 of the .pem file: base64 -w0 app.pem
GITHUB_APP_SLUG=dashlytix-pr-review-agent
GITHUB_OAUTH_CLIENT_ID=Iv1....
GITHUB_OAUTH_CLIENT_SECRET=...
DASHBOARD_ORG=dashlytix
DASHBOARD_SESSION_KEY=...              # base64 of 32 random bytes: openssl rand -base64 32
DASHBOARD_BASE_URL=https://dashboard.internal.example.com   # no trailing slash
DASHBOARD_LISTEN_ADDR=:8081            # optional, defaults to :8081
```

`DASHBOARD_BASE_URL` must be reachable from an admin's browser and must exactly
match the callback URL registered on the App (`<DASHBOARD_BASE_URL>/auth/callback`)
under "Identifying and authorizing users" in the App's settings -- a mismatch
here is the most common reason the OAuth flow fails at the callback step.
`DASHBOARD_SESSION_KEY` must decode to exactly 32 bytes (AES-256); anything
else fails fast at startup rather than silently sealing broken cookies.

## 3. Install and start the systemd service

```
sudo useradd --system --no-create-home ai-ci-agent   # if it doesn't already exist
sudo cp deploy/dashboard.service /etc/systemd/system/
sudo chown ai-ci-agent:ai-ci-agent /usr/local/bin/ai-ci-agent-dashboard
sudo chmod 600 /etc/ai-ci-agent/dashboard.env
sudo systemctl daemon-reload
sudo systemctl enable --now dashboard.service
```

## 4. Verify

```
sudo systemctl status dashboard.service
sudo journalctl -u dashboard.service -f
```

Then, in a browser, visit `DASHBOARD_BASE_URL`, sign in with a GitHub account
that is an active admin of `DASHBOARD_ORG`, and confirm the installations
table matches what the App's own "Install App" settings page shows. Also
worth checking the negative case: sign in with an account that is a member
but not an admin (or not a member at all) of the org, and confirm the
dashboard rejects it rather than showing anything.

This repo has no reverse proxy/TLS/DNS configuration of its own -- put this
service behind whatever the org already uses for internal HTTPS termination
(same caveat as `cmd/webhookserver`'s public URL above), since OAuth callback
URLs must be HTTPS in practice and the session cookie's `Secure` attribute is
derived from `DASHBOARD_BASE_URL` starting with `https://`.
