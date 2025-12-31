// Package logging provides structured logging for zstack-ovn-kubernetes.
//
// This package wraps the zap logger with the logr interface for compatibility
// with controller-runtime. It supports:
// - JSON and text output formats
// - Dynamic log level adjustment
// - Structured key-value logging
// - Context-aware logging
//
// Log Levels:
// - debug: Detailed debugging information
// - info: General operational information
// - warn: Warning messages for potentially harmful situations
// - error: Error messages for serious problems
//
// Usage:
//
//	logger := logging.NewLogger(logging.Options{
//	    Level:  "info",
//	    Format: "json",
//	})
//	logger.Info("Starting controller", "version", "1.0.0")
//	logger.Error(err, "Failed to connect", "address", "tcp:192.168.1.100:6641")
//
// Reference: OVN-Kubernetes uses klog, but we use zap for better structured logging
package logging

import (
	"os"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Log level constants
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Log format constants
const (
	FormatJSON = "json"
	FormatText = "text"
)

// Options contains configuration options for the logger
type Options struct {
	// Level is the log level: debug, info, warn, error
	// Default: info
	Level string

	// Format is the log format: json or text
	// Default: json
	Format string

	// OutputPath is the output file path
	// If empty, logs to stdout
	OutputPath string

	// Development enables development mode with more verbose output
	// Default: false
	Development bool

	// AddCaller adds caller information to log entries
	// Default: true
	AddCaller bool

	// CallerSkip is the number of stack frames to skip when determining caller
	// Default: 1
	CallerSkip int
}

// DefaultOptions returns default logging options
func DefaultOptions() Options {
	return Options{
		Level:      LevelInfo,
		Format:     FormatJSON,
		AddCaller:  true,
		CallerSkip: 1,
	}
}

// Logger wraps a zap logger with dynamic level support
type Logger struct {
	// zapLogger is the underlying zap logger
	zapLogger *zap.Logger

	// atomicLevel allows dynamic level changes
	atomicLevel zap.AtomicLevel

	// logr is the logr interface for controller-runtime compatibility
	logr logr.Logger

	// mu protects concurrent access
	mu sync.RWMutex
}

// globalLogger is the global logger instance
var (
	globalLogger atomic.Value
	initOnce     sync.Once
)

// NewLogger creates a new logger with the given options
//
// Parameters:
//   - opts: Logger configuration options
//
// Returns:
//   - *Logger: Configured logger instance
//   - error: Configuration error
func NewLogger(opts Options) (*Logger, error) {
	// Parse log level
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	// Create atomic level for dynamic adjustment
	atomicLevel := zap.NewAtomicLevelAt(level)

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create encoder based on format
	var encoder zapcore.Encoder
	if opts.Format == FormatText {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	}

	// Create output writer
	var output zapcore.WriteSyncer
	if opts.OutputPath != "" {
		file, err := os.OpenFile(opts.OutputPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		output = zapcore.AddSync(file)
	} else {
		output = zapcore.AddSync(os.Stdout)
	}

	// Create core
	core := zapcore.NewCore(encoder, output, atomicLevel)

	// Build zap options
	zapOpts := []zap.Option{}
	if opts.AddCaller {
		zapOpts = append(zapOpts, zap.AddCaller())
		if opts.CallerSkip > 0 {
			zapOpts = append(zapOpts, zap.AddCallerSkip(opts.CallerSkip))
		}
	}
	if opts.Development {
		zapOpts = append(zapOpts, zap.Development())
	}

	// Create zap logger
	zapLogger := zap.New(core, zapOpts...)

	// Create logr wrapper
	logrLogger := zapr.NewLogger(zapLogger)

	return &Logger{
		zapLogger:   zapLogger,
		atomicLevel: atomicLevel,
		logr:        logrLogger,
	}, nil
}

// parseLevel parses a string log level to zapcore.Level
func parseLevel(level string) (zapcore.Level, error) {
	switch level {
	case LevelDebug:
		return zapcore.DebugLevel, nil
	case LevelInfo:
		return zapcore.InfoLevel, nil
	case LevelWarn:
		return zapcore.WarnLevel, nil
	case LevelError:
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, nil
	}
}

// SetLevel dynamically changes the log level
//
// Parameters:
//   - level: New log level (debug/info/warn/error)
//
// Returns:
//   - error: If the level string is invalid
func (l *Logger) SetLevel(level string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	zapLevel, err := parseLevel(level)
	if err != nil {
		return err
	}

	l.atomicLevel.SetLevel(zapLevel)
	return nil
}

// GetLevel returns the current log level
func (l *Logger) GetLevel() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	switch l.atomicLevel.Level() {
	case zapcore.DebugLevel:
		return LevelDebug
	case zapcore.InfoLevel:
		return LevelInfo
	case zapcore.WarnLevel:
		return LevelWarn
	case zapcore.ErrorLevel:
		return LevelError
	default:
		return LevelInfo
	}
}

// Logger returns the logr.Logger interface for controller-runtime compatibility
func (l *Logger) Logger() logr.Logger {
	return l.logr
}

// ZapLogger returns the underlying zap.Logger
func (l *Logger) ZapLogger() *zap.Logger {
	return l.zapLogger
}

// WithName returns a new logger with the given name
func (l *Logger) WithName(name string) *Logger {
	return &Logger{
		zapLogger:   l.zapLogger.Named(name),
		atomicLevel: l.atomicLevel,
		logr:        l.logr.WithName(name),
	}
}

// WithValues returns a new logger with the given key-value pairs
func (l *Logger) WithValues(keysAndValues ...interface{}) *Logger {
	fields := make([]zap.Field, 0, len(keysAndValues)/2)
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 < len(keysAndValues) {
			key, ok := keysAndValues[i].(string)
			if ok {
				fields = append(fields, zap.Any(key, keysAndValues[i+1]))
			}
		}
	}

	return &Logger{
		zapLogger:   l.zapLogger.With(fields...),
		atomicLevel: l.atomicLevel,
		logr:        l.logr.WithValues(keysAndValues...),
	}
}

