//go:build webui

package orchestrator

import (
	"encoding/json"
	"fmt"

	"feedwatch/internal/domain"
	notiftpl "feedwatch/internal/notifier/template"
)

// Envelope is stored JSON in notifications.markdown. It carries everything the
// delivery planner needs (owner, target channels) plus the channel-neutral
// notification payload, so a feed response is rendered only once per user per
// cycle rather than once per recipient or per channel.
type Envelope struct {
	UserID     int64               `json:"user_id"`
	ChannelIDs []int64             `json:"channel_ids"`
	Note       domain.Notification `json:"note"`
}

type renderedNotification struct {
	Kind     string
	Subject  string
	Text     string
	HTML     string
	Markdown string
	Envelope Envelope
}

func materialize(note domain.Notification, prefix string, userID int64, channelIDs []int64) (renderedNotification, error) {
	r, err := notiftpl.Render(note, prefix)
	if err != nil {
		return renderedNotification{}, fmt.Errorf("orchestrator: render: %w", err)
	}
	env := Envelope{UserID: userID, ChannelIDs: channelIDs, Note: note}
	payload, err := json.Marshal(env)
	if err != nil {
		return renderedNotification{}, fmt.Errorf("orchestrator: encode envelope: %w", err)
	}
	return renderedNotification{
		Kind:     string(note.Kind),
		Subject:  r.Subject,
		Text:     r.Text,
		HTML:     r.HTML,
		Markdown: string(payload),
		Envelope: env,
	}, nil
}
