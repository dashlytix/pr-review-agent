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
