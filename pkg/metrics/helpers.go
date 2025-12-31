// Package metrics provides Prometheus metrics for zstack-ovn-kubernetes.
package metrics

import (
	"time"
)

// Result constants for metric labels
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
	ResultRequeue = "requeue"
)

// Operation constants for CNI metrics
const (
	OperationAdd   = "add"
	OperationDel   = "del"
	OperationCheck = "check"
)

// Database constants for OVN metrics
const (
	DatabaseNB = "nb"
	DatabaseSB = "sb"
)

// Controller constants for controller metrics
const (
	ControllerSubnet  = "subnet"
	ControllerPod     = "pod"
	ControllerService = "service"
	ControllerPolicy  = "policy"
	ControllerNode    = "node"
)

// OVN operation constants
const (
	OVNOpCreateLS  = "create_ls"
	OVNOpDeleteLS  = "delete_ls"
	OVNOpGetLS     = "get_ls"
	OVNOpListLS    = "list_ls"
	OVNOpCreateLSP = "create_lsp"
	OVNOpDeleteLSP = "delete_lsp"
	OVNOpGetLSP    = "get_lsp"
	OVNOpCreateLB  = "create_lb"
	OVNOpUpdateLB  = "update_lb"
	OVNOpDeleteLB  = "delete_lb"
	OVNOpCreateACL = "create_acl"
	OVNOpDeleteACL = "delete_acl"
	OVNOpTransact  = "transact"
)

// Timer is a helper for measuring operation duration
type Timer struct {
	start time.Time
}

// NewTimer creates a new timer starting from now
func NewTimer() *Timer {
	return &Timer{start: time.Now()}
}

// ObserveDuration records the duration since the timer was created
// to the given histogram with the specified labels
func (t *Timer) ObserveDuration() time.Duration {
	return time.Since(t.start)
}

// RecordCNIOperation records a CNI operation metric
//
// Parameters:
//   - operation: The CNI operation (add/del/check)
//   - err: The error from the operation (nil for success)
//   - duration: The duration of the operation
func RecordCNIOperation(operation string, err error, duration time.Duration) {
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}

	PodNetworkConfigDuration.WithLabelValues(operation, result).Observe(duration.Seconds())
	PodNetworkConfigTotal.WithLabelValues(operation, result).Inc()
}

// RecordOVNOperation records an OVN database operation metric
//
// Parameters:
//   - operation: The OVN operation (create_ls/delete_ls/etc)
//   - err: The error from the operation (nil for success)
//   - duration: The duration of the operation
func RecordOVNOperation(operation string, err error, duration time.Duration) {
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}

	OVNOperationDuration.WithLabelValues(operation, result).Observe(duration.Seconds())
	OVNOperationTotal.WithLabelValues(operation, result).Inc()
}

// RecordOVNTransaction records an OVN transaction metric
//
// Parameters:
//   - duration: The duration of the transaction
func RecordOVNTransaction(duration time.Duration) {
	OVNTransactionDuration.Observe(duration.Seconds())
}

// SetDBConnectionStatus sets the database connection status
//
// Parameters:
//   - database: The database name (nb/sb)
//   - connected: Whether the database is connected
func SetDBConnectionStatus(database string, connected bool) {
	value := float64(0)
	if connected {
		value = 1
	}
	OVNDBConnectionStatus.WithLabelValues(database).Set(value)
}

// RecordDBReconnect records a database reconnection attempt
//
// Parameters:
//   - database: The database name (nb/sb)
//   - err: The error from the reconnection (nil for success)
func RecordDBReconnect(database string, err error) {
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}
	OVNDBReconnectTotal.WithLabelValues(database, result).Inc()
}

// RecordControllerReconcile records a controller reconciliation metric
//
// Parameters:
//   - controller: The controller name (subnet/pod/service/policy/node)
//   - result: The result of the reconciliation (success/failure/requeue)
//   - duration: The duration of the reconciliation
func RecordControllerReconcile(controller, result string, duration time.Duration) {
	ControllerReconcileDuration.WithLabelValues(controller, result).Observe(duration.Seconds())
	ControllerReconcileTotal.WithLabelValues(controller, result).Inc()
}

// SetControllerWorkQueueDepth sets the work queue depth for a controller
//
// Parameters:
//   - controller: The controller name (subnet/pod/service/policy/node)
//   - depth: The current queue depth
func SetControllerWorkQueueDepth(controller string, depth int) {
	ControllerWorkQueueDepth.WithLabelValues(controller).Set(float64(depth))
}

// UpdateSubnetIPStats updates the IP allocation statistics for a subnet
//
// Parameters:
//   - subnet: The subnet name
//   - available: The number of available IPs
//   - used: The number of used IPs
func UpdateSubnetIPStats(subnet string, available, used int) {
	IPAllocatorAvailableIPs.WithLabelValues(subnet).Set(float64(available))
	IPAllocatorUsedIPs.WithLabelValues(subnet).Set(float64(used))
}

// RecordIPAllocation records an IP allocation operation
//
// Parameters:
//   - subnet: The subnet name
//   - err: The error from the allocation (nil for success)
//   - duration: The duration of the allocation
func RecordIPAllocation(subnet string, err error, duration time.Duration) {
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}

	IPAllocationTotal.WithLabelValues(subnet, result).Inc()
	IPAllocationDuration.Observe(duration.Seconds())
}

// IncrementCNIRequestsInFlight increments the CNI requests in flight counter
func IncrementCNIRequestsInFlight() {
	CNIRequestsInFlight.Inc()
}

// DecrementCNIRequestsInFlight decrements the CNI requests in flight counter
func DecrementCNIRequestsInFlight() {
	CNIRequestsInFlight.Dec()
}

// DeleteSubnetMetrics removes metrics for a deleted subnet
//
// Parameters:
//   - subnet: The subnet name
func DeleteSubnetMetrics(subnet string) {
	IPAllocatorAvailableIPs.DeleteLabelValues(subnet)
	IPAllocatorUsedIPs.DeleteLabelValues(subnet)
}
