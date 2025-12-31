// Package ovndb provides external OVN database connection support.
//
// This file implements functionality specific to external mode, where
// zstack-ovn-kubernetes connects to ZStack-managed OVN databases instead
// of managing its own OVN infrastructure.
//
// External Mode Features:
// - Connection validation to external NB/SB databases
// - Health checking for external database connections
// - Reconnection handling with exponential backoff
// - Compatibility checks with ZStack OVN configuration
//
// Reference: Requirements 2.4, 17.3
package ovndb

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// ExternalDBConfig contains configuration for external OVN database connections
type ExternalDBConfig struct {
	// NBDBAddress is the Northbound DB address (required)
	// Format: tcp:IP:PORT or ssl:IP:PORT
	// Multiple addresses can be specified separated by commas for HA
	NBDBAddress string

	// SBDBAddress is the Southbound DB address (required)
	// Format: tcp:IP:PORT or ssl:IP:PORT
	SBDBAddress string

	// SSL is the SSL configuration (optional)
	SSL *SSLConfig

	// ConnectTimeout is the timeout for initial connection
	// Default: 30 seconds
	ConnectTimeout time.Duration

	// HealthCheckInterval is the interval between health checks
	// Default: 30 seconds
	HealthCheckInterval time.Duration

	// MaxReconnectAttempts is the maximum number of reconnection attempts
	// 0 means unlimited attempts
	// Default: 0 (unlimited)
	MaxReconnectAttempts int
}

// ExternalDBManager manages connections to external OVN databases
// It provides connection validation, health checking, and reconnection handling
type ExternalDBManager struct {
	config *ExternalDBConfig
	client *Client

	// mu protects the connection state
	mu sync.RWMutex

	// connected indicates if currently connected
	connected bool

	// lastHealthCheck is the time of the last successful health check
	lastHealthCheck time.Time

	// reconnectAttempts is the current number of reconnection attempts
	reconnectAttempts int

	// stopCh is used to signal shutdown
	stopCh chan struct{}

	// healthCheckDone is closed when health check goroutine exits
	healthCheckDone chan struct{}
}

// NewExternalDBManager creates a new ExternalDBManager
//
// Parameters:
//   - config: External database configuration
//
// Returns:
//   - *ExternalDBManager: Manager instance
//   - error: Configuration validation error
func NewExternalDBManager(config *ExternalDBConfig) (*ExternalDBManager, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Validate required fields
	if config.NBDBAddress == "" {
		return nil, fmt.Errorf("NBDBAddress is required for external mode")
	}
	if config.SBDBAddress == "" {
		return nil, fmt.Errorf("SBDBAddress is required for external mode")
	}

	// Validate address format
	if err := validateDBAddress(config.NBDBAddress); err != nil {
		return nil, fmt.Errorf("invalid NBDBAddress: %w", err)
	}
	if err := validateDBAddress(config.SBDBAddress); err != nil {
		return nil, fmt.Errorf("invalid SBDBAddress: %w", err)
	}

	// Apply defaults
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 30 * time.Second
	}
	if config.HealthCheckInterval == 0 {
		config.HealthCheckInterval = 30 * time.Second
	}

	return &ExternalDBManager{
		config:          config,
		stopCh:          make(chan struct{}),
		healthCheckDone: make(chan struct{}),
	}, nil
}

// validateDBAddress validates an OVN database address format
func validateDBAddress(address string) error {
	if address == "" {
		return fmt.Errorf("address is empty")
	}

	// Support multiple addresses separated by commas (for HA)
	for _, addr := range strings.Split(address, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}

		// Check for valid scheme
		if !strings.HasPrefix(addr, "tcp:") &&
			!strings.HasPrefix(addr, "ssl:") &&
			!strings.HasPrefix(addr, "unix:") {
			return fmt.Errorf("invalid address scheme: %s (must be tcp:, ssl:, or unix:)", addr)
		}

		// For tcp: and ssl:, validate IP:PORT format
		if strings.HasPrefix(addr, "tcp:") || strings.HasPrefix(addr, "ssl:") {
			hostPort := strings.TrimPrefix(addr, "tcp:")
			hostPort = strings.TrimPrefix(hostPort, "ssl:")

			if !strings.Contains(hostPort, ":") {
				return fmt.Errorf("invalid address format: %s (expected IP:PORT)", addr)
			}
		}
	}

	return nil
}

// Connect establishes connection to external OVN databases
//
// This method:
// 1. Validates the external database configuration
// 2. Creates an OVN client with the external addresses
// 3. Connects to both NB and SB databases
// 4. Starts health check monitoring
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - error: Connection error
func (m *ExternalDBManager) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.connected {
		return nil
	}

	klog.Infof("Connecting to external OVN databases: NB=%s, SB=%s",
		m.config.NBDBAddress, m.config.SBDBAddress)

	// Create client configuration
	clientConfig := &ClientConfig{
		NBDBAddress:          m.config.NBDBAddress,
		SBDBAddress:          m.config.SBDBAddress,
		SSL:                  m.config.SSL,
		ConnectTimeout:       m.config.ConnectTimeout,
		ReconnectInterval:    1 * time.Second,
		MaxReconnectInterval: 60 * time.Second,
	}

	// Create OVN client
	client, err := NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create OVN client: %w", err)
	}

	// Connect to databases
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to external OVN databases: %w", err)
	}

	m.client = client
	m.connected = true
	m.lastHealthCheck = time.Now()
	m.reconnectAttempts = 0

	klog.Info("Successfully connected to external OVN databases")

	// Start health check goroutine
	go m.healthCheckLoop()

	return nil
}

