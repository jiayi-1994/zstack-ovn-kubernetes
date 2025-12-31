// Package cni implements the CNI plugin for zstack-ovn-kubernetes.
//
// This package provides:
// - CNI command handlers (ADD, DEL, CHECK)
// - Communication with CNI Server via Unix Socket
// - Network interface configuration in containers
//
// CNI (Container Network Interface) is a specification for configuring
// network interfaces in Linux containers. The CNI plugin is called by
// the container runtime (containerd, CRI-O) during Pod creation/deletion.
//
// CNI Commands:
// - ADD: Configure network for a new container
// - DEL: Clean up network for a deleted container
// - CHECK: Verify network configuration is correct
// - VERSION: Report supported CNI versions
//
// Architecture:
// The CNI binary (zstack-ovn-cni) is a thin client that forwards requests
// to the CNI Server running in zstack-ovnkube-node via Unix Socket.
// This allows the CNI binary to be fast and lightweight while the actual
// network configuration logic runs in the node agent.
//
// Reference: OVN-Kubernetes pkg/cni/cni.go
package cni

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
)

const (
	// DefaultCNISocketPath is the default path to the CNI server socket
	DefaultCNISocketPath = "/var/run/zstack-ovn/cni-server.sock"

	// CNIClientTimeout is the timeout for CNI client requests
	// This should be longer than the server timeout to allow for retries
	CNIClientTimeout = 120 * time.Second

	// CNIConnectTimeout is the timeout for connecting to the CNI server
	CNIConnectTimeout = 5 * time.Second
)

// CNIClient is a client for communicating with the CNI Server
type CNIClient struct {
	// socketPath is the path to the CNI server Unix socket
	socketPath string

	// httpClient is the HTTP client for making requests
	httpClient *http.Client
}

// NewCNIClient creates a new CNI client
//
// Parameters:
//   - socketPath: Path to the CNI server Unix socket
//
// Returns:
//   - *CNIClient: CNI client instance
func NewCNIClient(socketPath string) *CNIClient {
	if socketPath == "" {
		socketPath = DefaultCNISocketPath
	}

	// Create HTTP client with Unix socket transport
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{
				Timeout: CNIConnectTimeout,
			}
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}

	return &CNIClient{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   CNIClientTimeout,
		},
	}
}

