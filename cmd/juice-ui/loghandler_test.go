package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestConsoleHandlerPlain(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newConsoleHandler(&buf, slog.LevelInfo, false))
	log.Info("request", "method", "GET", "path", "/run", "status", 200)
	out := buf.String()
	if !strings.Contains(out, " INF request ") {
		t.Errorf("missing compact level+msg: %q", out)
	}
	if !strings.Contains(out, "method=GET") || !strings.Contains(out, "path=/run") || !strings.Contains(out, "status=200") {
		t.Errorf("attrs not rendered as key=val: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("no-color mode must not emit ANSI: %q", out)
	}
	if len(out) < 9 || out[2] != ':' || out[5] != ':' {
		t.Errorf("expected HH:MM:SS prefix: %q", out)
	}
}

func TestConsoleHandlerColorAndThreshold(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newConsoleHandler(&buf, slog.LevelInfo, true))
	log.Warn("watch out")
	if !strings.Contains(buf.String(), "\x1b[33mWRN\x1b[0m") {
		t.Errorf("warn must be yellow-tagged: %q", buf.String())
	}
	buf.Reset()
	log.Debug("noise")
	if buf.Len() != 0 {
		t.Errorf("debug below the Info threshold must be suppressed: %q", buf.String())
	}
}

func TestConsoleHandlerWithAttrsAndQuoting(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(newConsoleHandler(&buf, slog.LevelInfo, false)).With("component", "server")
	base.Info("up", "addr", ":8080")
	if out := buf.String(); !strings.Contains(out, "component=server") || !strings.Contains(out, "addr=:8080") {
		t.Errorf("WithAttrs + record attrs both expected: %q", out)
	}
	buf.Reset()
	base.Info("msg", "reason", "kernel down")
	if !strings.Contains(buf.String(), `reason="kernel down"`) {
		t.Errorf("spaced value must be quoted: %q", buf.String())
	}
}
