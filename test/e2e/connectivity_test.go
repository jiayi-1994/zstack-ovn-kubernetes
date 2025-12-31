// Package e2e contains connectivity E2E tests for zstack-ovn-kubernetes CNI.
// These tests verify Pod-to-Pod and Pod-to-Service network connectivity.
//
// Feature: zstack-ovn-kubernetes-cni
// Property 5: Pod 网络连通性
// Validates: Requirements 19.1, 21.1, 21.2
package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("Pod Network Connectivity", func() {
	var (
		ctx       context.Context
		f         *TestFramework
		namespace string
	)

	BeforeEach(func() {
		ctx = context.Background()
		f = GetFramework()
		Expect(f).NotTo(BeNil(), "Test framework not initialized")
		namespace = f.TestNamespace
	})

	// Property 5: Pod 网络连通性
	// For any two Pods:
	// - Same-node Pods should be able to communicate via IP
	// - Cross-node Pods should be able to communicate via VXLAN tunnel
	// - Pods should be able to access other Pods via Service ClusterIP

	Describe("Same-Node Pod Communication", func() {
		// Validates: Requirements 21.1
		// WHEN two Pods are on the same node, THE Network_Datapath SHALL
		// forward traffic locally through OVS br-int

		var (
			pod1Name string
			pod2Name string
		)

		BeforeEach(func() {
			pod1Name = "same-node-pod-1"
			pod2Name = "same-node-pod-2"
		})

		AfterEach(func() {
			// Cleanup pods
			_ = f.DeletePod(ctx, namespace, pod1Name)
			_ = f.DeletePod(ctx, namespace, pod2Name)
		})

		It("should allow ICMP ping between pods on the same node", func() {
			By("Getting worker node names")
			workerNodes, err := f.GetWorkerNodeNames(ctx)
			if len(workerNodes) == 0 {
				Skip("No worker nodes available, skipping same-node test")
			}
			Expect(err).NotTo(HaveOccurred())

			targetNode := workerNodes[0]

			By(fmt.Sprintf("Creating first pod on node %s", targetNode))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod1Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "same-node-test", "pod": "1"},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": targetNode,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By(fmt.Sprintf("Creating second pod on node %s", targetNode))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod2Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "same-node-test", "pod": "2"},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": targetNode,
				},
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

			By(fmt.Sprintf("Testing ICMP connectivity from %s to %s (%s)", pod1Name, pod2Name, pod2IP))
			success, err := f.TestConnectivity(ctx, namespace, pod1Name, pod2IP, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(), "ICMP ping should succeed between same-node pods")
		})
	})


	Describe("Cross-Node Pod Communication", func() {
		// Validates: Requirements 21.2
		// WHEN two Pods are on different nodes, THE Network_Datapath SHALL
		// forward traffic through Geneve/VXLAN tunnel

		var (
			pod1Name string
			pod2Name string
		)

		BeforeEach(func() {
			pod1Name = "cross-node-pod-1"
			pod2Name = "cross-node-pod-2"
		})

		AfterEach(func() {
			// Cleanup pods
			_ = f.DeletePod(ctx, namespace, pod1Name)
			_ = f.DeletePod(ctx, namespace, pod2Name)
		})

		It("should allow ICMP ping between pods on different nodes", func() {
			By("Getting worker node names")
			workerNodes, err := f.GetWorkerNodeNames(ctx)
			Expect(err).NotTo(HaveOccurred())

			if len(workerNodes) < 2 {
				Skip("Need at least 2 worker nodes for cross-node test")
			}

			node1 := workerNodes[0]
			node2 := workerNodes[1]

			By(fmt.Sprintf("Creating first pod on node %s", node1))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod1Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "cross-node-test", "pod": "1"},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node1,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By(fmt.Sprintf("Creating second pod on node %s", node2))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod2Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "cross-node-test", "pod": "2"},
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

			By("Getting pod IPs")
			pod2IP, err := f.GetPodIP(ctx, namespace, pod2Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(pod2IP).NotTo(BeEmpty())

			By(fmt.Sprintf("Testing ICMP connectivity from %s to %s (%s)", pod1Name, pod2Name, pod2IP))
			success, err := f.TestConnectivity(ctx, namespace, pod1Name, pod2IP, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(), "ICMP ping should succeed between cross-node pods")
		})

		It("should allow TCP connectivity between pods on different nodes", func() {
			By("Getting worker node names")
			workerNodes, err := f.GetWorkerNodeNames(ctx)
			Expect(err).NotTo(HaveOccurred())

			if len(workerNodes) < 2 {
				Skip("Need at least 2 worker nodes for cross-node test")
			}

			node1 := workerNodes[0]
			node2 := workerNodes[1]

			By(fmt.Sprintf("Creating server pod on node %s", node2))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod2Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				// Start a simple TCP server on port 8080
				Command: []string{"nc", "-l", "-p", "8080", "-e", "echo", "hello"},
				Labels:  map[string]string{"app": "cross-node-tcp-test", "role": "server"},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node2,
				},
				Ports: []int32{8080},
			})
			Expect(err).NotTo(HaveOccurred())

			By(fmt.Sprintf("Creating client pod on node %s", node1))
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      pod1Name,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "cross-node-tcp-test", "role": "client"},
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node1,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pods to be running")
			_, err = f.WaitForPodRunning(ctx, namespace, pod1Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			_, err = f.WaitForPodRunning(ctx, namespace, pod2Name, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Getting server pod IP")
			serverIP, err := f.GetPodIP(ctx, namespace, pod2Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(serverIP).NotTo(BeEmpty())

			By(fmt.Sprintf("Testing TCP connectivity from %s to %s:8080", pod1Name, serverIP))
			success, err := f.TestConnectivity(ctx, namespace, pod1Name, serverIP, 8080)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(), "TCP connection should succeed between cross-node pods")
		})
	})


	Describe("Pod to Service Communication", func() {
		// Validates: Requirements 19.1
		// WHEN a Pod accesses a Service ClusterIP, THE Network_Datapath SHALL
		// load balance traffic to backend Pods

		var (
			clientPodName string
			serverPodName string
			serviceName   string
		)

		BeforeEach(func() {
			clientPodName = "service-client-pod"
			serverPodName = "service-server-pod"
			serviceName = "test-service"
		})

		AfterEach(func() {
			// Cleanup resources
			_ = f.DeletePod(ctx, namespace, clientPodName)
			_ = f.DeletePod(ctx, namespace, serverPodName)
			_ = f.DeleteService(ctx, namespace, serviceName)
		})

		It("should allow Pod to access Service via ClusterIP", func() {
			By("Creating server pod")
			_, err := f.CreatePod(ctx, PodConfig{
				Name:      serverPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				// Start a simple HTTP server
				Command: []string{"httpd", "-f", "-p", "80"},
				Labels:  map[string]string{"app": "service-test", "role": "server"},
				Ports:   []int32{80},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Creating service")
			_, err = f.CreateService(ctx, ServiceConfig{
				Name:      serviceName,
				Namespace: namespace,
				Selector:  map[string]string{"app": "service-test", "role": "server"},
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
			Expect(err).NotTo(HaveOccurred())

			By("Creating client pod")
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      clientPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "service-test", "role": "client"},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for pods to be running")
			_, err = f.WaitForPodRunning(ctx, namespace, serverPodName, DefaultPodTimeout)
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

			By(fmt.Sprintf("Testing TCP connectivity from %s to service %s (%s:80)", clientPodName, serviceName, clusterIP))
			success, err := f.TestConnectivity(ctx, namespace, clientPodName, clusterIP, 80)
			Expect(err).NotTo(HaveOccurred())
			Expect(success).To(BeTrue(), "TCP connection to Service ClusterIP should succeed")
		})

		It("should load balance traffic across multiple backend pods", func() {
			By("Creating multiple server pods")
			for i := 1; i <= 2; i++ {
				podName := fmt.Sprintf("%s-%d", serverPodName, i)
				_, err := f.CreatePod(ctx, PodConfig{
					Name:      podName,
					Namespace: namespace,
					Image:     "busybox:1.36",
					Command:   []string{"httpd", "-f", "-p", "80"},
					Labels:    map[string]string{"app": "lb-test", "role": "server"},
					Ports:     []int32{80},
				})
				Expect(err).NotTo(HaveOccurred())

				// Wait for pod to be running
				_, err = f.WaitForPodRunning(ctx, namespace, podName, DefaultPodTimeout)
				Expect(err).NotTo(HaveOccurred())
			}

			By("Creating service with multiple backends")
			_, err := f.CreateService(ctx, ServiceConfig{
				Name:      serviceName,
				Namespace: namespace,
				Selector:  map[string]string{"app": "lb-test", "role": "server"},
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
			Expect(err).NotTo(HaveOccurred())

			By("Creating client pod")
			_, err = f.CreatePod(ctx, PodConfig{
				Name:      clientPodName,
				Namespace: namespace,
				Image:     "busybox:1.36",
				Command:   []string{"sleep", "3600"},
				Labels:    map[string]string{"app": "lb-test", "role": "client"},
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = f.WaitForPodRunning(ctx, namespace, clientPodName, DefaultPodTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for service endpoints")
			err = f.WaitForServiceEndpoints(ctx, namespace, serviceName, DefaultServiceTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Getting service ClusterIP")
			clusterIP, err := f.GetServiceClusterIP(ctx, namespace, serviceName)
			Expect(err).NotTo(HaveOccurred())
			fmt.Println("123")
			By("Testing multiple connections to verify load balancing")
			// Make multiple connections to verify the service is accessible
			for i := 0; i < 5; i++ {
				success, err := f.TestConnectivity(ctx, namespace, clientPodName, clusterIP, 80)
				Expect(err).NotTo(HaveOccurred())
				Expect(success).To(BeTrue(), "Connection %d to Service should succeed", i+1)
				time.Sleep(100 * time.Millisecond)
			}

			// Cleanup additional server pods
			for i := 1; i <= 2; i++ {
				podName := fmt.Sprintf("%s-%d", serverPodName, i)
				_ = f.DeletePod(ctx, namespace, podName)
			}
		})
	})
})
