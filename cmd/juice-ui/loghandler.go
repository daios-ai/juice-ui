package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// newLogHandler returns the colored console handler when w is a terminal, else the
// plain text handler — so redirected logs and files stay ANSI-free and grep-friendly.
func newLogHandler(w *os.File) slog.Handler {
	if fi, err := w.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return newConsoleHandler(w, slog.LevelInfo, true)
	}
	return slog.NewTextHandler(w, nil)
}

// consoleHandler is a compact, colored slog.Handler for the terminal, matching the
// kernel's console style: "HH:MM:SS LVL msg key=val …". The level tag is colored
// when color is set (a TTY); otherwise the same layout is written without ANSI, so
// log files stay clean. juice-ui logs are flat, so groups are a no-op.
type consoleHandler struct {
	w     io.Writer
	mu    *sync.Mutex // shared across WithAttrs clones so writes never interleave
	level slog.Leveler
	color bool
	attrs string // preformatted " key=val" carried from WithAttrs
}

func newConsoleHandler(w io.Writer, level slog.Leveler, color bool) *consoleHandler {
	return &consoleHandler{w: w, mu: &sync.Mutex{}, level: level, color: color}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

// levelTag maps a level to its 3-letter tag and ANSI color (dim/green/yellow/red).
var levelTag = map[slog.Level][2]string{
	slog.LevelDebug: {"DBG", "\x1b[2m"},
	slog.LevelInfo:  {"INF", "\x1b[32m"},
	slog.LevelWarn:  {"WRN", "\x1b[33m"},
	slog.LevelError: {"ERR", "\x1b[31m"},
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteByte(' ')
	tag, ok := levelTag[r.Level]
	if !ok {
		tag = [2]string{r.Level.String(), ""}
	}
	if h.color && tag[1] != "" {
		b.WriteString(tag[1])
		b.WriteString(tag[0])
		b.WriteString("\x1b[0m")
	} else {
		b.WriteString(tag[0])
	}
	b.WriteByte(' ')
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// writeAttr appends " key=val", quoting values that contain spaces or quotes.
func writeAttr(b *strings.Builder, a slog.Attr) {
	b.WriteByte(' ')
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \t\"") {
		b.WriteString(strconv.Quote(v))
	} else {
		b.WriteString(v)
	}
}

func (h *consoleHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range as {
		writeAttr(&b, a)
	}
	nh := *h
	nh.attrs = b.String()
	return &nh
}

func (h *consoleHandler) WithGroup(string) slog.Handler { return h }
