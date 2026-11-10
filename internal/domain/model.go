// Package domain holds the implementation-neutral data model shared by every
// source, notifier and store. It must not import any other internal package.
package domain

import "time"

// SchemaVersion allows the state file format to evolve without breaking
// existing deployments.
const SchemaVersion = 1

// Release is one entry from a release feed, normalised into a common shape.
// ID is the deduplication key (the Atom <id> when available) and must be
// stable across polls.
type Release struct {
	ID          string
	Feed        string
	Title       string
	Version     string // display value, e.g. "go1.27.3" / "v3.15.2" / "1.99.1"
	CoreVersion string // numeric core, e.g. "1.27.3", used for sorting
	URL         string
	PublishedAt time.Time
	UpdatedAt   time.Time
	Summary     string
	ContentHTML string
	IsStable    bool
}

// EffectiveTime returns the best available timestamp for ordering.
func (r Release) EffectiveTime() time.Time {
	if !r.PublishedAt.IsZero() {
		return r.PublishedAt
	}
	return r.UpdatedAt
}

// FeedState is the persisted knowledge about a single configured feed.
type FeedState struct {
	SeenIDs      map[string]time.Time `json:"seen_ids"`
	ETag         string               `json:"etag,omitempty"`
	LastModified string               `json:"last_modified,omitempty"`
	// omitzero (Go 1.24+) is the correct "skip zero value" tag for structs:
	// omitempty never worked on time.Time, so this used to write
	// "0001-01-01T00:00:00Z" into the state file for untouched feeds.
	LastChecked time.Time `json:"last_checked,omitzero"`
	LastVersion string    `json:"last_version,omitempty"`
}

// SeenState is the whole persisted state, keyed by feed name.
type SeenState struct {
	SchemaVersion int                   `json:"schema_version"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
	Feeds         map[string]*FeedState `json:"feeds"`
}

// NewSeenState builds an empty state ready for a first run.
func NewSeenState() SeenState {
	now := time.Now().UTC()
	return SeenState{
		SchemaVersion: SchemaVersion,
		CreatedAt:     now,
		UpdatedAt:     now,
		Feeds:         make(map[string]*FeedState),
	}
}

// EnsureFeed returns the state for name, creating it when missing.
func (s *SeenState) EnsureFeed(name string) *FeedState {
	if s.Feeds == nil {
		s.Feeds = make(map[string]*FeedState)
	}
	fs := s.Feeds[name]
	if fs == nil {
		fs = &FeedState{SeenIDs: make(map[string]time.Time)}
		s.Feeds[name] = fs
	}
	if fs.SeenIDs == nil {
		fs.SeenIDs = make(map[string]time.Time)
	}
	return fs
}

// NotificationKind distinguishes the first-run snapshot from later release
// notifications, so a single template can render both.
type NotificationKind string

const (
	KindBaseline    NotificationKind = "baseline"
	KindNewReleases NotificationKind = "new_releases"
)

// ReleaseItem is one row in a notification.
type ReleaseItem struct {
	Feed        string
	Version     string
	URL         string
	PublishedAt time.Time
	IsNew       bool
}

// Notification is a channel-neutral payload. Each Notifier renders it in its
// own format; the watcher never knows how it is delivered.
type Notification struct {
	Kind        NotificationKind
	GeneratedAt time.Time
	Items       []ReleaseItem
}
