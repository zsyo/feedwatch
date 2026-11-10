// Package lognotify is a zero-network Notifier used for local development,
// -dry-run and as the smallest demonstration of a pluggable channel.
package lognotify

import (
	"context"
	"log/slog"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
	notiftpl "feedwatch/internal/notifier/template"
)

type Notifier struct {
	spec notifier.NotifierSpec
	log  *slog.Logger
}

func New(spec notifier.NotifierSpec, log *slog.Logger) (notifier.Notifier, error) {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{spec: spec, log: log}, nil
}

func (n *Notifier) Type() string { return "log" }

func (n *Notifier) Send(ctx context.Context, note domain.Notification) error {
	rendered, err := notiftpl.Render(note, n.spec.SubjectPrefix)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n.log.Info("===== FeedWatch email preview (no message was sent) =====",
		"kind", note.Kind, "items", len(note.Items), "subject", rendered.Subject, "text", rendered.Text)
	n.log.Debug("email html preview", "html", rendered.HTML)
	return nil
}
