package notifier

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"feedwatch/internal/domain"
)

type fakeNotifier struct {
	typ   string
	err   error
	calls int
}

func (f *fakeNotifier) Type() string { return f.typ }
func (f *fakeNotifier) Send(context.Context, domain.Notification) error {
	f.calls++
	return f.err
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func note() domain.Notification { return domain.Notification{Kind: domain.KindNewReleases} }

func TestMultiBestEffort(t *testing.T) {
	failErr := errors.New("channel down")
	cases := []struct {
		name     string
		errs     []error
		policy   FailurePolicy
		wantErr  bool
		wantCall int
	}{
		{"all succeed", []error{nil, nil, nil}, BestEffort, false, 3},
		{"one fails", []error{nil, failErr, nil}, BestEffort, false, 3},
		{"all fail", []error{failErr, failErr}, BestEffort, true, 2},
		{"single fails (legacy semantics)", []error{failErr}, BestEffort, true, 1},
		{"strict with one failure", []error{nil, failErr}, Strict, true, 2},
		{"strict all succeed", []error{nil, nil}, Strict, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			children := make([]Notifier, 0, len(tc.errs))
			fakes := make([]*fakeNotifier, 0, len(tc.errs))
			for i, e := range tc.errs {
				f := &fakeNotifier{typ: "ch" + string(rune('a'+i)), err: e}
				fakes = append(fakes, f)
				children = append(children, f)
			}
			m, err := NewMulti(children, tc.policy, quiet())
			if err != nil {
				t.Fatal(err)
			}
			got := m.Send(context.Background(), note())
			if (got != nil) != tc.wantErr {
				t.Fatalf("Send error = %v, wantErr=%v", got, tc.wantErr)
			}
			called := 0
			for _, f := range fakes {
				called += f.calls
				if f.calls != 1 {
					t.Errorf("%s called %d times, want 1", f.typ, f.calls)
				}
			}
			if called != tc.wantCall {
				t.Errorf("total calls = %d, want %d", called, tc.wantCall)
			}
			if got != nil && !strings.Contains(got.Error(), "channel down") {
				t.Errorf("joined error lost the cause: %v", got)
			}
		})
	}
}

func TestMultiRejectsInvalidInput(t *testing.T) {
	if _, err := NewMulti(nil, BestEffort, quiet()); err == nil {
		t.Error("empty children must fail")
	}
	if _, err := NewMulti([]Notifier{&fakeNotifier{typ: "x"}}, FailurePolicy("bogus"), quiet()); err == nil {
		t.Error("unknown policy must fail")
	}
	if p, err := ParseFailurePolicy(""); err != nil || p != BestEffort {
		t.Errorf("empty policy should default to best_effort: %v %v", p, err)
	}
	if _, err := ParseFailurePolicy("whatever"); err == nil {
		t.Error("unknown policy string must fail")
	}
}

func TestMultiStopsOnCanceledContext(t *testing.T) {
	a := &fakeNotifier{typ: "a"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, _ := NewMulti([]Notifier{a}, BestEffort, quiet())
	if err := m.Send(ctx, note()); err == nil {
		t.Fatal("canceled context must produce an error")
	}
	if a.calls != 0 {
		t.Errorf("no channel should be called after cancellation, got %d", a.calls)
	}
}

func TestRegistryTypesAreSorted(t *testing.T) {
	r := NewRegistry()
	r.Register("z", nil)
	r.Register("a", nil)
	got := r.Types()
	if len(got) != 2 || got[0] != "a" || got[1] != "z" {
		t.Errorf("Types() = %v", got)
	}
	if _, err := r.Build(NotifierSpec{Type: "missing"}, quiet()); !errors.Is(err, ErrUnknownType) {
		t.Errorf("expected ErrUnknownType, got %v", err)
	}
}
