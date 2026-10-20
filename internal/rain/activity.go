package rain

import (
	"sort"
	"sync"
	"time"
)

// Tracker remembers when each person last spoke in each group, which is the
// whole of what "active in the last N minutes" means here. It keeps nothing
// older than Keep, and nothing on disk: after a restart the bot has seen
// nobody, and the first rain covers only who has spoken since. A minute or
// two of blindness costs less than writing every group message to disk.
type Tracker struct {
	Keep time.Duration

	mu    sync.Mutex
	chats map[int64]map[int64]Seen
	swept time.Time
}

// Seen is one person's latest message in a group.
type Seen struct {
	ID   int64
	Name string
	At   time.Time
}

func NewTracker(keep time.Duration) *Tracker {
	return &Tracker{Keep: keep, chats: map[int64]map[int64]Seen{}}
}

// Saw records a message.
func (t *Tracker) Saw(chat, user int64, name string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.chats[chat]
	if m == nil {
		m = map[int64]Seen{}
		t.chats[chat] = m
	}
	if old, ok := m[user]; !ok || at.After(old.At) {
		m[user] = Seen{ID: user, Name: name, At: at}
	}
	if at.Sub(t.swept) > t.Keep {
		t.sweep(at)
	}
}

// Active lists who has spoken in a group since a moment, the most recent
// first. Ties, several messages in one second, go to the lower id so the
// order never depends on map iteration.
func (t *Tracker) Active(chat int64, since time.Time) []Seen {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Seen
	for _, s := range t.chats[chat] {
		if !s.At.Before(since) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (t *Tracker) sweep(now time.Time) {
	cut := now.Add(-t.Keep)
	for chat, m := range t.chats {
		for id, s := range m {
			if s.At.Before(cut) {
				delete(m, id)
			}
		}
		if len(m) == 0 {
			delete(t.chats, chat)
		}
	}
	t.swept = now
}
