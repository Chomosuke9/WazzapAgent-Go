package action

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// outbox is a Store holding one pending reply.
type outbox struct {
	action StoredAction
}

func (box *outbox) Load(context.Context, agent.DispatchRef) (StoredAction, error) {
	return box.action, nil
}
func (box *outbox) Start(context.Context, agent.DispatchRef, time.Time) error {
	box.action.State = StateExecuting
	return nil
}
func (box *outbox) Complete(_ context.Context, _ agent.DispatchRef, receipt string, at time.Time) error {
	box.action.State, box.action.ProviderReceipt, box.action.CompletedAt = StateSucceeded, receipt, &at
	return nil
}
func (box *outbox) FailTerminal(context.Context, agent.DispatchRef, agent.ErrorCode, time.Time) error {
	box.action.State = StateFailedTerminal
	return nil
}
func (box *outbox) MarkUnknown(context.Context, agent.DispatchRef, agent.ErrorCode, time.Time) error {
	box.action.State = StateUnknownOutcome
	return nil
}

type allowAll struct{}

func (allowAll) AuthorizeSend(context.Context, agent.Key) error { return nil }

// imageSender records whether a reply went as text or as a captioned image.
type imageSender struct {
	texts  []SendTextRequest
	images []SendImageRequest
}

func (sender *imageSender) Ready() bool { return true }
func (sender *imageSender) SendText(_ context.Context, request SendTextRequest) (SendTextResult, error) {
	sender.texts = append(sender.texts, request)
	return SendTextResult{ProviderReceipt: "text-receipt"}, nil
}
func (sender *imageSender) SendCopyCode(context.Context, SendCopyCodeRequest) error { return nil }
func (sender *imageSender) SendImageReply(_ context.Context, request SendImageRequest) (SendTextResult, error) {
	sender.images = append(sender.images, request)
	return SendTextResult{ProviderReceipt: "image-receipt"}, nil
}

// textOnly is a sender that cannot send images.
type textOnly struct {
	texts []SendTextRequest
}

func (sender *textOnly) Ready() bool { return true }
func (sender *textOnly) SendText(_ context.Context, request SendTextRequest) (SendTextResult, error) {
	sender.texts = append(sender.texts, request)
	return SendTextResult{ProviderReceipt: "text-receipt"}, nil
}
func (sender *textOnly) SendCopyCode(context.Context, SendCopyCodeRequest) error { return nil }

type attachments struct {
	invocation identity.InvocationID
	sent       []string
}

func (source *attachments) ReplyAttachment(_ context.Context, _ agent.Key, invocation identity.InvocationID) (*Attachment, error) {
	if invocation != source.invocation {
		return nil, nil
	}
	return &Attachment{Path: "/tmp/cat.jpg", Mime: "image/jpeg"}, nil
}

func (source *attachments) AttachmentSent(_ context.Context, _ agent.Key, invocation identity.InvocationID, receipt string) error {
	source.sent = append(source.sent, invocation.String()+"@"+receipt)
	return nil
}

func dispatchReply(t *testing.T, sender TextSender, source AttachmentSource, text string, invocation identity.InvocationID) agent.DeliveryResult {
	t.Helper()
	tenant, _ := identity.NewTenantID()
	account, _ := identity.NewAccountID()
	chat, _ := identity.NewChatID()
	actionID, _ := identity.NewActionID()
	ref := agent.DispatchRef{Key: agent.Key{TenantID: tenant, AccountID: account, ChatID: chat}, ActionID: actionID}
	box := &outbox{action: StoredAction{Ref: ref, InvocationID: invocation, Text: text, State: StatePending}}
	dispatcher, err := NewDispatcher(box, allowAll{}, sender, agent.SystemClock{}, DiscardObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if source != nil {
		dispatcher.SetAttachments(source)
	}
	result, err := dispatcher.Dispatch(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReplyGoesOutAsTheCaptionOfItsAttachment(t *testing.T) {
	invocation, _ := identity.NewInvocationID()
	source := &attachments{invocation: invocation}
	sender := &imageSender{}
	if result := dispatchReply(t, sender, source, "Here is your cat astronaut!", invocation); result.Status != agent.DeliverySucceeded {
		t.Fatalf("delivery = %+v", result)
	}
	if len(sender.texts) != 0 || len(sender.images) != 1 || sender.images[0].Caption != "Here is your cat astronaut!" || sender.images[0].Path != "/tmp/cat.jpg" {
		t.Fatalf("sent texts=%+v images=%+v", sender.texts, sender.images)
	}
	if len(source.sent) != 1 || source.sent[0] != invocation.String()+"@image-receipt" {
		t.Fatalf("attachment records = %q", source.sent)
	}
}

func TestReplyWithoutAnAttachableImageStaysText(t *testing.T) {
	invocation, _ := identity.NewInvocationID()
	other, _ := identity.NewInvocationID()
	for name, test := range map[string]struct {
		sender TextSender
		source AttachmentSource
		text   string
		turn   identity.InvocationID
	}{
		"no source":       {&imageSender{}, nil, "hi", invocation},
		"other turn":      {&imageSender{}, &attachments{invocation: invocation}, "hi", other},
		"too long":        {&imageSender{}, &attachments{invocation: invocation}, strings.Repeat("a", MaxCaptionRunes+1), invocation},
		"text-only phone": {&textOnly{}, &attachments{invocation: invocation}, "hi", invocation},
	} {
		dispatchReply(t, test.sender, test.source, test.text, test.turn)
		var texts, images int
		switch sender := test.sender.(type) {
		case *imageSender:
			texts, images = len(sender.texts), len(sender.images)
		case *textOnly:
			texts = len(sender.texts)
		}
		if texts != 1 || images != 0 {
			t.Errorf("%s: texts=%d images=%d, want the reply as text", name, texts, images)
		}
		if source, ok := test.source.(*attachments); ok && len(source.sent) != 0 {
			t.Errorf("%s: an attachment was recorded as sent", name)
		}
	}
}
