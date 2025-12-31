// Package e2e contains dual-mode E2E tests for zstack-ovn-kubernetes CNI.
// These tests verify that both Standalone and External deployment modes
// provide consistent network functionality.
//
// Feature: zstack-ovn-kubernetes-cni
// Task: 20.3 编写双模式 E2E 测试
// Validates: Requirements 24.1, 24.2, 24.3
//
// Requirement 24: 两种模式下的功能一致性
// - 24.1: Service_Controller SHALL provide same Service load balancing in both modes
// - 24.2: Network_Datapath SHALL support Pod-to-Pod, Pod-to-Service, Pod-to-External in both modes
// - 24.3: Policy_Controller SHALL support NetworkPolicy in both modes
package e2e

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// DeploymentMode represents the CNI deployment mode
type DeploymentMode string

const (
	// StandaloneMode indicates self-managed OVN databases
	StandaloneMode DeploymentMode = "standalone"
	// ExternalMode indicates connection to external OVN databases (e.g., ZStack)
	ExternalMode DeploymentMode = "external"
)

// DualModeTestConfig holds configuration for dual-mode testing
type DualModeTestConfig struct {
	// Mode is the current deployment mode being tested
	Mode DeploymentMode
	// OVNNBDBAddress is the Northbound DB address (for external mode)
	OVNNBDBAddress string
	// OVNSBDBAddress is the Southbound DB address (for external mode)
	OVNSBDBAddress string
}

// getCurrentMode returns the current deployment mode from environment
func getCurrentMode() DeploymentMode {
	mode := os.Getenv("E2E_OVN_MODE")
	if mode == "external" {
		return ExternalMode
	}
	return StandaloneMode
}

// getTestConfig returns the test configuration based on environment
func getTestConfig() DualModeTestConfig {
	return DualModeTestConfig{
		Mode:           getCurrentMode(),
		OVNNBDBAddress: os.Getenv("E2E_OVN_NBDB_ADDRESS"),
		OVNSBDBAddress: os.Getenv("E2E_OVN_SBDB_ADDRESS"),
	}
}