// healthCheckLoop periodically checks the connection health
func (m *ExternalDBManager) healthCheckLoop() {
	defer close(m.healthCheckDone)

	ticker := time.NewTicker(m.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			klog.V(4).Info("Health check loop stopped")
			return
		case <-ticker.C:
			if err := m.performHealthCheck(); err != nil {
				klog.Warningf("Health check failed: %v", err)
				m.handleConnectionFailure()
			}
		}
	}
}

// performHealthCheck checks if the connection is healthy
func (m *ExternalDBManager) performHealthCheck() error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("client is nil")
	}

	if !client.IsConnected() {
		return fmt.Errorf("client is not connected")
	}

	// Try to list logical switches as a health check
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	lsOps := NewLogicalSwitchOps(client)
	_, err := lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return fmt.Errorf("health check query failed: %w", err)
	}

	m.mu.Lock()
	m.lastHealthCheck = time.Now()
	m.mu.Unlock()

	klog.V(4).Info("Health check passed")
	return nil
}

// handleConnectionFailure handles a connection failure
func (m *ExternalDBManager) handleConnectionFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.connected = false
	m.reconnectAttempts++

	klog.Warningf("Connection to external OVN databases lost (attempt %d)", m.reconnectAttempts)

	// Check if we've exceeded max reconnect attempts
	if m.config.MaxReconnectAttempts > 0 && m.reconnectAttempts > m.config.MaxReconnectAttempts {
		klog.Errorf("Exceeded maximum reconnection attempts (%d)", m.config.MaxReconnectAttempts)
		return
	}

	// Attempt reconnection in background
	go m.attemptReconnect()
}

// attemptReconnect attempts to reconnect to the external databases
func (m *ExternalDBManager) attemptReconnect() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	klog.Info("Attempting to reconnect to external OVN databases...")

	// Close existing client
	m.mu.Lock()
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
	m.mu.Unlock()

	// Create new client and connect
	clientConfig := &ClientConfig{
		NBDBAddress:          m.config.NBDBAddress,
		SBDBAddress:          m.config.SBDBAddress,
		SSL:                  m.config.SSL,
		ConnectTimeout:       m.config.ConnectTimeout,
		ReconnectInterval:    1 * time.Second,
		MaxReconnectInterval: 60 * time.Second,
	}

	client, err := NewClient(clientConfig)
	if err != nil {
		klog.Errorf("Failed to create OVN client for reconnection: %v", err)
		return
	}

	if err := client.Connect(ctx); err != nil {
		klog.Errorf("Failed to reconnect to external OVN databases: %v", err)
		return
	}

	m.mu.Lock()
	m.client = client
	m.connected = true
	m.lastHealthCheck = time.Now()
	m.reconnectAttempts = 0
	m.mu.Unlock()

	klog.Info("Successfully reconnected to external OVN databases")
}

// Close closes the connection to external databases
func (m *ExternalDBManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Signal shutdown
	select {
	case <-m.stopCh:
		// Already closed
	default:
		close(m.stopCh)
	}

	// Wait for health check to stop
	<-m.healthCheckDone

	if m.client != nil {
		m.client.Close()
		m.client = nil
	}

	m.connected = false
	klog.Info("External OVN database connections closed")
}

// IsConnected returns whether the manager is connected
func (m *ExternalDBManager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

// GetClient returns the underlying OVN client
func (m *ExternalDBManager) GetClient() *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client
}

// GetLastHealthCheck returns the time of the last successful health check
func (m *ExternalDBManager) GetLastHealthCheck() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastHealthCheck
}

// GetReconnectAttempts returns the current number of reconnection attempts
func (m *ExternalDBManager) GetReconnectAttempts() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.reconnectAttempts
}

// ValidateExternalConnection validates that the external OVN databases are accessible
// and properly configured for use with zstack-ovn-kubernetes
//
// This method performs the following checks:
// 1. Connection to NB DB is successful
// 2. Connection to SB DB is successful
// 3. Basic OVN schema is accessible
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - error: Validation error with details
func (m *ExternalDBManager) ValidateExternalConnection(ctx context.Context) error {
	m.mu.RLock()
	client := m.client
	connected := m.connected
	m.mu.RUnlock()

	if !connected || client == nil {
		return fmt.Errorf("not connected to external OVN databases")
	}

	// Validate NB DB connection
	if client.NBClient() == nil {
		return fmt.Errorf("NB DB client is not available")
	}

	// Validate SB DB connection (if configured)
	if m.config.SBDBAddress != "" && client.SBClient() == nil {
		return fmt.Errorf("SB DB client is not available")
	}

	// Try to list logical switches to verify NB DB access
	lsOps := NewLogicalSwitchOps(client)
	switches, err := lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return fmt.Errorf("failed to query NB DB: %w", err)
	}

	klog.V(4).Infof("External OVN database validation successful: found %d logical switches", len(switches))
	return nil
}

// GetExternalLogicalSwitches returns all logical switches from the external OVN database
// This is useful for discovering ZStack-managed logical switches
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - []*LogicalSwitch: List of logical switches
//   - error: Query error
func (m *ExternalDBManager) GetExternalLogicalSwitches(ctx context.Context) ([]*LogicalSwitch, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("not connected to external OVN databases")
	}

	lsOps := NewLogicalSwitchOps(client)
	return lsOps.ListLogicalSwitches(ctx)
}

// CheckLogicalSwitchExists checks if a logical switch exists in the external OVN database
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the logical switch
//
// Returns:
//   - bool: True if the switch exists
//   - error: Query error
func (m *ExternalDBManager) CheckLogicalSwitchExists(ctx context.Context, name string) (bool, error) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return false, fmt.Errorf("not connected to external OVN databases")
	}

	lsOps := NewLogicalSwitchOps(client)
	_, err := lsOps.GetLogicalSwitch(ctx, name)
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
