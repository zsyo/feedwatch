package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
)

// channelStub models one delivery channel inside a real Multi notifier.
type channelStub struct {
	fail  bool
	calls int
}

func (c *channelStub) Type() string { return "stub" }
func (c *channelStub) Send(context.Context, domain.Notification) error {
	c.calls++
	if c.fail {
		return errors.New("channel down")
	}
	return nil
}

func fanoutWatcher(t *testing.T, n notifier.Notifier, st *memStore) *Watcher {
	t.Helper()
	w := New(
		[]FeedTask{stableTask("golang/go", step{result: res(rel("id-v1", "v1.2.3", fixedNow))})},
		st, n,
		Options{Interval: 0, Timeout: 5 * time.Second, RunOnStart: true, NotifyMode: "digest"},
		quiet(),
	)
	w.now = func() time.Time { return fixedNow }
	return w
}

func TestFanOutBestEffortCommitsAndNeverDuplicates(t *testing.T) {
	good := &channelStub{}
	bad := &channelStub{fail: true}
	m, err := notifier.NewMulti([]notifier.Notifier{good, bad}, notifier.BestEffort, quiet())
	if err != nil {
		t.Fatal(err)
	}
	st := &memStore{}
	w := fanoutWatcher(t, m, st)
	if err := w.RunOnce(t.Context()); err != nil {
		t.Fatalf("best_effort should succeed when one channel delivers: %v", err)
	}
	if st.saveCalls != 1 {
		t.Fatalf("state must be committed, saveCalls=%d", st.saveCalls)
	}
	// Restart with no new releases: absolutely no channel may be called again.
	if err := w.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if good.calls != 1 || bad.calls != 1 {
		t.Errorf("duplicate push: good=%d bad=%d, want 1/1", good.calls, bad.calls)
	}
}

func TestFanOutStrictDoesNotCommitOnPartialFailure(t *testing.T) {
	good := &channelStub{}
	bad := &channelStub{fail: true}
	m, err := notifier.NewMulti([]notifier.Notifier{good, bad}, notifier.Strict, quiet())
	if err != nil {
		t.Fatal(err)
	}
	st := &memStore{}
	if err := fanoutWatcher(t, m, st).RunOnce(t.Context()); err == nil {
		t.Fatal("strict must propagate a partial failure")
	}
	if st.saveCalls != 0 {
		t.Errorf("state must NOT be committed, saveCalls=%d", st.saveCalls)
	}
}

func TestFanOutAllChannelsFailRetriesWholeBatch(t *testing.T) {
	a := &channelStub{fail: true}
	b := &channelStub{fail: true}
	m, err := notifier.NewMulti([]notifier.Notifier{a, b}, notifier.BestEffort, quiet())
	if err != nil {
		t.Fatal(err)
	}
	st := &memStore{}
	if err := fanoutWatcher(t, m, st).RunOnce(t.Context()); err == nil {
		t.Fatal("zero deliveries must be an error")
	}
	if st.saveCalls != 0 {
		t.Errorf("state must NOT be committed, saveCalls=%d", st.saveCalls)
	}
}