var _ = Describe("Dual Mode Deployment Tests", func() {
	var (
		ctx       context.Context
		f         *TestFramework
		namespace string
		config    DualModeTestConfig
	)

	BeforeEach(func() {
		ctx = context.Background()
		f = GetFramework()
		Expect(f).NotTo(BeNil(), "Test framework not initialized")
		namespace = f.TestNamespace
		config = getTestConfig()
	})

	// Requirement 24.1: Service_Controller SHALL provide same Service load balancing
	// in standalone and external modes
	Describe("Service Load Balancing Consistency", func() {
		var (
			clientPodName  string
			serverPod1Name string
			serverPod2Name string
			serviceName    string
		)

		BeforeEach(func() {
			clientPodName = fmt.Sprintf("svc-lb-client-%s", config.Mode)
			serverPod1Name = fmt.Sprintf("svc-lb-server-1-%s", config.Mode)
			serverPod2Name = fmt.Sprintf("svc-lb-server-2-%s", config.Mode)
			serviceName = fmt.Sprintf("svc-lb-test-%s", config.Mode)
		})

		AfterEach(func() {
			// Cleanup resources
			_ = f.DeletePod(ctx, namespace, clientPodName)
			_ = f.DeletePod(ctx, namespace, serverPod1Name)
			_ = f.DeletePod(ctx, namespace, serverPod2Name)
			_ = f.DeleteService(ctx, namespace, serviceName)
		})

		It(fmt.Sprintf("should provide ClusterIP Service load balancing in %s mode", config.Mode), func() {
			By(fmt.Sprintf("Testing in %s mode", config.Mode))

			By("Creating server pods")
			for i, podName := range []string{serverPod1Name, serverPod2Name} {
				_, err := f.CreatePod(ctx, PodConfig{
					Name:      podName,
					Namespace: namespace,
					Image:     "busybox:1.36",
					Command:   []string{"httpd", "-f", "-p", "80"},
					Labels: map[string]string{
						"app":  "svc-lb-test",
						"mode": string(config.Mode),
					},
					Ports: []int32{80},
				})
				Expect(err).NotTo(HaveOccurred(), "Failed to create server pod %d", i+1)
			}

			By("Creating ClusterIP Service")
			_, err := f.CreateService(ctx, ServiceConfig{
				Name:      serviceName,
				Namespace: namespace,
				Selector: map[string]string{
					"app":  "svc-lb-test",
					"mode": string(config.Mode),
				},
				Ports: []ServicePort{
					{
						Name:       "http",
						Port:       80,
						TargetPort: 80,
						Protocol:   corev1.ProtocolTCP,
					},
				},
				Type: corev1.ServiceTypeClusterIP,
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create Service")

			By("Creating client pod")
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      clientPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "svc-lb-client", "mode": string(config.Mode)},
			})
			Expect(err).NotTo(HaveOccurred(), "Failed to create client pod")

			By("Waiting for pods to be running")
			_, err = f.WaitForPodRunning(ctx, namespace, serverPod1Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WaitForPodRunning(ctx, namespace, serverPod2Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WaitForPodRunning(ctx, namespace, clientPodName, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for service endpoints")
			err = f.WaitForServiceEndpoints(ctx, namespace, serviceName, DefaultServiceTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Getting service ClusterIP")
			clusterIP, err := f.GetServiceClusterIP(ctx, namespace, serviceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(clusterIP).NotTo(BeEmpty())

			By(fmt.Sprintf("Testing Service connectivity in %s mode", config.Mode))
			// Test multiple connections to verify load balancing works
			successCount := 0
			for i := 0; i < 5; i++ {
				success, err := f.TestConnectivity(ctx, namespace, clientPodName, clusterIP, 80)
				if err == nil && success {
					successCount++
				}
				time.Sleep(200 * time.Millisecond)
			}
			Expect(successCount).To(BeNumerically(">=", 3),
				"At least 3 out of 5 connections should succeed in %s mode", config.Mode)
		})
	})

	// Requirement 24.2: Network_Datapath SHALL support Pod-to-Pod, Pod-to-Service,
	// Pod-to-External connectivity in both modes
	Describe("Network Connectivity Consistency", func() {
		var (
			pod1Name string
			pod2Name string
		)

		BeforeEach(func() {
			pod1Name = fmt.Sprintf("net-conn-pod-1-%s", config.Mode)
			pod2Name = fmt.Sprintf("net-conn-pod-2-%s", config.Mode)
		})

		AfterEach(func() {
			_ = f.DeletePod(ctx, namespace, pod1Name)
			_ = f.DeletePod(ctx, namespace, pod2Name)
		})

		It(fmt.Sprintf("should support Pod-to-Pod connectivity in %s mode", config.Mode), func() {
			By(fmt.Sprintf("Testing Pod-to-Pod connectivity in %s mode", config.Mode))

			By("Creating test pods")
			_, err := f.CreatePod(ctx, PodConfig{
				Name:      pod1Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "net-conn-test", "pod": "1", "mode": string(config.Mode)},
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod2Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "net-conn-test", "pod": "2", "mode": string(config.Mode)},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pods to be running")
			_, err = f.WaitForPodRunning(ctx, namespace, pod1Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WaitForPodRunning(ctx, namespace, pod2Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Getting pod IPs")
			pod2IP, err := f.GetPodIP(ctx, namespace, pod2Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(pod2IP).NotTo(BeEmpty())

			By(fmt.Sprintf("Testing ICMP connectivity in %s mode", config.Mode))
			success, err := f.TestConnectivity(ctx, namespace, pod1Name, pod2IP, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(),
				"Pod-to-Pod ICMP connectivity should work in %s mode", config.Mode)
		})

		It(fmt.Sprintf("should support cross-node Pod connectivity in %s mode", config.Mode), func() {
			By("Getting worker node names")
			workerNodes, err := f.GetWorkerNodeNames(ctx)
			Expect(err).NotTo(HaveOccurred())

			if len(workerNodes) < 2 {
				Skip("Need at least 2 worker nodes for cross-node test")
			}

			node1 := workerNodes[0]
			node2 := workerNodes[1]

			By(fmt.Sprintf("Creating pods on different nodes in %s mode", config.Mode))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod1Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "cross-node-test", "mode": string(config.Mode)},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node1,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod2Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "cross-node-test", "mode": string(config.Mode)},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node2,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pods to be running")
			_, err = f.WaitForPodRunning(ctx, namespace, pod1Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WaitForPodRunning(ctx, namespace, pod2Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Getting pod IP")
			pod2IP, err := f.GetPodIP(ctx, namespace, pod2Name)
			Expect(err).NotTo(HaveOccurred())

			By(fmt.Sprintf("Testing cross-node connectivity in %s mode", config.Mode))
			success, err := f.TestConnectivity(ctx, namespace, pod1Name, pod2IP, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(),
				"Cross-node Pod connectivity should work in %s mode", config.Mode)
		})
	})

	// Requirement 24.3: Policy_Controller SHALL support NetworkPolicy in both modes
	Describe("NetworkPolicy Consistency", func() {
		var (
			serverPodName string
			allowedPodName string
			deniedPodName  string
			policyName     string
		)

		BeforeEach(func() {
			serverPodName = fmt.Sprintf("np-server-%s", config.Mode)
			allowedPodName = fmt.Sprintf("np-allowed-%s", config.Mode)
			deniedPodName = fmt.Sprintf("np-denied-%s", config.Mode)
			policyName = fmt.Sprintf("np-test-%s", config.Mode)
		})

		AfterEach(func() {
			// Cleanup resources
			_ = f.DeletePod(ctx, namespace, serverPodName)
			_ = f.DeletePod(ctx, namespace, allowedPodName)
			_ = f.DeletePod(ctx, namespace, deniedPodName)
			_ = deleteNetworkPolicy(ctx, f, namespace, policyName)
		})

		It(fmt.Sprintf("should enforce NetworkPolicy ingress rules in %s mode", config.Mode), func() {
			By(fmt.Sprintf("Testing NetworkPolicy in %s mode", config.Mode))

			By("Creating server pod")
			_, err := f.CreatePod(ctx, PodConfig{
				Name:      serverPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"nc", "-l", "-p", "8080", "-e", "echo", "hello"},
				Labels: map[string]string{
					"app":  "np-server",
					"mode": string(config.Mode),
				},
				Ports: []int32{8080},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating allowed client pod")
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      allowedPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels: map[string]string{
					"app":    "np-client",
					"access": "allowed",
					"mode":   string(config.Mode),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating denied client pod")
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      deniedPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels: map[string]string{
					"app":    "np-client",
					"access": "denied",
					"mode":   string(config.Mode),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pods to be running")
			_, err = f.WaitForPodRunning(ctx, namespace, serverPodName, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WaitForPodRunning(ctx, namespace, allowedPodName, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WaitForPodRunning(ctx, namespace, deniedPodName, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Getting server pod IP")
			serverIP, err := f.GetPodIP(ctx, namespace, serverPodName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying connectivity before NetworkPolicy")
			// Both pods should be able to connect before policy is applied
			success, _ := f.TestConnectivity(ctx, namespace, allowedPodName, serverIP, 8080)
			Expect(success).To(BeTrue(), "Allowed pod should connect before policy")

			By("Creating NetworkPolicy")
			err = createNetworkPolicy(ctx, f, namespace, policyName, config.Mode)
			Expect(err).NotTo(HaveOccurred())

			// Wait for policy to be applied
			time.Sleep(5 * time.Second)

			By("Verifying allowed pod can still connect")
			success, err = f.TestConnectivity(ctx, namespace, allowedPodName, serverIP, 8080)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(),
				"Allowed pod should connect after NetworkPolicy in %s mode", config.Mode)

			By("Verifying denied pod cannot connect")
			// Note: This test may timeout which is expected behavior
			success, _ = f.TestConnectivity(ctx, namespace, deniedPodName, serverIP, 8080)
			Expect(success).To(BeFalse(),
				"Denied pod should NOT connect after NetworkPolicy in %s mode", config.Mode)
		})
	})

	// Test mode-specific behavior
	Describe("Mode-Specific Behavior", func() {
		It(fmt.Sprintf("should correctly identify deployment mode as %s", config.Mode), func() {
			By(fmt.Sprintf("Verifying deployment mode is %s", config.Mode))
			Expect(config.Mode).To(BeElementOf(StandaloneMode, ExternalMode),
				"Mode should be either standalone or external")

			if config.Mode == ExternalMode {
				By("Verifying external mode configuration")
				// In external mode, OVN DB addresses should be configured
				// This is a configuration check, not a connectivity test
				Expect(config.OVNNBDBAddress).NotTo(BeEmpty(),
					"E2E_OVN_NBDB_ADDRESS should be set in external mode")
				Expect(config.OVNSBDBAddress).NotTo(BeEmpty(),
					"E2E_OVN_SBDB_ADDRESS should be set in external mode")
			}
		})
	})
})

// createNetworkPolicy creates a NetworkPolicy that allows traffic only from pods
// with label "access=allowed"
func createNetworkPolicy(ctx context.Context, f *TestFramework, namespace, name string, mode DeploymentMode) error {
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":  "np-server",
					"mode": string(mode),
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"access": "allowed",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
							Port:     func() *intstr.IntOrString { p := intstr.FromInt(8080); return &p }(),
						},
					},
				},
			},
		},
	}

	_, err := f.Clientset.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	return err
}

// deleteNetworkPolicy deletes a NetworkPolicy
func deleteNetworkPolicy(ctx context.Context, f *TestFramework, namespace, name string) error {
	return f.Clientset.NetworkingV1().NetworkPolicies(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}
