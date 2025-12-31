// Package cni provides the CNI Server implementation.
//
// The CNI Server listens on a Unix Socket and handles CNI requests
// from the CNI binary (zstack-ovn-cni). This architecture separates
// the CNI binary (which must be fast and lightweight) from the actual
// network configuration logic (which may need to wait for resources).
//
// Architecture:
//
//	┌─────────────────┐     Unix Socket      ┌─────────────────┐
//	│  zstack-ovn-cni │ ──────────────────▶  │   CNI Server    │
//	│  (CNI Binary)   │                      │ (in ovnkube-node)│
//	└─────────────────┘                      └─────────────────┘
//	       │                                         │
//	       │ Called by                               │ Configures
//	       │ container runtime                       │ OVN/OVS
//	       ▼                                         ▼
//	┌─────────────────┐                      ┌─────────────────┐
//	│   containerd    │                      │   OVN NB DB     │
//	│   CRI-O         │                      │   OVS br-int    │
//	└─────────────────┘                      └─────────────────┘
//
// Request Flow:
// 1. Container runtime calls CNI binary with ADD/DEL/CHECK command
// 2. CNI binary sends HTTP request to CNI Server via Unix Socket
// 3. CNI Server processes the request (creates LSP, configures OVS, etc.)
// 4. CNI Server returns result to CNI binary
// 5. CNI binary returns result to container runtime
//
// Reference: OVN-Kubernetes pkg/cni/cniserver.go
package cni

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	// CNIServerSocketPath is the default Unix Socket path for CNI Server
	// This path is used by both the CNI binary and the CNI Server
	CNIServerSocketPath = "/var/run/zstack-ovn/cni-server.sock"

	// CNIServerSocketDir is the directory containing the socket
	CNIServerSocketDir = "/var/run/zstack-ovn"

	// HTTP endpoints for CNI commands
	CNIAddPath   = "/cni/add"
	CNIDelPath   = "/cni/del"
	CNICheckPath = "/cni/check"

	// Request timeout for CNI operations
	// CNI ADD may need to wait for Pod annotation, so use a longer timeout
	CNIRequestTimeout = 60 * time.Second

	// Maximum request body size (1MB should be more than enough for CNI requests)
	MaxRequestBodySize = 1 << 20
)

// Request represents a CNI request from the CNI binary
// This is the JSON payload sent over the Unix Socket
type Request struct {
	// Command is the CNI command: ADD, DEL, or CHECK
	Command string `json:"command"`

	// ContainerID is the container ID from CNI_CONTAINERID
	ContainerID string `json:"containerID"`

	// Netns is the network namespace path from CNI_NETNS
	// Example: /var/run/netns/cni-xxxxx
	Netns string `json:"netns"`

	// IfName is the interface name from CNI_IFNAME
	// Usually "eth0"
	IfName string `json:"ifName"`

	// PodNamespace is the Pod's namespace from CNI_ARGS
	PodNamespace string `json:"podNamespace"`

	// PodName is the Pod's name from CNI_ARGS
	PodName string `json:"podName"`

	// PodUID is the Pod's UID from CNI_ARGS
	PodUID string `json:"podUID"`

	// CNIConfig is the raw CNI configuration from stdin
	CNIConfig []byte `json:"cniConfig"`
}

// Response represents a CNI response to the CNI binary
type Response struct {
	// Result is the CNI result for ADD command (JSON encoded)
	// Contains IP addresses, routes, DNS, etc.
	Result []byte `json:"result,omitempty"`

	// Error is the error message if the request failed
	Error string `json:"error,omitempty"`
}

// PodNetworkInfo contains the network configuration for a Pod
// This is populated by the controller and used by CNI Server
type PodNetworkInfo struct {
	// IPAddress is the Pod's IP address with prefix length
	// Example: "10.244.1.5/24"
	IPAddress string

	// MACAddress is the Pod's MAC address
	// Example: "0a:58:0a:f4:01:05"
	MACAddress string

	// Gateway is the default gateway IP
	// Example: "10.244.1.1"
	Gateway string

	// Routes are additional routes for the Pod
	Routes []Route

	// MTU is the MTU for the Pod interface
	MTU int

	// SandboxID is the sandbox/container ID
	SandboxID string

	// LogicalSwitchPort is the OVN LSP name
	LogicalSwitchPort string
}

// Route represents a network route
type Route struct {
	// Dest is the destination CIDR
	Dest string `json:"dest"`

	// NextHop is the next hop IP address
	NextHop string `json:"nextHop"`
}

