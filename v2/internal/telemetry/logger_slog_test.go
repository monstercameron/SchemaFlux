package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// slogAt returns a slog logger that writes plain lines (no time) to the buffer
// at or above level.
func slogAt(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func TestFromSlogWritesThroughTheLoggerItIsGiven(t *testing.T) {
	cases := []struct {
		name    string
		level   slog.Level
		write   func(*Logger)
		written string
	}{
		{"a debug line at a debug threshold", slog.LevelDebug, func(l *Logger) { l.Debug("fine detail", "k", 1) }, "level=DEBUG msg=\"fine detail\" k=1\n"},
		{"an info line at an info threshold", slog.LevelInfo, func(l *Logger) { l.Info("started") }, "level=INFO msg=started\n"},
		{"a warn line at a warn threshold", slog.LevelWarn, func(l *Logger) { l.Warn("slow", "ms", 900) }, "level=WARN msg=slow ms=900\n"},
		{"an error line at an error threshold", slog.LevelError, func(l *Logger) { l.Error("failed", "op", "extract") }, "level=ERROR msg=failed op=extract\n"},
		{"an info line below a warn threshold writes nothing", slog.LevelWarn, func(l *Logger) { l.Info("quiet") }, ""},
		{"a debug line below an info threshold writes nothing", slog.LevelInfo, func(l *Logger) { l.Debug("quiet") }, ""},
		{"a warn line below an error threshold writes nothing", slog.LevelError, func(l *Logger) { l.Warn("quiet") }, ""},
		{"a line with a context goes the same way", slog.LevelInfo, func(l *Logger) { l.InfoContext(context.Background(), "ctx line") }, "level=INFO msg=\"ctx line\"\n"},
		{"fields added with WithFields come in key order", slog.LevelInfo, func(l *Logger) { l.WithFields(map[string]any{"b": 2, "a": 1}).Info("with fields") }, "level=INFO msg=\"with fields\" a=1 b=2\n"},
		{"an attribute group is kept", slog.LevelInfo, func(l *Logger) { l.Info("grouped", slog.Group("g", slog.Int("n", 3))) }, "level=INFO msg=grouped g.n=3\n"},
		{"two lines arrive in order", slog.LevelInfo, func(l *Logger) { l.Info("one"); l.Info("two") }, "level=INFO msg=one\nlevel=INFO msg=two\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.write(FromSlog(slogAt(&buf, tc.level)))
			if buf.String() != tc.written {
				t.Errorf("wrote %q, want %q", buf.String(), tc.written)
			}
		})
	}
}

func TestFromSlogKeepsNoHistoryAndOwnsNoOutput(t *testing.T) {
	var buf bytes.Buffer
	given := slogAt(&buf, slog.LevelDebug)
	l := FromSlog(given)
	if l == nil {
		t.Fatal("FromSlog returned nil")
	}

	t.Run("it is the slog logger it was given", func(t *testing.T) {
		if l.Logger != given {
			t.Errorf("the embedded logger is not the one given")
		}
	})
	t.Run("lines are not kept as entries", func(t *testing.T) {
		l.Warn("a line")
		if got := l.Entries(); len(got) != 0 {
			t.Errorf("Entries = %d, want none", len(got))
		}
	})
	t.Run("resetting the entries is harmless", func(t *testing.T) {
		l.ResetEntries()
	})
	t.Run("closing it closes nothing and reports nothing", func(t *testing.T) {
		if err := l.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
		before := buf.Len()
		l.Info("after close")
		if buf.Len() == before {
			t.Errorf("a line written after Close did not arrive")
		}
	})
	t.Run("the configuration is the zero one", func(t *testing.T) {
		if got := l.Config(); got.Capture || got.FilePath != "" || got.Output != nil {
			t.Errorf("Config = %+v, want the zero configuration", got)
		}
	})
	t.Run("setting the level changes nothing", func(t *testing.T) {
		var quiet bytes.Buffer
		held := FromSlog(slogAt(&quiet, slog.LevelError))
		held.SetLevel(DebugLevel)
		held.SetLevelString("debug")
		held.Info("still below the threshold")
		if quiet.Len() != 0 {
			t.Errorf("SetLevel reached a logger it does not own: %q", quiet.String())
		}
	})
	t.Run("the process logger is not replaced", func(t *testing.T) {
		before := GetLogger()
		FromSlog(slogAt(&bytes.Buffer{}, slog.LevelInfo))
		if GetLogger() != before {
			t.Errorf("FromSlog replaced the process logger")
		}
	})
	t.Run("two loggers over one slog logger share its output only", func(t *testing.T) {
		var shared bytes.Buffer
		a := FromSlog(slogAt(&shared, slog.LevelInfo))
		b := FromSlog(slogAt(&shared, slog.LevelInfo))
		a.Info("a")
		b.Info("b")
		if !strings.Contains(shared.String(), "msg=a") || !strings.Contains(shared.String(), "msg=b") {
			t.Errorf("a line was lost: %q", shared.String())
		}
	})
}

