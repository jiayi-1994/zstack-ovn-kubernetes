// Package ovndb provides OVN database error types.
//
// This file defines custom error types for OVN database operations.
// These errors provide detailed information about failures and can be
// used for error handling and logging.
package ovndb

import (
	"fmt"
)

// ConnectionError represents an OVN database connection error
type ConnectionError struct {
	// Address is the database address that failed to connect
	Address string

	// Cause is the underlying error
	Cause error

	// Retries is the number of retry attempts made
	Retries int
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("failed to connect to OVN DB at %s after %d retries: %v",
		e.Address, e.Retries, e.Cause)
}

func (e *ConnectionError) Unwrap() error {
	return e.Cause
}

// TransactionError represents an OVN database transaction error
type TransactionError struct {
	// Operation is the operation that failed
	Operation string

	// Cause is the underlying error
	Cause error

	// Details provides additional context
	Details string
}

func (e *TransactionError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("OVN transaction failed for %s: %v (%s)", e.Operation, e.Cause, e.Details)
	}
	return fmt.Sprintf("OVN transaction failed for %s: %v", e.Operation, e.Cause)
}

func (e *TransactionError) Unwrap() error {
	return e.Cause
}

// ObjectNotFoundError represents an error when an OVN object is not found
type ObjectNotFoundError struct {
	// ObjectType is the type of object (e.g., "LogicalSwitch", "LogicalSwitchPort")
	ObjectType string

	// ObjectName is the name or identifier of the object
	ObjectName string
}

func (e *ObjectNotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found", e.ObjectType, e.ObjectName)
}

// ObjectExistsError represents an error when an OVN object already exists
type ObjectExistsError struct {
	// ObjectType is the type of object
	ObjectType string

	// ObjectName is the name or identifier of the object
	ObjectName string
}

func (e *ObjectExistsError) Error() string {
	return fmt.Sprintf("%s %q already exists", e.ObjectType, e.ObjectName)
}

// ValidationError represents a validation error for OVN operations
type ValidationError struct {
	// Field is the field that failed validation
	Field string

	// Value is the invalid value
	Value interface{}

	// Message describes the validation failure
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for %s=%v: %s", e.Field, e.Value, e.Message)
}

// NewConnectionError creates a new ConnectionError
func NewConnectionError(address string, cause error, retries int) *ConnectionError {
	return &ConnectionError{
		Address: address,
		Cause:   cause,
		Retries: retries,
	}
}

// NewTransactionError creates a new TransactionError
func NewTransactionError(operation string, cause error, details string) *TransactionError {
	return &TransactionError{
		Operation: operation,
		Cause:     cause,
		Details:   details,
	}
}

// NewObjectNotFoundError creates a new ObjectNotFoundError
func NewObjectNotFoundError(objectType, objectName string) *ObjectNotFoundError {
	return &ObjectNotFoundError{
		ObjectType: objectType,
		ObjectName: objectName,
	}
}

// NewObjectExistsError creates a new ObjectExistsError
func NewObjectExistsError(objectType, objectName string) *ObjectExistsError {
	return &ObjectExistsError{
		ObjectType: objectType,
		ObjectName: objectName,
	}
}

// NewValidationError creates a new ValidationError
func NewValidationError(field string, value interface{}, message string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Value:   value,
		Message: message,
	}
}

// IsNotFound checks if an error is an ObjectNotFoundError
func IsNotFound(err error) bool {
	_, ok := err.(*ObjectNotFoundError)
	return ok
}

// IsExists checks if an error is an ObjectExistsError
func IsExists(err error) bool {
	_, ok := err.(*ObjectExistsError)
	return ok
}

// IsConnectionError checks if an error is a ConnectionError
func IsConnectionError(err error) bool {
	_, ok := err.(*ConnectionError)
	return ok
}

// IsTransactionError checks if an error is a TransactionError
func IsTransactionError(err error) bool {
	_, ok := err.(*TransactionError)
	return ok
}

// IsValidationError checks if an error is a ValidationError
func IsValidationError(err error) bool {
	_, ok := err.(*ValidationError)
	return ok
}
