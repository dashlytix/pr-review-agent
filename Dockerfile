# Builder — no external Go modules, so no network access is needed
# beyond pulling the base image itself.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/ai-ci-agent ./cmd/agent

# Runtime — static binary on a minimal, non-root base. No shell tools are
# needed since the agent only talks to the GitHub API and the LLM
# provider over HTTPS.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ai-ci-agent /ai-ci-agent
ENTRYPOINT ["/ai-ci-agent"]