func TestFromSlogReportsTheLevelItWillWrite(t *testing.T) {
	cases := []struct {
		name  string
		level slog.Level
		want  LogLevel
	}{
		{"a debug threshold reads as debug", slog.LevelDebug, DebugLevel},
		{"a threshold below debug reads as debug", slog.LevelDebug - 4, DebugLevel},
		{"an info threshold reads as info", slog.LevelInfo, InfoLevel},
		{"a threshold between info and warn reads as warn", slog.LevelInfo + 1, WarnLevel},
		{"a warn threshold reads as warn", slog.LevelWarn, WarnLevel},
		{"an error threshold reads as error", slog.LevelError, ErrorLevel},
		{"a threshold between warn and error reads as error", slog.LevelWarn + 1, ErrorLevel},
		{"a threshold above error reads as fatal", slog.LevelError + 4, FatalLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromSlog(slogAt(&bytes.Buffer{}, tc.level)).Level(); got != tc.want {
				t.Errorf("Level = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFromSlogGivenNothingDiscards(t *testing.T) {
	l := FromSlog(nil)
	if l == nil || l.Logger == nil {
		t.Fatal("FromSlog(nil) did not return a usable logger")
	}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if l.Enabled(context.Background(), level) {
			t.Errorf("a logger made from nothing is enabled at %v", level)
		}
	}
	l.Error("goes nowhere")
}

func TestDiscardIsEnabledAtNoLevelAndWritesNowhere(t *testing.T) {
	l := Discard()
	if l == nil || l.Logger == nil {
		t.Fatal("Discard returned no logger")
	}

	levels := []struct {
		name  string
		level slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"a level far below debug", slog.Level(-100)},
		{"a level far above error", slog.Level(100)},
		{"the highest level there is", slog.Level(1<<31 - 1)},
		{"the lowest level there is", slog.Level(-1 << 31)},
	}
	for _, tc := range levels {
		t.Run("it is not enabled at "+tc.name, func(t *testing.T) {
			if l.Enabled(context.Background(), tc.level) {
				t.Errorf("enabled at %v", tc.level)
			}
		})
	}

	t.Run("its handler is the standard discarding one", func(t *testing.T) {
		if l.Handler() != slog.DiscardHandler {
			t.Errorf("handler = %T, want slog.DiscardHandler", l.Handler())
		}
	})
	t.Run("a line at every level is accepted and kept nowhere", func(t *testing.T) {
		l.Debug("d")
		l.Info("i")
		l.Warn("w")
		l.Error("e")
		if got := l.Entries(); len(got) != 0 {
			t.Errorf("Entries = %d, want none", len(got))
		}
	})
	t.Run("fields added to it leave it discarding", func(t *testing.T) {
		with := l.WithFields(map[string]any{"k": "v"})
		if with == nil || with.Enabled(context.Background(), slog.LevelError) {
			t.Errorf("a logger with fields is enabled")
		}
	})
	t.Run("it reports the highest level", func(t *testing.T) {
		if got := l.Level(); got != FatalLevel {
			t.Errorf("Level = %d, want %d", got, FatalLevel)
		}
	})
	t.Run("setting its level changes nothing", func(t *testing.T) {
		l.SetLevel(DebugLevel)
		if l.Enabled(context.Background(), slog.LevelError) {
			t.Errorf("SetLevel enabled a discarding logger")
		}
	})
	t.Run("closing it reports nothing", func(t *testing.T) {
		if err := l.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	t.Run("each call returns a logger of its own", func(t *testing.T) {
		if Discard() == Discard() {
			t.Errorf("two calls returned the same logger")
		}
	})
}
