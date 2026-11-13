//go:build webui

package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/source"
)

type userAccumulator struct {
	digest  []domain.ReleaseItem
	perFeed map[string][]domain.ReleaseItem
	status  []domain.ReleaseItem
	hasNew  bool
	always  bool
}

// RunOnce performs one poll cycle:
//  1. load the live subscription pool and deduplicate by normalized URL key;
//  2. fetch each URL exactly once (through the shared pace.Gate);
//  3. immediately commit global seen IDs / ETags, independent of delivery;
//  4. apply each user's own filter, diff against that user's baseline;
//  5. render once per user notification and materialize it for the delivery
//     planner (which runs separately and may fail without blocking the seen IDs).
func (o *Orchestrator) RunOnce(ctx context.Context) error {
	if o.opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.opt.Timeout)
		defer cancel()
	}
	cycleID := "cycle-" + randomHex(8)
	nowTime := o.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	db := o.st.DB()

	subs, err := loadSubscriptions(db)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	channelIDs, err := loadChannelIDs(db)
	if err != nil {
		return err
	}
	seen, err := loadGlobalSeen(db)
	if err != nil {
		return fmt.Errorf("orchestrator: load global seen: %w", err)
	}
	userSeen, err := loadUserSeen(db)
	if err != nil {
		return fmt.Errorf("orchestrator: load user seen: %w", err)
	}

	// One fetch per URL, regardless of how many users subscribe to it.
	unique := make(map[string]Subscription)
	order := make([]string, 0, len(subs))
	for _, s := range subs {
		if _, ok := unique[s.URLKey]; !ok {
			unique[s.URLKey] = s
			order = append(order, s.URLKey)
		}
	}

	results := make(map[string]source.Result)
	for _, key := range order {
		entry := unique[key]
		var release func()
		if o.gate != nil {
			release, err = o.gate.Acquire(ctx)
			if err != nil {
				return err
			}
		}
		res, fetchErr := o.fetcher.Fetch(ctx, entry.URL, source.Cursor{
			ETag: entry.PoolETag, LastModified: entry.PoolLastModified,
		})
		if release != nil {
			release()
		}
		if fetchErr != nil {
			o.log.Warn("webui: feed fetch failed; state left untouched for retry",
				"url", entry.URL, "error", fetchErr)
			continue
		}
		results[key] = res
	}

	acc := make(map[int64]*userAccumulator)
	userSeenAdds := make(map[int64]map[string][]string)
	for _, s := range subs {
		res, ok := results[s.URLKey]
		if !ok {
			continue // failed fetch: do not advance this user's baseline
		}
		flt, err := filterFor(s.FilterJSON)
		if err != nil {
			o.log.Warn("webui: invalid subscription filter", "id", s.ID, "error", err)
			continue
		}
		var releases []domain.Release
		if !res.NotModified {
			releases = flt.Apply(res.Releases)
		}
		already := userSeen[s.UserID][s.URLKey]
		if already == nil {
			already = map[string]struct{}{}
		}
		var newForUser []domain.Release
		ids := make([]string, 0, len(releases))
		for _, r := range releases {
			if r.ID == "" {
				continue
			}
			ids = append(ids, r.ID)
			if _, seenBefore := already[r.ID]; !seenBefore {
				newForUser = append(newForUser, r)
			}
		}
		if userSeenAdds[s.UserID] == nil {
			userSeenAdds[s.UserID] = make(map[string][]string)
		}
		userSeenAdds[s.UserID][s.URLKey] = append(userSeenAdds[s.UserID][s.URLKey], ids...)

		a := acc[s.UserID]
		if a == nil {
			a = &userAccumulator{perFeed: make(map[string][]domain.ReleaseItem)}
			acc[s.UserID] = a
		}
		if len(newForUser) > 0 {
			a.hasNew = true
			for _, r := range newForUser {
				item := releaseItem(r, s.DisplayName, true)
				if s.Mode == "per_feed" {
					a.perFeed[s.DisplayName] = append(a.perFeed[s.DisplayName], item)
				} else {
					a.digest = append(a.digest, item)
				}
			}
			continue
		}
		if s.Policy == "always" {
			a.always = true
			version := latestStable(releases)
			if version == "" {
				version = s.PoolLastVersion
			}
			if version == "" {
				version = "未知"
			}
			name := s.DisplayName
			if name == "" {
				name = s.URL
			}
			a.status = append(a.status, domain.ReleaseItem{Feed: name, Version: version, URL: s.URL})
		}
	}

	// Render once per notification, not once per channel or recipient.
	rendered := make([]renderedNotification, 0)
	users := make([]int64, 0, len(acc))
	for userID := range acc {
		users = append(users, userID)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for _, userID := range users {
		a := acc[userID]
		chIDs := channelIDs[userID]
		if len(chIDs) == 0 {
			continue
		}
		notes := make([]domain.Notification, 0)
		if a.hasNew {
			if len(a.digest) > 0 {
				notes = append(notes, domain.Notification{
					Kind: domain.KindNewReleases, GeneratedAt: nowTime, Items: a.digest,
				})
			}
			names := make([]string, 0, len(a.perFeed))
			for name := range a.perFeed {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				notes = append(notes, domain.Notification{
					Kind: domain.KindNewReleases, GeneratedAt: nowTime,
					Items: a.perFeed[name], Scope: name,
				})
			}
		} else if a.always && len(a.status) > 0 {
			// No updates: an `always` user gets one per-cycle status summary.
			// The baseline template lists the current latest version of every
			// subscription, which is exactly what a status summary needs.
			notes = append(notes, domain.Notification{
				Kind: domain.KindBaseline, GeneratedAt: nowTime, Items: a.status,
			})
		}
		for _, note := range notes {
			r, err := materialize(note, o.opt.SubjectPrefix, userID, chIDs)
			if err != nil {
				return err
			}
			rendered = append(rendered, r)
		}
	}

	// Global fetch state, per-user baselines, notifications and the cycle record
	// share one transaction. Delivery is a separate, retryable phase.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := commitFetch(tx, now, subs, results, seen); err != nil {
		return err
	}
	for userID, byKey := range userSeenAdds {
		for key, ids := range byKey {
			if err := insertUserSeen(tx, userID, key, ids, now); err != nil {
				return fmt.Errorf("orchestrator: commit user seen: %w", err)
			}
		}
	}
	for _, r := range rendered {
		if _, err := insertNotification(tx, cycleID, r, now); err != nil {
			return fmt.Errorf("orchestrator: materialize notification: %w", err)
		}
	}
	if err := insertCycle(tx, cycleID, now, len(order), len(rendered)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	o.log.Info("webui: poll cycle finished", "cycle", cycleID,
		"urls", len(order), "fetched", len(results), "notifications", len(rendered))
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
