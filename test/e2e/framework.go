// Package e2e provides the E2E testing framework for zstack-ovn-kubernetes CNI.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	DefaultKindClusterName = "zstack-ovn-e2e"
	DefaultTestNamespace   = "e2e-test"
	DefaultPodTimeout      = 5 * time.Minute
	DefaultServiceTimeout  = 2 * time.Minute
	PollInterval           = 2 * time.Second
)

type TestFramework struct {
	ClusterName    string
	Clientset      kubernetes.Interface
	KubeconfigPath string
	TestNamespace  string
	ClusterCreated bool
}

var framework *TestFramework
var _ = wait.PollUntilContextTimeout

func InitTestFramework() error {
	framework = &TestFramework{
		ClusterName:   getEnvOrDefault("E2E_CLUSTER_NAME", DefaultKindClusterName),
		TestNamespace: getEnvOrDefault("E2E_NAMESPACE", DefaultTestNamespace),
	}
	useExisting := os.Getenv("E2E_USE_EXISTING_CLUSTER") == "true"
	if !useExisting {
		if err := framework.CreateKindCluster(); err != nil {
			return fmt.Errorf("failed to create Kind cluster: %w", err)
		}
		framework.ClusterCreated = true
	}
	if err := framework.SetupClient(); err != nil {
		return fmt.Errorf("failed to setup Kubernetes client: %w", err)
	}
	if err := framework.CreateTestNamespace(); err != nil {
		return fmt.Errorf("failed to create test namespace: %w", err)
	}
	return nil
}

func CleanupTestFramework() {
	if framework == nil {
		return
	}
	if framework.Clientset != nil {
		ctx := context.Background()
		_ = framework.Clientset.CoreV1().Namespaces().Delete(ctx, framework.TestNamespace, metav1.DeleteOptions{})
	}
	if framework.ClusterCreated && os.Getenv("E2E_SKIP_CLEANUP") != "true" {
		_ = framework.DeleteKindCluster()
	}
}

func GetFramework() *TestFramework { return framework }

func (f *TestFramework) CreateKindCluster() error {
	if _, err := exec.LookPath("kind"); err != nil {
		return fmt.Errorf("kind CLI not found: %w", err)
	}
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err == nil && strings.Contains(string(output), f.ClusterName) {
		return nil
	}
	configPath, err := f.writeKindConfig()
	if err != nil {
		return fmt.Errorf("failed to write Kind config: %w", err)
	}
	defer os.Remove(configPath)
	cmd = exec.Command("kind", "create", "cluster", "--name", f.ClusterName, "--config", configPath, "--wait", "5m")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create Kind cluster: %w", err)
	}
	return nil
}

func (f *TestFramework) writeKindConfig() (string, error) {
	config := `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: "10.244.0.0/16"
  serviceSubnet: "10.96.0.0/16"
nodes:
  - role: control-plane
  - role: worker
  - role: worker
`
	tmpFile, err := os.CreateTemp("", "kind-config-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := tmpFile.WriteString(config); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", err
	}
	tmpFile.Close()
	return tmpFile.Name(), nil
}

func (f *TestFramework) DeleteKindCluster() error {
	cmd := exec.Command("kind", "delete", "cluster", "--name", f.ClusterName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (f *TestFramework) SetupClient() error {
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
	}
	f.KubeconfigPath = kubeconfigPath
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to build config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}
	f.Clientset = clientset
	return nil
}

func (f *TestFramework) CreateTestNamespace() error {
	ctx := context.Background()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   f.TestNamespace,
			Labels: map[string]string{"app.kubernetes.io/name": "e2e-test"},
		},
	}
	_, err := f.Clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
