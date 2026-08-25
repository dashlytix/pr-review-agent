package slackbot

import (
	"context"
	"log"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Run connects to Slack over Socket Mode and answers @-mentions until
// ctx is cancelled. It blocks for the daemon's lifetime -- callers
// (cmd/slackbot/main.go) run it against a context cancelled by
// SIGINT/SIGTERM for a clean shutdown.
func Run(ctx context.Context, cfg Config) error {
	api := slack.New(cfg.BotToken, slack.OptionAppLevelToken(cfg.AppToken))
	client := socketmode.New(api)

	cache := newPRCache(cfg.Client)
	go cache.runRefresh(ctx, cfg.cacheRefresh())

	seen := newSeenSet(500)

	go func() {
		for evt := range client.Events {
			if evt.Type != socketmode.EventTypeEventsAPI {
				continue
			}
			eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			// Ack immediately, before any processing -- gather+LLM+GitHub
			// calls can take several seconds, and an un-acked event gets
			// redelivered by Slack, which would otherwise risk a duplicate
			// reply for the same question.
			if evt.Request != nil {
				client.Ack(*evt.Request)
			}

			if eventsAPIEvent.Type != slackevents.CallbackEvent {
				continue
			}
			ev, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.AppMentionEvent)
			if !ok {
				continue
			}
			// Defense-in-depth against a redelivery slipping through
			// before the Ack above lands, or Slack itself retrying.
			if !seen.addIfNew(ev.TimeStamp) {
				continue
			}

			m := Mention{Channel: ev.Channel, TS: ev.TimeStamp, ThreadTS: ev.ThreadTimeStamp, Text: ev.Text}
			go handleMention(ctx, cfg, cache, m)
		}
	}()

	log.Printf("slackbot: connecting to Slack via Socket Mode")
	return client.RunContext(ctx)
}

// seenSet is a small, fixed-capacity, insertion-ordered set used to drop
// a redelivered Slack event that slipped through before this daemon's
// own Ack was received -- process-lifetime only, no persistence needed,
// since a duplicate redelivery only ever happens within Slack's own
// short retry window, not across a restart.
type seenSet struct {
	mu       sync.Mutex
	capacity int
	order    []string
	set      map[string]struct{}
}

func newSeenSet(capacity int) *seenSet {
	return &seenSet{capacity: capacity, set: make(map[string]struct{}, capacity)}
}

// addIfNew reports whether id was not already present, adding it. When
// the set is at capacity, the oldest entry is evicted to make room.
func (s *seenSet) addIfNew(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.set[id]; ok {
		return false
	}
	if len(s.order) >= s.capacity {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.set, oldest)
	}
	s.order = append(s.order, id)
	s.set[id] = struct{}{}
	return true
}
