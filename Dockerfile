# Builder — go.mod now has a real require block (internal/slackbot's
# github.com/slack-go/slack, added for cmd/slackbot -- a separate,
# always-on daemon not built by this Dockerfile at all, see cmd/slackbot
# and deploy/). This build only ever produces ./cmd/agent, which doesn't
# import slack-go, but Go's module graph still needs go.sum's checksums
# to resolve the build list, so -- unlike before this dependency
# landed -- this build now needs network access to a module proxy
# (unless the module cache is already warm), not just to pull the base
# image.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/ai-ci-agent ./cmd/agent

# Runtime — static binary on a minimal, non-root base. No shell tools are
# needed since the agent only talks to the GitHub API and the LLM
# provider over HTTPS.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ai-ci-agent /ai-ci-agent
ENTRYPOINT ["/ai-ci-agent"]
