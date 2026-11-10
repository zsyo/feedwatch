package lognotify

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
)

func TestLogNotifierRendersWithoutCredentials(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	n, err := New(notifier.NotifierSpec{Type: "log", SubjectPrefix: "[FW]"}, log)
	if err != nil {
		t.Fatal(err)
	}
	if n.Type() != "log" {
		t.Errorf("Type() = %q", n.Type())
	}
	note := domain.Notification{
		Kind:  domain.KindBaseline,
		Items: []domain.ReleaseItem{{Feed: "golang/go", Version: "go1.27.3", URL: "https://example.com/r"}},
	}
	if err := n.Send(context.Background(), note); err != nil {
		t.Fatalf("Send: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "版本监控已启动") || !strings.Contains(out, "go1.27.3") {
		t.Errorf("preview did not include the rendered mail: %q", out)
	}
	if !strings.Contains(out, "no message was sent") {
		t.Errorf("preview should make clear nothing was delivered: %q", out)
	}
}

func TestLogNotifierRespectsCancelledContext(t *testing.T) {
	n, _ := New(notifier.NotifierSpec{Type: "log"}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Send(ctx, domain.Notification{Kind: domain.KindBaseline}); err == nil {
		t.Error("cancelled context should be honoured")
	}
}
