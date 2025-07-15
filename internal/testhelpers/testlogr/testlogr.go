package testlogr

import (
	"testing"

	"github.com/go-logr/logr"
)

// New creates a logger that writes to test output.
func New(t testing.TB) logr.Logger {
	t.Helper()
	return logr.New(&sink{t, nil})
}

// TODO(jlegrone): Refactor this after https://github.com/golang/go/issues/59928 is released.
type sink struct {
	t             testing.TB
	keysAndValues []any
}

func (s *sink) Init(info logr.RuntimeInfo) {
	// pass
}

func (s *sink) Enabled(level int) bool {
	// log everything
	return true
}

func (s *sink) Info(level int, msg string, keysAndValues ...any) {
	s.t.Helper()
	s.log("INFO", msg, keysAndValues...)
}

func (s *sink) Error(err error, msg string, keysAndValues ...any) {
	s.t.Helper()
	s.log("ERROR", msg+" "+err.Error(), keysAndValues...)
}

func (s *sink) WithValues(keysAndValues ...any) logr.LogSink {
	s.t.Helper()
	return &sink{
		t:             s.t,
		keysAndValues: append(s.keysAndValues, keysAndValues...),
	}
}

func (s *sink) WithName(name string) logr.LogSink {
	s.t.Helper()
	return s
}

func (s *sink) log(level string, msg string, keysAndValues ...any) {
	s.t.Helper()
	args := append([]any{level, msg}, s.keysAndValues...)
	args = append(args, keysAndValues...)
	s.t.Log(args...)
}