// sendRequest sends a request to the CNI server
func (c *CNIClient) sendRequest(ctx context.Context, path string, req *Request) (*Response, error) {
	// Marshal request to JSON
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	// Use "localhost" as host since we're using Unix socket
	url := fmt.Sprintf("http://localhost%s", path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Send request
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request to CNI server: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse response
	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for error in response
	if resp.Error != "" {
		return nil, fmt.Errorf("CNI server error: %s", resp.Error)
	}

	return &resp, nil
}

// Add sends a CNI ADD request to the server
func (c *CNIClient) Add(ctx context.Context, req *Request) (*Response, error) {
	return c.sendRequest(ctx, CNIAddPath, req)
}

// Del sends a CNI DEL request to the server
func (c *CNIClient) Del(ctx context.Context, req *Request) (*Response, error) {
	return c.sendRequest(ctx, CNIDelPath, req)
}

// Check sends a CNI CHECK request to the server
func (c *CNIClient) Check(ctx context.Context, req *Request) (*Response, error) {
	return c.sendRequest(ctx, CNICheckPath, req)
}

// CmdAdd handles the CNI ADD command.
// It is called when a new Pod is created and needs network configuration.
//
// The ADD command:
// 1. Parses CNI configuration from stdin
// 2. Extracts Pod information from CNI_ARGS
// 3. Sends request to CNI Server via Unix Socket
// 4. Waits for response with IP/MAC/Gateway
// 5. Returns CNI result to container runtime
//
// Parameters:
//   - args: CNI arguments from container runtime
//
// Returns:
//   - error: CNI error (will be returned to container runtime)
//
// Reference: OVN-Kubernetes pkg/cni/cni.go cmdAdd()
func CmdAdd(args *skel.CmdArgs) error {
	// Parse CNI configuration
	cniConfig, err := config.ParseCNIConfig(args.StdinData)
	if err != nil {
		return fmt.Errorf("failed to parse CNI config: %w", err)
	}

	// Parse CNI_ARGS to get Pod information
	podNamespace, podName, podUID, err := parseCNIArgs()
	if err != nil {
		return fmt.Errorf("failed to parse CNI_ARGS: %w", err)
	}

	// Create CNI request
	req := &Request{
		Command:      "ADD",
		ContainerID:  args.ContainerID,
		Netns:        args.Netns,
		IfName:       args.IfName,
		PodNamespace: podNamespace,
		PodName:      podName,
		PodUID:       podUID,
		CNIConfig:    args.StdinData,
	}

	// Create CNI client
	client := NewCNIClient(cniConfig.ServerSocket)

	// Send ADD request to CNI server
	ctx, cancel := context.WithTimeout(context.Background(), CNIClientTimeout)
	defer cancel()

	resp, err := client.Add(ctx, req)
	if err != nil {
		return fmt.Errorf("CNI ADD failed: %w", err)
	}

	// Parse and return CNI result
	result, err := parseCNIResult(resp.Result)
	if err != nil {
		return fmt.Errorf("failed to parse CNI result: %w", err)
	}

	return types.PrintResult(result, cniConfig.CNIVersion)
}

// CmdDel handles the CNI DEL command.
// It is called when a Pod is deleted and needs network cleanup.
//
// The DEL command:
// 1. Parses CNI configuration from stdin
// 2. Extracts Pod information from CNI_ARGS
// 3. Sends request to CNI Server via Unix Socket
// 4. Server cleans up OVN LSP, OVS port, and veth pair
//
// DEL is idempotent - it should succeed even if the network was already cleaned up.
//
// Parameters:
//   - args: CNI arguments from container runtime
//
// Returns:
//   - error: CNI error (will be returned to container runtime)
//
// Reference: OVN-Kubernetes pkg/cni/cni.go cmdDel()
func CmdDel(args *skel.CmdArgs) error {
	// Parse CNI configuration
	cniConfig, err := config.ParseCNIConfig(args.StdinData)
	if err != nil {
		return fmt.Errorf("failed to parse CNI config: %w", err)
	}

	// Parse CNI_ARGS to get Pod information
	podNamespace, podName, podUID, err := parseCNIArgs()
	if err != nil {
		// DEL should be idempotent, don't fail if we can't parse args
		podNamespace = ""
		podName = ""
		podUID = ""
	}

	// Create CNI request
	req := &Request{
		Command:      "DEL",
		ContainerID:  args.ContainerID,
		Netns:        args.Netns,
		IfName:       args.IfName,
		PodNamespace: podNamespace,
		PodName:      podName,
		PodUID:       podUID,
		CNIConfig:    args.StdinData,
	}

	// Create CNI client
	client := NewCNIClient(cniConfig.ServerSocket)

	// Send DEL request to CNI server
	ctx, cancel := context.WithTimeout(context.Background(), CNIClientTimeout)
	defer cancel()

	_, err = client.Del(ctx, req)
	if err != nil {
		// DEL should be idempotent, log error but don't fail
		// This allows cleanup to proceed even if the server is unavailable
		fmt.Fprintf(os.Stderr, "CNI DEL warning: %v\n", err)
	}

	return nil
}

// CmdCheck handles the CNI CHECK command.
// It is called to verify Pod network configuration is correct.
//
// The CHECK command:
// 1. Verifies OVN LSP exists
// 2. Verifies OVS port exists
// 3. Verifies container network configuration matches expected state
//
// Parameters:
//   - args: CNI arguments from container runtime
//
// Returns:
//   - error: CNI error if check fails
//
// Reference: OVN-Kubernetes pkg/cni/cni.go cmdCheck()
func CmdCheck(args *skel.CmdArgs) error {
	// Parse CNI configuration
	cniConfig, err := config.ParseCNIConfig(args.StdinData)
	if err != nil {
		return fmt.Errorf("failed to parse CNI config: %w", err)
	}

	// Parse CNI_ARGS to get Pod information
	podNamespace, podName, podUID, err := parseCNIArgs()
	if err != nil {
		return fmt.Errorf("failed to parse CNI_ARGS: %w", err)
	}

	// Create CNI request
	req := &Request{
		Command:      "CHECK",
		ContainerID:  args.ContainerID,
		Netns:        args.Netns,
		IfName:       args.IfName,
		PodNamespace: podNamespace,
		PodName:      podName,
		PodUID:       podUID,
		CNIConfig:    args.StdinData,
	}

	// Create CNI client
	client := NewCNIClient(cniConfig.ServerSocket)

	// Send CHECK request to CNI server
	ctx, cancel := context.WithTimeout(context.Background(), CNIClientTimeout)
	defer cancel()

	_, err = client.Check(ctx, req)
	if err != nil {
		return fmt.Errorf("CNI CHECK failed: %w", err)
	}

	return nil
}

// parseCNIArgs parses the CNI_ARGS environment variable
// Format: K8S_POD_NAMESPACE=xxx;K8S_POD_NAME=yyy;K8S_POD_UID=zzz
//
// Returns:
//   - namespace: Pod namespace
//   - name: Pod name
//   - uid: Pod UID
//   - error: Parse error
func parseCNIArgs() (namespace, name, uid string, err error) {
	argsStr := os.Getenv("CNI_ARGS")
	if argsStr == "" {
		return "", "", "", fmt.Errorf("CNI_ARGS environment variable is not set")
	}

	// Parse key=value pairs separated by semicolons
	pairs := strings.Split(argsStr, ";")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])

		switch key {
		case "K8S_POD_NAMESPACE":
			namespace = value
		case "K8S_POD_NAME":
			name = value
		case "K8S_POD_UID":
			uid = value
		}
	}

	// Validate required fields
	if namespace == "" {
		return "", "", "", fmt.Errorf("K8S_POD_NAMESPACE is required in CNI_ARGS")
	}
	if name == "" {
		return "", "", "", fmt.Errorf("K8S_POD_NAME is required in CNI_ARGS")
	}

	return namespace, name, uid, nil
}

// parseCNIResult parses the CNI result from JSON bytes
func parseCNIResult(data []byte) (types.Result, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty CNI result")
	}

	// Parse as CNI 1.0.0 result
	result := &current.Result{}
	if err := json.Unmarshal(data, result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CNI result: %w", err)
	}

	return result, nil
}
