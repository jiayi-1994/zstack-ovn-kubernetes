// Package logging provides structured logging for zstack-ovn-kubernetes.
package logging

import (
	"context"

	"github.com/go-logr/logr"
)

// contextKey is the type for context keys
type contextKey string

// loggerKey is the context key for the logger
const loggerKey contextKey = "logger"

// FromContext returns the logger from the context
// If no logger is found, returns the global logger
func FromContext(ctx context.Context) *Logger {
	if ctx == nil {
		return GetGlobalLogger()
	}

	if logger, ok := ctx.Value(loggerKey).(*Logger); ok {
		return logger
	}

	return GetGlobalLogger()
}

// IntoContext returns a new context with the logger
func IntoContext(ctx context.Context, logger *Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LogrFromContext returns a logr.Logger from the context
// This is useful for controller-runtime compatibility
func LogrFromContext(ctx context.Context) logr.Logger {
	return FromContext(ctx).Logger()
}

// WithContext returns a new logger with context-specific values
// This is useful for adding request-specific information to logs
func WithContext(ctx context.Context, keysAndValues ...interface{}) *Logger {
	return FromContext(ctx).WithValues(keysAndValues...)
}

// ContextWithLogger creates a new context with a named logger
// This is useful for creating component-specific loggers
func ContextWithLogger(ctx context.Context, name string) context.Context {
	logger := FromContext(ctx).WithName(name)
	return IntoContext(ctx, logger)
}

// LoggerForController returns a logger configured for a specific controller
// Adds standard controller-related fields
func LoggerForController(name string) *Logger {
	return GetGlobalLogger().WithName(name).WithValues(
		"controller", name,
	)
}

// LoggerForPod returns a logger with Pod-specific fields
func LoggerForPod(namespace, name string) *Logger {
	return GetGlobalLogger().WithValues(
		"namespace", namespace,
		"pod", name,
	)
}

// LoggerForSubnet returns a logger with Subnet-specific fields
func LoggerForSubnet(name string) *Logger {
	return GetGlobalLogger().WithValues(
		"subnet", name,
	)
}

// LoggerForService returns a logger with Service-specific fields
func LoggerForService(namespace, name string) *Logger {
	return GetGlobalLogger().WithValues(
		"namespace", namespace,
		"service", name,
	)
}

// LoggerForNode returns a logger with Node-specific fields
func LoggerForNode(name string) *Logger {
	return GetGlobalLogger().WithValues(
		"node", name,
	)
}

// LoggerForOVN returns a logger for OVN operations
func LoggerForOVN(operation string) *Logger {
	return GetGlobalLogger().WithName("ovn").WithValues(
		"operation", operation,
	)
}

// LoggerForCNI returns a logger for CNI operations
func LoggerForCNI(operation string) *Logger {
	return GetGlobalLogger().WithName("cni").WithValues(
		"operation", operation,
	)
}
