package slackbot

import (
	"context"
	"log"
	"regexp"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/gather"
	"github.com/dimension/ai-ci-agent/internal/notify"
)

// Mention is the subset of a Slack app_mention event handleMention
// needs, decoupled from slackevents.AppMentionEvent so the
// lookup -> gather -> answer -> render pipeline is unit-testable without
// the raw socketmode/slackevents transport types (see daemon.go for the
// translation from the real event).
type Mention struct {
	Channel string
	TS      string // this message's own timestamp
	// ThreadTS is the thread root's timestamp, or "" if this message
	// isn't inside any thread.
	ThreadTS string
	// Text is the raw message text, including the leading "<@BOTID>"
	// Slack inserts for an app_mention.
	Text string
}

var mentionPattern = regexp.MustCompile(`<@[A-Z0-9]+>\s*`)

// stripMention removes the leading "<@BOTID>" from an app_mention
// event's text, leaving just the human's question.
func stripMention(text string) string {
	return strings.TrimSpace(mentionPattern.ReplaceAllString(text, ""))
}

// isThreadReply reports whether m is a genuine reply inside an existing
// thread, as opposed to a new top-level message that happens to mention
// the bot (ThreadTS unset) or the thread root's own message restating
// its ts as ThreadTS -- neither has an existing PR thread to answer
// into.
func isThreadReply(m Mention) bool {
	return m.ThreadTS != "" && m.ThreadTS != m.TS
}

// handleMention answers one @-mention: looks up which PR the thread
// belongs to, fetches its diff, asks the LLM the human's question, and
// replies in the same thread. Every failure mode posts a short, visible
// reply rather than going silent -- a human explicitly asked a question
// and is watching for a reply, so silence here reads as broken, not
// idle, unlike this repo's fire-and-forget lifecycle notifications
// (matches the degrade-but-stay-visible pattern in post.RenderFallback/
// RenderMinimal).
func handleMention(ctx context.Context, cfg Config, cache *prCache, m Mention) {
	if !isThreadReply(m) {
		return // not a reply inside an existing thread -- nothing to answer
	}
	if cfg.Channel != "" && m.Channel != cfg.Channel {
		return // mention from a channel this bot isn't scoped to
	}

	question := stripMention(m.Text)
	if question == "" {
		return
	}

	slackCfg := notify.SlackConfig{BotToken: cfg.BotToken, Channel: m.Channel}

	prNumber, found, err := cache.lookup(ctx, m.ThreadTS)
	if err != nil {
		log.Printf("slackbot: pr lookup failed for thread %s: %v", m.ThreadTS, err)
		reply(ctx, slackCfg, m.ThreadTS, question, "Sorry, I hit an error looking up which PR this thread belongs to.")
		return
	}
	if !found {
		reply(ctx, slackCfg, m.ThreadTS, question, "I couldn't find an open PR for this thread -- it may already be closed or merged.")
		return
	}

	req, _, err := gather.GatherForReview(ctx, cfg.Client, prNumber)
	if err != nil {
		log.Printf("slackbot: gather failed for pr %d: %v", prNumber, err)
		reply(ctx, slackCfg, m.ThreadTS, question, "Sorry, I hit an error fetching this PR's diff.")
		return
	}

	answer, err := cfg.Provider.Answer(ctx, req, question)
	if err != nil {
		log.Printf("slackbot: answer call failed for pr %d: %v", prNumber, err)
		reply(ctx, slackCfg, m.ThreadTS, question, "Sorry, I hit an error trying to answer that -- try again in a bit.")
		return
	}

	if _, err := notify.Post(ctx, slackCfg, notify.RenderAnswer(question, answer), m.ThreadTS); err != nil {
		log.Printf("slackbot: reply failed for pr %d: %v", prNumber, err)
	}
}

// reply posts a short fallback message into the thread for a
// degrade-but-stay-visible failure, reusing RenderAnswer so the reply
// still carries the original question for context.
func reply(ctx context.Context, cfg notify.SlackConfig, threadTS, question, message string) {
	if _, err := notify.Post(ctx, cfg, notify.RenderAnswer(question, message), threadTS); err != nil {
		log.Printf("slackbot: fallback reply failed: %v", err)
	}
}
