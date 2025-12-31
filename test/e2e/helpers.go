// Package e2e provides helper functions for E2E tests.
package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
)

type PodConfig struct {
	Name         string
	Namespace    string
	Image        string
	Command      []string
	Args         []string
	Labels       map[string]string
	NodeSelector map[string]string
	Ports        []int32
}

type ServiceConfig struct {
	Name      string
	Namespace string
	Selector  map[string]string
	Ports     []ServicePort
	Type      corev1.ServiceType
}

type ServicePort struct {
	Name       string
	Port       int32
	TargetPort int32
	Protocol   corev1.Protocol
}

func (f *TestFramework) CreatePod(ctx context.Context, config PodConfig) (*corev1.Pod, error) {
	if config.Namespace == "" {
		config.Namespace = f.TestNamespace
	}
	if config.Image == "" {
		config.Image = "busybox:1.36"
	}
	if config.Labels == nil {
		config.Labels = map[string]string{"app": config.Name}
	}
	var containerPorts []corev1.ContainerPort
	for _, port := range config.Ports {
		containerPorts = append(containerPorts, corev1.ContainerPort{ContainerPort: port, Protocol: corev1.ProtocolTCP})
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: config.Name, Namespace: config.Namespace, Labels: config.Labels},
		Spec: corev1.PodSpec{
			Containers:    []corev1.Container{{Name: "main", Image: config.Image, Command: config.Command, Args: config.Args, Ports: containerPorts}},
			NodeSelector:  config.NodeSelector,
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
	return f.Clientset.CoreV1().Pods(config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

func (f *TestFramework) CreateService(ctx context.Context, config ServiceConfig) (*corev1.Service, error) {
	if config.Namespace == "" {
		config.Namespace = f.TestNamespace
	}
	if config.Type == "" {
		config.Type = corev1.ServiceTypeClusterIP
	}
	var servicePorts []corev1.ServicePort
	for _, port := range config.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		servicePorts = append(servicePorts, corev1.ServicePort{Name: port.Name, Port: port.Port, TargetPort: intstr.FromInt(int(port.TargetPort)), Protocol: protocol})
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: config.Name, Namespace: config.Namespace},
		Spec:       corev1.ServiceSpec{Selector: config.Selector, Ports: servicePorts, Type: config.Type},
	}
	return f.Clientset.CoreV1().Services(config.Namespace).Create(ctx, svc, metav1.CreateOptions{})
}

func (f *TestFramework) WaitForPodRunning(ctx context.Context, namespace, name string, timeout time.Duration) (*corev1.Pod, error) {
	var pod *corev1.Pod
	err := wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		var err error
		pod, err = f.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if pod.Status.Phase == corev1.PodRunning {
			for _, cs := range pod.Status.ContainerStatuses {
				if !cs.Ready {
					return false, nil
				}
			}
			return true, nil
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return false, fmt.Errorf("pod %s/%s is in terminal state: %s", namespace, name, pod.Status.Phase)
		}
		return false, nil
	})
	if err != nil {
		return nil, fmt.Errorf("timeout waiting for pod %s/%s to be running: %w", namespace, name, err)
	}
	return pod, nil
}

func (f *TestFramework) WaitForServiceEndpoints(ctx context.Context, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, PollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		endpoints, err := f.Clientset.CoreV1().Endpoints(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, subset := range endpoints.Subsets {
			if len(subset.Addresses) > 0 {
				return true, nil
			}
		}
		return false, nil
	})
}

func (f *TestFramework) GetPodIP(ctx context.Context, namespace, name string) (string, error) {
	pod, err := f.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod %s/%s has no IP address", namespace, name)
	}
	return pod.Status.PodIP, nil
}

func (f *TestFramework) GetServiceClusterIP(ctx context.Context, namespace, name string) (string, error) {
	svc, err := f.Clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return "", fmt.Errorf("service %s/%s has no ClusterIP", namespace, name)
	}
	return svc.Spec.ClusterIP, nil
}

func (f *TestFramework) ExecInPod(ctx context.Context, namespace, name string, command []string) (string, error) {
	args := []string{"exec", "-n", namespace, name, "--"}
	args = append(args, command...)
	output, err := runKubectl(args...)
	if err != nil {
		return "", fmt.Errorf("failed to exec in pod %s/%s: %w", namespace, name, err)
	}
	return output, nil
}

func (f *TestFramework) TestConnectivity(ctx context.Context, fromNamespace, fromPod, targetIP string, targetPort int32) (bool, error) {
	var command []string
	if targetPort == 0 {
		command = []string{"ping", "-c", "3", "-W", "2", targetIP}
	} else {
		command = []string{"nc", "-z", "-w", "5", targetIP, fmt.Sprintf("%d", targetPort)}
	}
	output, err := f.ExecInPod(ctx, fromNamespace, fromPod, command)
	if err != nil {
		return false, fmt.Errorf("connectivity test failed: %w, output: %s", err, output)
	}
	return true, nil
}

func (f *TestFramework) DeletePod(ctx context.Context, namespace, name string) error {
	return f.Clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func (f *TestFramework) DeleteService(ctx context.Context, namespace, name string) error {
	return f.Clientset.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func (f *TestFramework) GetNodeNames(ctx context.Context) ([]string, error) {
	nodes, err := f.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, node := range nodes.Items {
		names = append(names, node.Name)
	}
	return names, nil
}

func (f *TestFramework) GetWorkerNodeNames(ctx context.Context) ([]string, error) {
	nodes, err := f.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, node := range nodes.Items {
		if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
			continue
		}
		names = append(names, node.Name)
	}
	return names, nil
}

func runKubectl(args ...string) (string, error) {
	cmd := NewCommand("kubectl", args...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
