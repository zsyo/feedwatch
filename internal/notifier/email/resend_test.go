package email

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/resend/resend-go/v4"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
)

type fakeSender struct {
	params *resend.SendEmailRequest
	err    error
	calls  int
}

func (f *fakeSender) SendWithContext(_ context.Context, p *resend.SendEmailRequest) (*resend.SendEmailResponse, error) {
	f.calls++
	f.params = p
	if f.err != nil {
		return nil, f.err
	}
	return &resend.SendEmailResponse{Id: "fake-id"}, nil
}

func spec() notifier.NotifierSpec {
	return notifier.NotifierSpec{
		Type:          "email",
		From:          "FeedWatch <noreply@example.com>",
		To:            []string{"you@example.com"},
		Cc:            []string{"cc@example.com"},
		SubjectPrefix: "[FeedWatch]",
		APIKey:        "re_test",
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSendBuildsResendRequest(t *testing.T) {
	f := &fakeSender{}
	n := newWithSender(f, spec(), quiet())
	if n.Type() != "email" {
		t.Errorf("Type() = %q", n.Type())
	}
	note := domain.Notification{
		Kind: domain.KindNewReleases,
		Items: []domain.ReleaseItem{{
			Feed: "rust-lang/rust", Version: "1.99.1", URL: "https://example.com/r", IsNew: true,
		}},
	}
	if err := n.Send(context.Background(), note); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if f.calls != 1 || f.params == nil {
		t.Fatalf("expected one send, got %d", f.calls)
	}
	p := f.params
	if p.From != "FeedWatch <noreply@example.com>" || len(p.To) != 1 || p.To[0] != "you@example.com" {
		t.Errorf("from/to wrong: %+v", p)
	}
	if len(p.Cc) != 1 || !strings.Contains(p.Subject, "发现 1 个新版本") {
		t.Errorf("cc/subject wrong: %+v", p)
	}
	if !strings.Contains(p.Html, "1.99.1") || !strings.Contains(p.Text, "1.99.1") {
		t.Errorf("both HTML and text bodies must be populated: %q / %q", p.Html, p.Text)
	}
}

// Multiple recipients are merged into ONE Resend API call: the whole To/Cc
// slice goes into a single SendEmailRequest, not one request per address.
func TestSendMergesAllRecipientsIntoOneCall(t *testing.T) {
	f := &fakeSender{}
	s := spec()
	s.To = []string{"a@example.com", "b@example.com", "c@example.com"}
	s.Cc = []string{"cc1@example.com", "cc2@example.com"}
	n := newWithSender(f, s, quiet())
	if err := n.Send(context.Background(), domain.Notification{
		Kind: domain.KindNewReleases,
		Items: []domain.ReleaseItem{{
			Feed: "golang/go", Version: "go1.27.4", URL: "https://example.com/r", IsNew: true,
		}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("want exactly ONE API call for %d recipients, got %d", len(s.To)+len(s.Cc), f.calls)
	}
	if len(f.params.To) != 3 || f.params.To[2] != "c@example.com" || len(f.params.Cc) != 2 {
		t.Errorf("recipients were split or lost: to=%v cc=%v", f.params.To, f.params.Cc)
	}
}

func TestSendErrorIsPropagated(t *testing.T) {
	f := &fakeSender{err: errors.New("resend 500")}
	n := newWithSender(f, spec(), quiet())
	err := n.Send(context.Background(), domain.Notification{Kind: domain.KindBaseline})
	if err == nil || !strings.Contains(err.Error(), "resend 500") {
		t.Fatalf("expected wrapped send error, got %v", err)
	}
}

func TestNewValidatesCredentials(t *testing.T) {
	if _, err := New(notifier.NotifierSpec{}, quiet()); err == nil {
		t.Error("empty API key must fail")
	}
	s := spec()
	s.To = nil
	if _, err := New(s, quiet()); err == nil {
		t.Error("empty recipients must fail")
	}
	if _, err := New(spec(), quiet()); err != nil {
		t.Fatalf("valid spec should build: %v", err)
	}
}
