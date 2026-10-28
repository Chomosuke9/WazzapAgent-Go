package command

import (
	"strings"
	"sync"
)

// NoticeChange is what a command did to one item.
type NoticeChange uint8

const (
	NoticeAdded NoticeChange = iota + 1
	NoticeRemoved
	// NoticeChanged is a setting set to a new value, or an item replaced.
	NoticeChanged
	// noticeUpdated is a removal and an addition of the same Kind reported
	// together. Commands report the two halves; NoticeBatch pairs them.
	noticeUpdated
)

func (change NoticeChange) verb() string {
	switch change {
	case NoticeAdded:
		return "added"
	case NoticeRemoved:
		return "removed"
	case noticeUpdated:
		return "updated"
	}
	return "changed"
}

// Notice is one change a command made, such as a memory note added or a
// setting changed. A person who typed the command gets Text. The commands
// the bot runs in one reply are reported together in one message instead,
// one short line per change, where removing an item and adding one of the
// same Kind reads as an update.
type Notice struct {
	// Icon starts the notice's line, e.g. "🧠".
	Icon string
	// Kind names what changed, e.g. "Memory" or "Daily task".
	Kind   string
	Change NoticeChange
	// Item is the item or new value, shown after the Kind, e.g. the
	// memory's text. A NoticeChanged item is the whole line after the icon.
	Item string
	// Text is the full reply for a person who typed the command. A command
	// that reports several notices at once puts its whole reply on the
	// first and leaves Text empty on the rest.
	Text string
}

// NoticeBatch collects what the commands the bot runs in one reply report,
// to send it as one message. It is safe for concurrent use.
type NoticeBatch struct {
	mu      sync.Mutex
	entries []noticeEntry
}

// noticeEntry is a Notice, or plain reply text when notice is nil.
type noticeEntry struct {
	notice *Notice
	text   string
}

func (batch *NoticeBatch) add(entry noticeEntry) {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	batch.entries = append(batch.entries, entry)
}

// Text renders the batch as one message, or "" when nothing was reported.
// A removal and an addition of the same Kind become one update, showing the
// added item. Notices of the same Kind and change share a heading.
func (batch *NoticeBatch) Text() string {
	batch.mu.Lock()
	entries := append([]noticeEntry(nil), batch.entries...)
	batch.mu.Unlock()

	notices := make([]*Notice, 0, len(entries))
	for _, entry := range entries {
		if entry.notice != nil {
			copied := *entry.notice
			notices = append(notices, &copied)
		}
	}
	pairUpdates(notices)

	type group struct {
		icon, heading string
		items         []string
	}
	var groups []*group
	byKey := map[string]*group{}
	var texts []string
	for _, entry := range entries {
		if entry.notice == nil {
			if text := strings.TrimSpace(entry.text); text != "" {
				texts = append(texts, text)
			}
		}
	}
	for _, notice := range notices {
		if notice == nil {
			continue // the removal half of an update
		}
		if notice.Change == NoticeChanged {
			groups = append(groups, &group{icon: notice.Icon, items: []string{notice.Item}})
			continue
		}
		key := notice.Kind + "\x00" + notice.Change.verb()
		if existing := byKey[key]; existing != nil {
			existing.items = append(existing.items, notice.Item)
			continue
		}
		added := &group{icon: notice.Icon, heading: notice.Kind + " " + notice.Change.verb(), items: []string{notice.Item}}
		byKey[key] = added
		groups = append(groups, added)
	}
	lines := make([]string, 0, len(groups)+len(texts))
	for _, group := range groups {
		prefix := strings.TrimSpace(group.icon + " " + group.heading)
		switch {
		case group.heading == "":
			lines = append(lines, strings.TrimSpace(group.icon+" "+group.items[0]))
		case len(group.items) == 1:
			lines = append(lines, prefix+": "+group.items[0])
		default:
			lines = append(lines, prefix+":\n• "+strings.Join(group.items, "\n• "))
		}
	}
	return strings.Join(append(lines, texts...), "\n")
}

// pairUpdates turns each removal that has an addition of the same Kind into
// an update in the addition's place, and sets the removal to nil. Removals
// and additions pair up in the order they were reported.
func pairUpdates(notices []*Notice) {
	removals := map[string][]int{}
	additions := map[string][]int{}
	for index, notice := range notices {
		switch notice.Change {
		case NoticeRemoved:
			removals[notice.Kind] = append(removals[notice.Kind], index)
		case NoticeAdded:
			additions[notice.Kind] = append(additions[notice.Kind], index)
		}
	}
	for kind, removed := range removals {
		added := additions[kind]
		for pair := 0; pair < len(removed) && pair < len(added); pair++ {
			notices[added[pair]].Change = noticeUpdated
			notices[removed[pair]] = nil
		}
	}
}
