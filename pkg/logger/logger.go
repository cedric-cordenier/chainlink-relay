package logger

import (
	"reflect"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

// Logger is a minimal subset of smartcontractkit/chainlink/core/logger.Logger implemented by go.uber.org/zap.SugaredLogger
type Logger interface {
	Name() string

	Debug(args ...interface{})
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Panic(args ...interface{})
	Fatal(args ...interface{})

	Debugf(format string, values ...interface{})
	Infof(format string, values ...interface{})
	Warnf(format string, values ...interface{})
	Errorf(format string, values ...interface{})
	Panicf(format string, values ...interface{})
	Fatalf(format string, values ...interface{})

	Debugw(msg string, keysAndValues ...interface{})
	Infow(msg string, keysAndValues ...interface{})
	Warnw(msg string, keysAndValues ...interface{})
	Errorw(msg string, keysAndValues ...interface{})
	Panicw(msg string, keysAndValues ...interface{})
	Fatalw(msg string, keysAndValues ...interface{})

	Sync() error
}

type Config struct {
	Level zapcore.Level
}

var defaultConfig Config

// New returns a new Logger with the default configuration.
func New() (Logger, error) { return defaultConfig.New() }

// New returns a new Logger for Config.
func (c *Config) New() (Logger, error) {
	return NewWith(func(cfg *zap.Config) {
		cfg.Level.SetLevel(c.Level)
	})
}

// NewWith returns a new Logger from a modified [zap.Config].
func NewWith(cfgFn func(*zap.Config)) (Logger, error) {
	cfg := zap.NewProductionConfig()
	cfgFn(&cfg)
	core, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	return &logger{core.Sugar(), ""}, nil
}

// Test returns a new test Logger for tb.
func Test(tb testing.TB) Logger {
	return &logger{zaptest.NewLogger(tb).Sugar(), ""}
}

// TestObserved returns a new test Logger for tb and ObservedLogs at the given Level.
func TestObserved(tb testing.TB, lvl zapcore.Level) (Logger, *observer.ObservedLogs) {
	sl, logs := testObserved(tb, lvl)
	return &logger{sl, ""}, logs
}

func testObserved(tb testing.TB, lvl zapcore.Level) (*zap.SugaredLogger, *observer.ObservedLogs) {
	oCore, logs := observer.New(lvl)
	observe := zap.WrapCore(func(c zapcore.Core) zapcore.Core {
		return zapcore.NewTee(c, oCore)
	})
	return zaptest.NewLogger(tb, zaptest.WrapOptions(observe)).Sugar(), logs
}

// Nop returns a no-op Logger.
func Nop() Logger {
	return &logger{zap.New(zapcore.NewNopCore()).Sugar(), ""}
}

type logger struct {
	*zap.SugaredLogger
	name string
}

func (l *logger) with(args ...interface{}) Logger {
	return &logger{l.SugaredLogger.With(args...), ""}
}

func joinName(old, new string) string {
	if old == "" {
		return new
	}
	return old + "." + new
}

func (l *logger) named(name string) Logger {
	newLogger := *l
	newLogger.name = joinName(l.name, name)
	newLogger.SugaredLogger = l.SugaredLogger.Named(name)
	return &newLogger
}

func (l *logger) Name() string {
	return l.name
}

func (l *logger) helper(skip int) Logger {
	return &logger{l.sugaredHelper(skip), l.name}
}

func (l *logger) sugaredHelper(skip int) *zap.SugaredLogger {
	return l.SugaredLogger.WithOptions(zap.AddCallerSkip(skip))
}

// With returns a Logger with keyvals, if 'l' has a method `With(...interface{}) L`, where L implements Logger, otherwise it returns l.
func With(l Logger, keyvals ...interface{}) Logger {
	switch t := l.(type) {
	case *logger:
		return t.with(keyvals...)
	}

	method := reflect.ValueOf(l).MethodByName("With")
	if method == (reflect.Value{}) {
		return l // not available
	}
	if ret := method.CallSlice([]reflect.Value{reflect.ValueOf(keyvals)}); len(ret) == 1 {
		nl, ok := ret[0].Interface().(Logger)
		if ok {
			return nl
		}
	}
	return l
}

// Named returns a logger with name 'n', if 'l' has a method `Named(string) L`, where L implements Logger, otherwise it returns l.
func Named(l Logger, n string) Logger {
	switch t := l.(type) {
	case *logger:
		return t.named(n)
	}

	method := reflect.ValueOf(l).MethodByName("Named")
	if method == (reflect.Value{}) {
		return l // not available
	}
	if ret := method.Call([]reflect.Value{reflect.ValueOf(n)}); len(ret) == 1 {
		nl, ok := ret[0].Interface().(Logger)
		if ok {
			return nl
		}
	}
	return l
}

// Helper returns a logger with 'skip' levels of callers skipped, if 'l' has a method `Helper(int) L`, where L implements Logger, otherwise it returns l.
// See [zap.AddCallerSkip]
func Helper(l Logger, skip int) Logger {
	switch t := l.(type) {
	case *logger:
		return t.helper(skip)
	}

	method := reflect.ValueOf(l).MethodByName("Helper")
	if method == (reflect.Value{}) {
		return l // not available
	}
	if ret := method.Call([]reflect.Value{reflect.ValueOf(skip)}); len(ret) == 1 {
		nl, ok := ret[0].Interface().(Logger)
		if ok {
			return nl
		}
	}
	return l
}

// Critical emits critical level logs (a remapping of [zap.DPanicLevel]) or falls back to error level with a '[crit]' prefix.
func Critical(l Logger, args ...interface{}) {
	switch t := l.(type) {
	case *logger:
		t.DPanic(args...)
		return
	}
	c, ok := l.(interface {
		Critical(args ...interface{})
	})
	if ok {
		c.Critical(args...)
		return
	}
	l.Error(append([]any{"[crit] "}, args...)...)
}

// Criticalf emits critical level logs (a remapping of [zap.DPanicLevel]) or falls back to error level with a '[crit]' prefix.
func Criticalf(l Logger, format string, values ...interface{}) {
	switch t := l.(type) {
	case *logger:
		t.DPanicf(format, values...)
		return
	}
	c, ok := l.(interface {
		Criticalf(format string, values ...interface{})
	})
	if ok {
		c.Criticalf(format, values...)
		return
	}
	l.Errorf("[crit] "+format, values...)
}

// Criticalw emits critical level logs (a remapping of [zap.DPanicLevel]) or falls back to error level with a '[crit]' prefix.
func Criticalw(l Logger, msg string, keysAndValues ...interface{}) {
	switch t := l.(type) {
	case *logger:
		t.DPanicw(msg, keysAndValues...)
		return
	}
	c, ok := l.(interface {
		Criticalw(msg string, keysAndValues ...interface{})
	})
	if ok {
		c.Criticalw(msg, keysAndValues...)
		return
	}
	l.Errorw("[crit] "+msg, keysAndValues...)
}
