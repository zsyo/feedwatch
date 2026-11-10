// Package email implements notifier.Notifier using the official Resend SDK.
package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/resend/resend-go/v4"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
	notiftpl "feedwatch/internal/notifier/template"
)

// emailSender is the tiny slice of the Resend client we actually use. Keeping
// it as an interface lets unit tests inject a fake without network calls.
type emailSender interface {
	SendWithContext(ctx context.Context, params *resend.SendEmailRequest) (*resend.SendEmailResponse, error)
}

type Notifier struct {
	sender emailSender
	spec   notifier.NotifierSpec
	log    *slog.Logger
}

func newWithSender(sender emailSender, spec notifier.NotifierSpec, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{sender: sender, spec: spec, log: log}
}

// New matches notifier.Factory and is registered by the composition root.
func New(spec notifier.NotifierSpec, log *slog.Logger) (notifier.Notifier, error) {
	if strings.TrimSpace(spec.APIKey) == "" {
		return nil, errors.New("email: Resend API key is empty")
	}
	if strings.TrimSpace(spec.From) == "" {
		return nil, errors.New("email: from address is empty")
	}
	if len(spec.To) == 0 {
		return nil, errors.New("email: no recipients configured")
	}
	client := resend.NewClient(spec.APIKey)
	return newWithSender(client.Emails, spec, log), nil
}

func (n *Notifier) Type() string { return "email" }

func (n *Notifier) Send(ctx context.Context, note domain.Notification) error {
	rendered, err := notiftpl.Render(note, n.spec.SubjectPrefix)
	if err != nil {
		return err
	}
	params := &resend.SendEmailRequest{
		From:    n.spec.From,
		To:      n.spec.To,
		Cc:      n.spec.Cc,
		Subject: rendered.Subject,
		Html:    rendered.HTML,
		Text:    rendered.Text,
	}
	resp, err := n.sender.SendWithContext(ctx, params)
	if err != nil {
		// The watcher relies on this error to avoid committing seen IDs, so the
		// failed notification is retried on the next poll cycle.
		return fmt.Errorf("email: resend send: %w", err)
	}
	id := ""
	if resp != nil {
		id = resp.Id
	}
	n.log.Info("notification email sent",
		"kind", note.Kind, "items", len(note.Items), "subject", rendered.Subject, "resend_id", id)
	return nil
}
