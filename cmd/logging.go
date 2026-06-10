// Logging setup for qwash.
//
// qwash historically wrote diagnostics with log.Printf("[INFO] ...") and
// fatal errors with log.Fatalf. We replace that with slog backed by a small
// custom handler that preserves the familiar "[LEVEL] message" output while
// giving us proper level filtering, a single source of truth for warn/error
// formatting, and a clear path toward structured/JSON logging later.
//
// Diagnostics (this file) go to stderr; the program's actual product — bloat
// reports, progress lines, interactive prompts — stays on stdout via fmt.
package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// prettyHandler renders a slog.Record as "[LEVEL] message key=value..."
// followed by a newline. Attributes are appended in the order they were
// added; groups are flattened into "group.key=value".
type prettyHandler struct {
	mu     *sync.Mutex
	out    io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
}

func newPrettyHandler(out io.Writer, level slog.Leveler) *prettyHandler {
	return &prettyHandler{
		mu:    &sync.Mutex{},
		out:   out,
		level: level,
	}
}

func (h *prettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", levelString(r.Level), r.Message)

	prefix := strings.Join(h.groups, ".")
	for _, a := range h.attrs {
		writeAttr(&b, prefix, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, prefix, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	fmt.Fprintf(b, " %s=%v", key, a.Value.Any())
}

// levelString maps slog levels to the level names qwash has always printed
// ([INFO]/[WARNING]/[ERROR]) so existing output and documentation stay valid.
// slog's own String() would yield "WARN" instead of "WARNING".
func levelString(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// logLevel is a mutable slog.Leveler so the active level can change after flag
// parsing. The default is Warn so INFO diagnostics stay quiet unless the user
// passes --verbose; warnings and errors are always shown.
var logLevel = new(slog.LevelVar)

// initLogger installs the pretty handler as slog's default. The active level
// is read from logLevel on every record, so setLogLevel can flip it after the
// flags have been parsed.
func initLogger() {
	logLevel.Set(slog.LevelWarn)
	slog.SetDefault(slog.New(newPrettyHandler(os.Stderr, logLevel)))
}

// setLogLevel changes the package-level log filter at runtime.
func setLogLevel(level slog.Level) {
	logLevel.Set(level)
}

// fatal logs an error-level diagnostic and exits with status 1. It is the
// slog-based replacement for log.Fatalf.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