// RequestHandler is the interface for handling CNI requests
// This allows the CNI Server to be decoupled from the actual implementation
type RequestHandler interface {
	// HandleAdd handles CNI ADD command
	// Returns the network configuration for the Pod
	HandleAdd(ctx context.Context, req *Request) (*PodNetworkInfo, error)

	// HandleDel handles CNI DEL command
	HandleDel(ctx context.Context, req *Request) error

	// HandleCheck handles CNI CHECK command
	HandleCheck(ctx context.Context, req *Request) error
}

// Server is the CNI Server that handles CNI requests via Unix Socket
type Server struct {
	// socketPath is the Unix Socket path
	socketPath string

	// listener is the Unix Socket listener
	listener net.Listener

	// httpServer is the HTTP server for handling requests
	httpServer *http.Server

	// handler is the request handler implementation
	handler RequestHandler

	// mu protects server state
	mu sync.Mutex

	// running indicates if the server is running
	running bool

	// stopCh is used to signal shutdown
	stopCh chan struct{}
}

// NewServer creates a new CNI Server
//
// Parameters:
//   - socketPath: Unix Socket path (default: /var/run/zstack-ovn/cni-server.sock)
//   - handler: Request handler implementation
//
// Returns:
//   - *Server: CNI Server instance
func NewServer(socketPath string, handler RequestHandler) *Server {
	if socketPath == "" {
		socketPath = CNIServerSocketPath
	}
	return &Server{
		socketPath: socketPath,
		handler:    handler,
		stopCh:     make(chan struct{}),
	}
}

// Start starts the CNI Server
//
// The server:
// 1. Creates the socket directory if it doesn't exist
// 2. Removes any existing socket file
// 3. Creates a Unix Socket listener
// 4. Starts an HTTP server to handle requests
//
// Endpoints:
// - POST /cni/add   - CNI ADD command
// - POST /cni/del   - CNI DEL command
// - POST /cni/check - CNI CHECK command
//
// Returns:
//   - error: Start error
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("CNI server is already running")
	}

	// Create socket directory if it doesn't exist
	socketDir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", socketDir, err)
	}

	// Remove existing socket file if it exists
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket %s: %w", s.socketPath, err)
	}

	// Create Unix Socket listener
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.socketPath, err)
	}
	s.listener = listener

	// Set socket permissions to allow access from CNI binary
	if err := os.Chmod(s.socketPath, 0666); err != nil {
		listener.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	// Create HTTP server with routes
	mux := http.NewServeMux()
	mux.HandleFunc(CNIAddPath, s.handleAdd)
	mux.HandleFunc(CNIDelPath, s.handleDel)
	mux.HandleFunc(CNICheckPath, s.handleCheck)

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  CNIRequestTimeout,
		WriteTimeout: CNIRequestTimeout,
	}

	s.running = true

	// Start HTTP server in a goroutine
	go func() {
		klog.Infof("CNI Server started, listening on %s", s.socketPath)
		if err := s.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			klog.Errorf("CNI Server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the CNI Server gracefully
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	// Signal shutdown
	close(s.stopCh)

	// Shutdown HTTP server with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		klog.Errorf("Error shutting down CNI server: %v", err)
	}

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	// Remove socket file
	os.Remove(s.socketPath)

	s.running = false
	klog.Infof("CNI Server stopped")
	return nil
}

// handleAdd handles POST /cni/add requests
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	req, err := s.parseRequest(r)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse request: %v", err))
		return
	}

	klog.V(4).Infof("CNI ADD request: pod=%s/%s, container=%s",
		req.PodNamespace, req.PodName, req.ContainerID)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), CNIRequestTimeout)
	defer cancel()

	// Handle the ADD request
	info, err := s.handler.HandleAdd(ctx, req)
	if err != nil {
		klog.Errorf("CNI ADD failed for pod %s/%s: %v", req.PodNamespace, req.PodName, err)
		s.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Build CNI result
	result, err := buildCNIResult(info)
	if err != nil {
		klog.Errorf("Failed to build CNI result for pod %s/%s: %v", req.PodNamespace, req.PodName, err)
		s.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	klog.V(4).Infof("CNI ADD success: pod=%s/%s, ip=%s",
		req.PodNamespace, req.PodName, info.IPAddress)

	s.sendResponse(w, &Response{Result: result})
}

// handleDel handles POST /cni/del requests
func (s *Server) handleDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	req, err := s.parseRequest(r)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse request: %v", err))
		return
	}

	klog.V(4).Infof("CNI DEL request: pod=%s/%s, container=%s",
		req.PodNamespace, req.PodName, req.ContainerID)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), CNIRequestTimeout)
	defer cancel()

	// Handle the DEL request
	if err := s.handler.HandleDel(ctx, req); err != nil {
		// DEL should be idempotent, log error but don't fail
		klog.Warningf("CNI DEL error for pod %s/%s (continuing): %v",
			req.PodNamespace, req.PodName, err)
	}

	klog.V(4).Infof("CNI DEL success: pod=%s/%s", req.PodNamespace, req.PodName)

	// DEL always returns success (idempotent)
	s.sendResponse(w, &Response{})
}

