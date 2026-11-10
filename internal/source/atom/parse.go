package atom

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"feedwatch/internal/domain"
)

type contentPolicy string

const (
	policyNone contentPolicy = "none"
	policyText contentPolicy = "text"
	policyHTML contentPolicy = "html"
)

func parsePolicy(s string) contentPolicy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none":
		return policyNone
	case "html":
		return policyHTML
	default:
		return policyText
	}
}

// ---------- XML shapes ----------

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Links     []atomLink `xml:"link"`
	Content   atomText   `xml:"content"`
	Summary   atomText   `xml:"summary"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Channel rssChan  `xml:"channel"`
}

type rssChan struct {
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	GUID        string `xml:"guid"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

// ---------- helpers ----------

var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	time.RFC822Z,
	time.RFC822,
	"2006-01-02",
}

// parseTime is deliberately forgiving: a bad date must not fail a whole poll.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func pickLink(links []atomLink) string {
	for _, l := range links {
		if l.Href != "" && (l.Rel == "alternate" || l.Rel == "") {
			return l.Href
		}
	}
	for _, l := range links {
		if l.Href != "" {
			return l.Href
		}
	}
	return ""
}

func stableID(id, url, title, updated string) string {
	if s := strings.TrimSpace(id); s != "" {
		return s
	}
	if s := strings.TrimSpace(url); s != "" {
		return s
	}
	sum := sha1.Sum([]byte(title + "|" + updated))
	return "sha1:" + hex.EncodeToString(sum[:])
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	whitespaceRe  = regexp.MustCompile(`\s+`)
	coreRe        = regexp.MustCompile(`\d+(?:\.\d+)+`)
)

const maxSummaryRunes = 200

func stripHTML(s string) string {
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = whitespaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > maxSummaryRunes {
		s = string(runes[:maxSummaryRunes]) + "…"
	}
	return s
}

func coreVersion(title string) string { return coreRe.FindString(title) }

func fillBody(r *domain.Release, raw string, policy contentPolicy) {
	if raw == "" {
		return
	}
	switch policy {
	case policyHTML:
		// Raw HTML is kept for future rich rendering; v1 templates use Summary.
		r.ContentHTML = raw
		r.Summary = stripHTML(raw)
	case policyText:
		r.Summary = stripHTML(raw)
	case policyNone:
		// deliberately empty
	}
}

func makeRelease(id, title, url, published, updated, body, feedName string, policy contentPolicy) domain.Release {
	upd := parseTime(updated)
	pub := parseTime(published)
	if pub.IsZero() {
		pub = upd
	}
	title = strings.TrimSpace(title)
	r := domain.Release{
		ID:          stableID(id, url, title, updated),
		Feed:        feedName,
		Title:       title,
		Version:     title,
		CoreVersion: coreVersion(title),
		URL:         strings.TrimSpace(url),
		PublishedAt: pub,
		UpdatedAt:   upd,
	}
	fillBody(&r, body, policy)
	return r
}

// ---------- dispatch ----------

func rootName(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local, nil
		}
	}
}

// parseFeed maps Atom 1.0 (and RSS 2.0) entries into the common domain model.
// Swapping this function for a third-party feed library is an internal change
// to this package only; no interface or other package needs to change.
func parseFeed(data []byte, feedName string, policy contentPolicy) ([]domain.Release, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("atom: empty feed body")
	}
	root, err := rootName(trimmed)
	if err != nil {
		return nil, fmt.Errorf("atom: detect feed root: %w", err)
	}
	switch strings.ToLower(root) {
	case "feed":
		return parseAtom(trimmed, feedName, policy)
	case "rss":
		return parseRSS(trimmed, feedName, policy)
	default:
		return nil, fmt.Errorf("atom: unsupported feed root <%s> (only Atom 1.0 and RSS 2.0)", root)
	}
}

func parseAtom(data []byte, feedName string, policy contentPolicy) ([]domain.Release, error) {
	var f atomFeed
	if err := xml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("atom: parse feed: %w", err)
	}
	out := make([]domain.Release, 0, len(f.Entries))
	for _, e := range f.Entries {
		body := e.Content.Text
		if strings.TrimSpace(body) == "" {
			body = e.Summary.Text
		}
		out = append(out, makeRelease(
			e.ID, e.Title, pickLink(e.Links), e.Published, e.Updated, body, feedName, policy,
		))
	}
	return out, nil
}

func parseRSS(data []byte, feedName string, policy contentPolicy) ([]domain.Release, error) {
	var doc rssDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("atom: parse rss: %w", err)
	}
	out := make([]domain.Release, 0, len(doc.Channel.Items))
	for _, it := range doc.Channel.Items {
		out = append(out, makeRelease(
			it.GUID, it.Title, it.Link, it.PubDate, "", it.Description, feedName, policy,
		))
	}
	return out, nil
}
