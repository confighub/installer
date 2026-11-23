// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"context"
	"io"
	"log"
	"log/slog"
	"strings"

	"github.com/confighub/sdk/core/function/handler"
)

// withFunctionLog returns ctx with the functions a render runs in this process
// logging to the Installer's ErrOut, rather than to slog's process-wide
// default, which the host owns.
func (i *Installer) withFunctionLog(ctx context.Context) context.Context {
	return handler.WithLogger(ctx, slog.New(newLineHandler(i.err)))
}

// lineHandler writes each record as slog's default logger does before a
// program replaces it, "2006/01/02 15:04:05 INFO msg key=value", so the
// installer CLI's output reads as it always has.
type lineHandler struct {
	out   *log.Logger
	attrs []slog.Attr
}

func newLineHandler(w io.Writer) *lineHandler {
	return &lineHandler{out: log.New(w, "", log.LstdFlags)}
}

func (h *lineHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *lineHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.String())
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	return h.out.Output(0, b.String())
}

func (h *lineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &lineHandler{out: h.out, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

// WithGroup is not needed by the functions' log, which has no groups.
func (h *lineHandler) WithGroup(string) slog.Handler { return h }
