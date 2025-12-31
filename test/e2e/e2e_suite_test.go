// Package e2e contains end-to-end tests for zstack-ovn-kubernetes CNI.
// These tests verify the complete functionality of the CNI plugin in a real
// Kubernetes cluster environment using Kind (Kubernetes in Docker).
//
// Test Requirements:
// - Docker installed and running
// - Kind CLI installed
// - kubectl installed
// - Sufficient system resources (4GB+ RAM recommended)
//
// Running Tests:
//   go test -v ./test/e2e/... -timeout 30m
//
// Feature: zstack-ovn-kubernetes-cni
// Validates: Requirements 14.1, 14.4
package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestE2E is the entry point for running E2E tests using Ginkgo.
// It registers the Ginkgo test framework with Go's testing package.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ZStack OVN Kubernetes E2E Suite")
}

var _ = BeforeSuite(func() {
	// Setup code that runs once before all tests
	By("Setting up E2E test environment")

	// Initialize test framework
	err := InitTestFramework()
	Expect(err).NotTo(HaveOccurred(), "Failed to initialize test framework")
})

var _ = AfterSuite(func() {
	// Cleanup code that runs once after all tests
	By("Cleaning up E2E test environment")

	// Cleanup test framework resources
	CleanupTestFramework()
})