// handleCheck handles POST /cni/check requests
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	req, err := s.parseRequest(r)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse request: %v", err))
		return
	}

	klog.V(4).Infof("CNI CHECK request: pod=%s/%s, container=%s",
		req.PodNamespace, req.PodName, req.ContainerID)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), CNIRequestTimeout)
	defer cancel()

	// Handle the CHECK request
	if err := s.handler.HandleCheck(ctx, req); err != nil {
		klog.Errorf("CNI CHECK failed for pod %s/%s: %v", req.PodNamespace, req.PodName, err)
		s.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	klog.V(4).Infof("CNI CHECK success: pod=%s/%s", req.PodNamespace, req.PodName)

	s.sendResponse(w, &Response{})
}

// parseRequest parses a CNI request from HTTP request body
func (s *Server) parseRequest(r *http.Request) (*Request, error) {
	// Limit request body size
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodySize))
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	defer r.Body.Close()

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal request: %w", err)
	}

	// Validate required fields
	if req.ContainerID == "" {
		return nil, fmt.Errorf("containerID is required")
	}
	if req.PodNamespace == "" {
		return nil, fmt.Errorf("podNamespace is required")
	}
	if req.PodName == "" {
		return nil, fmt.Errorf("podName is required")
	}

	return &req, nil
}

// sendResponse sends a successful response
func (s *Server) sendResponse(w http.ResponseWriter, resp *Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// sendError sends an error response
func (s *Server) sendError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(&Response{Error: message})
}

// buildCNIResult builds a CNI result from PodNetworkInfo
// The result follows CNI spec version 1.0.0
func buildCNIResult(info *PodNetworkInfo) ([]byte, error) {
	if info == nil {
		return nil, fmt.Errorf("PodNetworkInfo is nil")
	}

	// Parse IP address and prefix
	ip, ipNet, err := net.ParseCIDR(info.IPAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid IP address %s: %w", info.IPAddress, err)
	}

	// Determine IP version
	ipVersion := "4"
	if ip.To4() == nil {
		ipVersion = "6"
	}

	// Build CNI result structure
	// Reference: https://www.cni.dev/docs/spec/#success
	result := map[string]interface{}{
		"cniVersion": "1.0.0",
		"interfaces": []map[string]interface{}{
			{
				"name":    "eth0",
				"mac":     info.MACAddress,
				"sandbox": info.SandboxID,
			},
		},
		"ips": []map[string]interface{}{
			{
				"version":   ipVersion,
				"address":   info.IPAddress,
				"gateway":   info.Gateway,
				"interface": 0,
			},
		},
	}

	// Add routes if present
	if len(info.Routes) > 0 {
		routes := make([]map[string]interface{}, 0, len(info.Routes))
		for _, route := range info.Routes {
			r := map[string]interface{}{
				"dst": route.Dest,
			}
			if route.NextHop != "" {
				r["gw"] = route.NextHop
			}
			routes = append(routes, r)
		}
		result["routes"] = routes
	} else {
		// Add default route if no routes specified
		defaultDst := "0.0.0.0/0"
		if ipVersion == "6" {
			defaultDst = "::/0"
		}
		result["routes"] = []map[string]interface{}{
			{
				"dst": defaultDst,
				"gw":  info.Gateway,
			},
		}
	}

	// Add DNS configuration (empty for now, can be extended)
	result["dns"] = map[string]interface{}{}

	// Calculate prefix length from IPNet
	ones, _ := ipNet.Mask.Size()
	_ = ones // Used in address field above

	return json.Marshal(result)
}

// IsRunning returns whether the server is running
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// SocketPath returns the socket path
func (s *Server) SocketPath() string {
	return s.socketPath
}
