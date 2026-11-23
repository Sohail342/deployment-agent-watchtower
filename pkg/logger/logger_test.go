package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf)
	log.Info("pull", slog.String("password", "super-secret"), slog.String("image", "nginx:alpine"))
	out := buf.String()
	if strings.Contains(out, "super-secret") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected redaction, got %s", out)
	}
	if !strings.Contains(out, "nginx:alpine") {
		t.Fatalf("expected image to remain, got %s", out)
	}
}