// Debug logs a debug message with key-value pairs
func (l *Logger) Debug(msg string, keysAndValues ...interface{}) {
	l.logr.V(1).Info(msg, keysAndValues...)
}

// Info logs an info message with key-value pairs
func (l *Logger) Info(msg string, keysAndValues ...interface{}) {
	l.logr.Info(msg, keysAndValues...)
}

// Warn logs a warning message with key-value pairs
func (l *Logger) Warn(msg string, keysAndValues ...interface{}) {
	// logr doesn't have a Warn method, use Info with a warning indicator
	l.zapLogger.Warn(msg, toZapFields(keysAndValues)...)
}

// Error logs an error message with key-value pairs
func (l *Logger) Error(err error, msg string, keysAndValues ...interface{}) {
	l.logr.Error(err, msg, keysAndValues...)
}

// V returns a logger at the specified verbosity level
// Higher values are more verbose (debug)
func (l *Logger) V(level int) logr.Logger {
	return l.logr.V(level)
}

// Sync flushes any buffered log entries
func (l *Logger) Sync() error {
	return l.zapLogger.Sync()
}

// toZapFields converts key-value pairs to zap fields
func toZapFields(keysAndValues []interface{}) []zap.Field {
	fields := make([]zap.Field, 0, len(keysAndValues)/2)
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 < len(keysAndValues) {
			key, ok := keysAndValues[i].(string)
			if ok {
				fields = append(fields, zap.Any(key, keysAndValues[i+1]))
			}
		}
	}
	return fields
}

// InitGlobalLogger initializes the global logger
// This should be called once at application startup
func InitGlobalLogger(opts Options) error {
	var initErr error
	initOnce.Do(func() {
		logger, err := NewLogger(opts)
		if err != nil {
			initErr = err
			return
		}
		globalLogger.Store(logger)
	})
	return initErr
}

// GetGlobalLogger returns the global logger instance
// Returns a no-op logger if not initialized
func GetGlobalLogger() *Logger {
	if l := globalLogger.Load(); l != nil {
		return l.(*Logger)
	}
	// Return a default logger if not initialized
	logger, _ := NewLogger(DefaultOptions())
	return logger
}

// SetGlobalLogLevel dynamically changes the global log level
func SetGlobalLogLevel(level string) error {
	return GetGlobalLogger().SetLevel(level)
}

// L is a shorthand for GetGlobalLogger()
func L() *Logger {
	return GetGlobalLogger()
}
