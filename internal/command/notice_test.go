package command

import "testing"

func TestNoticeBatchPairsRemovalAndAdditionIntoUpdate(t *testing.T) {
	batch := &NoticeBatch{}
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeRemoved, Item: "Budi lives in Bandung"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeAdded, Item: "Budi lives in Jakarta"}})
	if got, want := batch.Text(), "🧠 Memory updated: Budi lives in Jakarta"; got != want {
		t.Fatalf("Text() = %q, want %q", got, want)
	}
}

func TestNoticeBatchMergesNoticesIntoOneMessage(t *testing.T) {
	batch := &NoticeBatch{}
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeAdded, Item: "likes cats"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "📅", Kind: "Daily task", Change: NoticeAdded, Item: "every day at 07:00"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeAdded, Item: "likes dogs"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeRemoved, Item: "likes fish"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeRemoved, Item: "old note"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "⚙️", Kind: "Trigger", Change: NoticeChanged, Item: "Mention is on."}})
	batch.add(noticeEntry{text: "No memory has the ID zz99."})
	want := "🧠 Memory updated:\n• likes cats\n• likes dogs\n" +
		"📅 Daily task added: every day at 07:00\n" +
		"⚙️ Mention is on.\n" +
		"No memory has the ID zz99."
	// The two additions pair with the two removals; nothing is left removed.
	if got := batch.Text(); got != want {
		t.Fatalf("Text() =\n%s\nwant\n%s", got, want)
	}
}

func TestNoticeBatchKeepsUnpairedRemovals(t *testing.T) {
	batch := &NoticeBatch{}
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeRemoved, Item: "a"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeRemoved, Item: "b"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🧠", Kind: "Memory", Change: NoticeAdded, Item: "a and b"}})
	batch.add(noticeEntry{notice: &Notice{Icon: "🖼️", Kind: "Sticker", Change: NoticeAdded, Item: "facepalm"}})
	want := "🧠 Memory removed: b\n🧠 Memory updated: a and b\n🖼️ Sticker added: facepalm"
	if got := batch.Text(); got != want {
		t.Fatalf("Text() =\n%s\nwant\n%s", got, want)
	}
	if got := (&NoticeBatch{}).Text(); got != "" {
		t.Fatalf("empty batch Text() = %q", got)
	}
}
