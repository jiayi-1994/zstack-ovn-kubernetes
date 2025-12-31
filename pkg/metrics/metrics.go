// Package metrics provides Prometheus metrics for zstack-ovn-kubernetes.
//
// This package exposes various metrics for monitoring the CNI plugin:
// - Pod network configuration latency
// - OVN database operation counts (success/failure)
// - Database connection status
// - IP allocation statistics
// - Controller reconciliation metrics
//
// Metrics are exposed via the /metrics endpoint on the controller's
// metrics server (default port 8080).
//
// Reference: OVN-Kubernetes pkg/metrics/
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	// Namespace is the Prometheus metrics namespace
	Namespace = "zstack_ovn_kubernetes"

	// Subsystem names for different metric categories
	SubsystemCNI        = "cni"
	SubsystemOVN        = "ovn"
	SubsystemController = "controller"
	SubsystemAllocator  = "allocator"
)

var (
	// registerOnce ensures metrics are registered only once
	registerOnce sync.Once

	// ---- CNI Metrics ----

	// PodNetworkConfigDuration measures the time taken to configure Pod network
	// Labels: operation (add/del/check), result (success/failure)
	PodNetworkConfigDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemCNI,
			Name:      "pod_network_config_duration_seconds",
			Help:      "Time taken to configure Pod network in seconds",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"operation", "result"},
	)

	// PodNetworkConfigTotal counts the total number of Pod network configurations
	// Labels: operation (add/del/check), result (success/failure)
	PodNetworkConfigTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemCNI,
			Name:      "pod_network_config_total",
			Help:      "Total number of Pod network configuration operations",
		},
		[]string{"operation", "result"},
	)

	// CNIRequestsInFlight tracks the number of CNI requests currently being processed
	CNIRequestsInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: SubsystemCNI,
			Name:      "requests_in_flight",
			Help:      "Number of CNI requests currently being processed",
		},
	)

	// ---- OVN Database Metrics ----

	// OVNOperationDuration measures the time taken for OVN database operations
	// Labels: operation (create_ls/delete_ls/create_lsp/etc), result (success/failure)
	OVNOperationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemOVN,
			Name:      "operation_duration_seconds",
			Help:      "Time taken for OVN database operations in seconds",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		},
		[]string{"operation", "result"},
	)

	// OVNOperationTotal counts the total number of OVN database operations
	// Labels: operation (create_ls/delete_ls/create_lsp/etc), result (success/failure)
	OVNOperationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemOVN,
			Name:      "operation_total",
			Help:      "Total number of OVN database operations",
		},
		[]string{"operation", "result"},
	)

	// OVNDBConnectionStatus indicates the connection status to OVN databases
	// Labels: database (nb/sb)
	// Value: 1 = connected, 0 = disconnected
	OVNDBConnectionStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: SubsystemOVN,
			Name:      "db_connection_status",
			Help:      "OVN database connection status (1=connected, 0=disconnected)",
		},
		[]string{"database"},
	)

	// OVNDBReconnectTotal counts the total number of database reconnection attempts
	// Labels: database (nb/sb), result (success/failure)
	OVNDBReconnectTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemOVN,
			Name:      "db_reconnect_total",
			Help:      "Total number of OVN database reconnection attempts",
		},
		[]string{"database", "result"},
	)

	// OVNTransactionDuration measures the time taken for OVN transactions
	OVNTransactionDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemOVN,
			Name:      "transaction_duration_seconds",
			Help:      "Time taken for OVN database transactions in seconds",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		},
	)

	// ---- Controller Metrics ----

	// ControllerReconcileDuration measures the time taken for controller reconciliation
	// Labels: controller (subnet/pod/service/policy/node), result (success/failure/requeue)
	ControllerReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemController,
			Name:      "reconcile_duration_seconds",
			Help:      "Time taken for controller reconciliation in seconds",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		},
		[]string{"controller", "result"},
	)

	// ControllerReconcileTotal counts the total number of controller reconciliations
	// Labels: controller (subnet/pod/service/policy/node), result (success/failure/requeue)
	ControllerReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemController,
			Name:      "reconcile_total",
			Help:      "Total number of controller reconciliations",
		},
		[]string{"controller", "result"},
	)

	// ControllerWorkQueueDepth tracks the depth of controller work queues
	// Labels: controller (subnet/pod/service/policy/node)
	ControllerWorkQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: SubsystemController,
			Name:      "workqueue_depth",
			Help:      "Current depth of controller work queues",
		},
		[]string{"controller"},
	)

	// ---- IP Allocator Metrics ----

	// IPAllocatorAvailableIPs tracks the number of available IPs per subnet
	// Labels: subnet (subnet name)
	IPAllocatorAvailableIPs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: SubsystemAllocator,
			Name:      "available_ips",
			Help:      "Number of available IPs in each subnet",
		},
		[]string{"subnet"},
	)

	// IPAllocatorUsedIPs tracks the number of used IPs per subnet
	// Labels: subnet (subnet name)
	IPAllocatorUsedIPs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: SubsystemAllocator,
			Name:      "used_ips",
			Help:      "Number of used IPs in each subnet",
		},
		[]string{"subnet"},
	)

	// IPAllocationTotal counts the total number of IP allocations
	// Labels: subnet (subnet name), result (success/failure)
	IPAllocationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemAllocator,
			Name:      "allocation_total",
			Help:      "Total number of IP allocation operations",
		},
		[]string{"subnet", "result"},
	)

	// IPAllocationDuration measures the time taken for IP allocation
	IPAllocationDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemAllocator,
			Name:      "allocation_duration_seconds",
			Help:      "Time taken for IP allocation in seconds",
			Buckets:   []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1},
		},
	)
)

// Register registers all metrics with the controller-runtime metrics registry.
// This function is safe to call multiple times; metrics will only be registered once.
func Register() {
	registerOnce.Do(func() {
		// CNI metrics
		metrics.Registry.MustRegister(PodNetworkConfigDuration)
		metrics.Registry.MustRegister(PodNetworkConfigTotal)
		metrics.Registry.MustRegister(CNIRequestsInFlight)

		// OVN metrics
		metrics.Registry.MustRegister(OVNOperationDuration)
		metrics.Registry.MustRegister(OVNOperationTotal)
		metrics.Registry.MustRegister(OVNDBConnectionStatus)
		metrics.Registry.MustRegister(OVNDBReconnectTotal)
		metrics.Registry.MustRegister(OVNTransactionDuration)

		// Controller metrics
		metrics.Registry.MustRegister(ControllerReconcileDuration)
		metrics.Registry.MustRegister(ControllerReconcileTotal)
		metrics.Registry.MustRegister(ControllerWorkQueueDepth)

		// IP Allocator metrics
		metrics.Registry.MustRegister(IPAllocatorAvailableIPs)
		metrics.Registry.MustRegister(IPAllocatorUsedIPs)
		metrics.Registry.MustRegister(IPAllocationTotal)
		metrics.Registry.MustRegister(IPAllocationDuration)
	})
}
