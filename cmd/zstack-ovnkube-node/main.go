// Package main provides the entry point for zstack-ovnkube-node.
//
// zstack-ovnkube-node is the node agent component that:
// - Runs as a DaemonSet on each Kubernetes node
// - Starts the CNI Server (Unix Socket) to handle CNI requests
// - Configures local OVS bridges and ports
// - Manages node-level networking (gateway, tunnels)
// - Coordinates with the controller for Pod network configuration
//
// The node agent is responsible for:
// 1. Starting the CNI Server that handles ADD/DEL/CHECK commands
// 2. Configuring OVS br-int bridge for Pod networking
// 3. Setting up VXLAN tunnels for cross-node communication
// 4. Configuring gateway for external traffic
//
// Usage:
//
//	zstack-ovnkube-node [flags]
//
// Flags:
//
//	--config string              Path to configuration file
//	--kubeconfig string          Path to kubeconfig file (default: in-cluster config)
//	--node-name string           Name of this node (default: from NODE_NAME env)
//	--cni-socket-path string     Path to CNI server socket (default: /var/run/zstack-ovn/cni-server.sock)
//	--metrics-bind-address       Address for metrics endpoint (default: :8082)
//	--health-probe-bind-address  Address for health probes (default: :8083)
//	--log-level string           Log level: debug, info, warn, error (default: info)
//
// Environment Variables:
//
//	NODE_NAME                    Name of this node (required)
//	ZSTACK_OVN_CONFIG_FILE       Path to configuration file
//	ZSTACK_OVN_MODE              Deployment mode: standalone or external
//	ZSTACK_OVN_NBDB_ADDRESS      OVN Northbound DB address
//	ZSTACK_OVN_SBDB_ADDRESS      OVN Southbound DB address
//
// Reference: OVN-Kubernetes cmd/ovnkube/ovnkube.go (node mode)
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/cni"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/node"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

var (
	// scheme is the runtime scheme for the node agent
	scheme = runtime.NewScheme()

	// Version information (set at build time)
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func init() {
	// Register standard Kubernetes types
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// Register our custom types (Subnet CRD)
	utilruntime.Must(networkv1.AddToScheme(scheme))

	// Register additional types needed
	utilruntime.Must(corev1.AddToScheme(scheme))
}

// Options contains command-line options for the node agent
type Options struct {
	// ConfigFile is the path to the configuration file
	ConfigFile string

	// Kubeconfig is the path to kubeconfig file
	Kubeconfig string

	// NodeName is the name of this node
	NodeName string

	// CNISocketPath is the path to CNI server socket
	CNISocketPath string

	// MetricsBindAddress is the address for metrics endpoint
	MetricsBindAddress string

	// HealthProbeBindAddress is the address for health probes
	HealthProbeBindAddress string

	// LogLevel is the log level
	LogLevel string

	// PrintVersion prints version information and exits
	PrintVersion bool
}

func main() {
	// Parse command-line flags
	opts := parseFlags()

	// Print version and exit if requested
	if opts.PrintVersion {
		printVersion()
		os.Exit(0)
	}

	// Initialize logging
	initLogging(opts.LogLevel)

	klog.Infof("Starting zstack-ovnkube-node %s (commit: %s, built: %s)",
		version, gitCommit, buildDate)

	// Validate node name
	if opts.NodeName == "" {
		klog.Fatal("Node name is required (use --node-name or NODE_NAME env var)")
	}
	klog.Infof("Running on node: %s", opts.NodeName)

	// Load configuration
	cfg, err := loadConfiguration(opts)
	if err != nil {
		klog.Fatalf("Failed to load configuration: %v", err)
	}

	klog.Infof("Configuration loaded: mode=%s, tunnelType=%s, gatewayMode=%s",
		cfg.OVN.Mode, cfg.Tunnel.Type, cfg.Gateway.Mode)

	// Create context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	setupSignalHandler(cancel)

	// Run the node agent
	if err := runNodeAgent(ctx, opts, cfg); err != nil {
		klog.Fatalf("Node agent failed: %v", err)
	}

	klog.Info("Node agent stopped")
}

// parseFlags parses command-line flags and returns Options
func parseFlags() *Options {
	opts := &Options{}

	flag.StringVar(&opts.ConfigFile, "config", "",
		"Path to configuration file (can also use ZSTACK_OVN_CONFIG_FILE env var)")
	flag.StringVar(&opts.Kubeconfig, "kubeconfig", "",
		"Path to kubeconfig file (default: in-cluster config)")
	flag.StringVar(&opts.NodeName, "node-name", os.Getenv("NODE_NAME"),
		"Name of this node (default: from NODE_NAME env var)")
	flag.StringVar(&opts.CNISocketPath, "cni-socket-path", cni.CNIServerSocketPath,
		"Path to CNI server socket")
	flag.StringVar(&opts.MetricsBindAddress, "metrics-bind-address", ":8082",
		"Address for metrics endpoint")
	flag.StringVar(&opts.HealthProbeBindAddress, "health-probe-bind-address", ":8083",
		"Address for health probes")
	flag.StringVar(&opts.LogLevel, "log-level", "info",
		"Log level: debug, info, warn, error")
	flag.BoolVar(&opts.PrintVersion, "version", false,
		"Print version information and exit")

	// Initialize klog flags
	klog.InitFlags(nil)

	flag.Parse()

	return opts
}

// initLogging initializes the logging system
func initLogging(level string) {
	// Configure zap logger for controller-runtime
	zapOpts := zap.Options{
		Development: level == "debug",
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	// Set klog verbosity based on log level
	switch level {
	case "debug":
		_ = flag.Set("v", "4")
	case "info":
		_ = flag.Set("v", "2")
	case "warn":
		_ = flag.Set("v", "1")
	case "error":
		_ = flag.Set("v", "0")
	}
}

// loadConfiguration loads the configuration from file and environment
func loadConfiguration(opts *Options) (*config.Config, error) {
	// Set config file from command line if provided
	if opts.ConfigFile != "" {
		os.Setenv("ZSTACK_OVN_CONFIG_FILE", opts.ConfigFile)
	}

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Override kubeconfig if provided via command line
	if opts.Kubeconfig != "" {
		cfg.Kubernetes.Kubeconfig = opts.Kubeconfig
	}

	return cfg, nil
}

// setupSignalHandler sets up signal handling for graceful shutdown
func setupSignalHandler(cancel context.CancelFunc) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		klog.Infof("Received signal %s, initiating shutdown...", sig)
		cancel()

		// Wait for second signal for force exit
		sig = <-sigCh
		klog.Infof("Received second signal %s, forcing exit", sig)
		os.Exit(1)
	}()
}

// printVersion prints version information
func printVersion() {
	fmt.Printf("zstack-ovnkube-node\n")
	fmt.Printf("  Version:    %s\n", version)
	fmt.Printf("  Git Commit: %s\n", gitCommit)
	fmt.Printf("  Build Date: %s\n", buildDate)
}

// runNodeAgent runs the main node agent loop
func runNodeAgent(ctx context.Context, opts *Options, cfg *config.Config) error {
	// Get kubeconfig
	kubeconfig := ctrl.GetConfigOrDie()
	if cfg.Kubernetes.Kubeconfig != "" {
		var err error
		kubeconfig, err = ctrl.GetConfig()
		if err != nil {
			return fmt.Errorf("failed to get kubeconfig: %w", err)
		}
	}

	// Create Kubernetes clients
	klog.Info("Creating Kubernetes clients...")
	kubeClient, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Create controller-runtime client for CRD access
	k8sClient, err := client.New(kubeconfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create controller-runtime manager for health checks and metrics
	klog.Info("Creating manager for health checks and metrics...")
	mgr, err := ctrl.NewManager(kubeconfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: opts.MetricsBindAddress,
		},
		HealthProbeBindAddress: opts.HealthProbeBindAddress,
		LeaderElection:         false, // Node agent doesn't need leader election
	})
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	// Create OVN client
	klog.Info("Connecting to OVN database...")
	ovnClient, err := createOVNClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create OVN client: %w", err)
	}
	defer ovnClient.Close()

	klog.Infof("Connected to OVN database (mode: %s)", cfg.OVN.Mode)

	// Create event recorder
	eventBroadcaster := record.NewBroadcaster()
	recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{
		Component: "zstack-ovnkube-node",
		Host:      opts.NodeName,
	})

	// Initialize OVS configuration
	klog.Info("Configuring local OVS...")
	if err := configureOVS(ctx, cfg, opts.NodeName); err != nil {
		return fmt.Errorf("failed to configure OVS: %w", err)
	}

	// Create and start CNI Server
	klog.Info("Starting CNI Server...")
	cniHandler := cni.NewHandler(k8sClient, ovnClient, cfg.Network.MTU)
	cniServer := cni.NewServer(opts.CNISocketPath, cniHandler)
	if err := cniServer.Start(); err != nil {
		return fmt.Errorf("failed to start CNI server: %w", err)
	}
	defer cniServer.Stop()

	klog.Infof("CNI Server started on %s", opts.CNISocketPath)

	// Create and start Node Controller
	klog.Info("Starting Node Controller...")
	nodeController, err := node.NewNodeController(
		mgr.GetClient(),
		kubeClient,
		cfg,
		ovnClient,
		recorder,
	)
	if err != nil {
		return fmt.Errorf("failed to create node controller: %w", err)
	}

	// Sync existing nodes on startup
	if err := nodeController.SyncExistingNodes(ctx); err != nil {
		klog.Warningf("Failed to sync existing nodes: %v", err)
	}

	// Setup node controller with manager
	if err := nodeController.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup node controller: %w", err)
	}

	// Configure gateway
	klog.Info("Configuring gateway...")
	if err := configureGateway(ctx, cfg, opts.NodeName, ovnClient); err != nil {
		klog.Warningf("Failed to configure gateway: %v", err)
		// Continue anyway - gateway configuration may not be critical
	}

	// Configure tunnels
	klog.Info("Configuring tunnels...")
	if err := configureTunnels(ctx, cfg, opts.NodeName, ovnClient); err != nil {
		klog.Warningf("Failed to configure tunnels: %v", err)
		// Continue anyway - tunnels may be configured later
	}

	// Add health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", func(_ *http.Request) error {
		// Check if CNI server is running
		if !cniServer.IsRunning() {
			return fmt.Errorf("CNI server is not running")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to add readyz check: %w", err)
	}

	// Start the manager
	klog.Info("Starting node agent manager...")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager failed: %w", err)
	}

	return nil
}

// createOVNClient creates an OVN database client with retry logic.
//
// This function attempts to connect to the OVN databases with exponential
// backoff retry. In external mode, it connects to ZStack-managed OVN databases.
// In standalone mode, it connects to self-managed databases.
//
// Parameters:
//   - ctx: Context for cancellation
//   - cfg: Configuration containing OVN database addresses
//
// Returns:
//   - *ovndb.Client: Connected OVN client
//   - error: Connection error after retries exhausted
func createOVNClient(ctx context.Context, cfg *config.Config) (*ovndb.Client, error) {
	// Build OVN client configuration
	clientConfig := &ovndb.ClientConfig{
		NBDBAddress:          cfg.GetNBDBAddress(),
		SBDBAddress:          cfg.GetSBDBAddress(),
		ConnectTimeout:       30 * time.Second,
		ReconnectInterval:    1 * time.Second,
		MaxReconnectInterval: 60 * time.Second,
		TxnTimeout:           30 * time.Second,
	}

	// Configure SSL if certificates are provided
	if cfg.OVN.SSL.CACert != "" && cfg.OVN.SSL.ClientCert != "" && cfg.OVN.SSL.ClientKey != "" {
		clientConfig.SSL = &ovndb.SSLConfig{
			CACert:     cfg.OVN.SSL.CACert,
			ClientCert: cfg.OVN.SSL.ClientCert,
			ClientKey:  cfg.OVN.SSL.ClientKey,
		}
	}

	// Create OVN client
	ovnClient, err := ovndb.NewClient(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create OVN client: %w", err)
	}

	// Connect with retry
	klog.Info("Connecting to OVN databases...")
	if err := ovnClient.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to OVN databases: %w", err)
	}

	return ovnClient, nil
}

// configureOVS configures the local OVS instance for Pod networking.
//
// This function:
// 1. Ensures br-int (integration bridge) exists
// 2. Sets OVS external_ids for OVN integration
// 3. Configures system-id for chassis identification
//
// Parameters:
//   - ctx: Context for cancellation
//   - cfg: Configuration
//   - nodeName: Name of this node (used as system-id)
//
// Returns:
//   - error: Configuration error
func configureOVS(ctx context.Context, cfg *config.Config, nodeName string) error {
	klog.V(4).Infof("Configuring OVS on node %s", nodeName)

	// Ensure br-int exists
	if err := ovsVsctl("--may-exist", "add-br", "br-int"); err != nil {
		return fmt.Errorf("failed to create br-int: %w", err)
	}

	// Set system-id (used as chassis name in OVN)
	if err := ovsVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:system-id=%s", nodeName)); err != nil {
		return fmt.Errorf("failed to set system-id: %w", err)
	}

	// Set hostname
	if err := ovsVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:hostname=%s", nodeName)); err != nil {
		klog.Warningf("Failed to set hostname: %v", err)
	}

	// Set OVN integration bridge
	if err := ovsVsctl("set", "Open_vSwitch", ".",
		"external_ids:ovn-bridge=br-int"); err != nil {
		return fmt.Errorf("failed to set ovn-bridge: %w", err)
	}

	// Configure OVN remote (Southbound DB address) for ovn-controller
	if cfg.IsExternalMode() {
		sbAddr := cfg.GetSBDBAddress()
		if err := ovsVsctl("set", "Open_vSwitch", ".",
			fmt.Sprintf("external_ids:ovn-remote=%s", sbAddr)); err != nil {
			return fmt.Errorf("failed to set ovn-remote: %w", err)
		}
		klog.Infof("Configured OVN remote: %s", sbAddr)
	}

	klog.Infof("OVS configured successfully on node %s", nodeName)
	return nil
}

// ovsVsctl executes an ovs-vsctl command.
func ovsVsctl(args ...string) error {
	cmd := exec.Command("ovs-vsctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ovs-vsctl %v failed: %w, output: %s", args, err, string(output))
	}
	return nil
}

// configureGateway configures the gateway for external traffic.
//
// This function creates a GatewayController and configures the gateway
// based on the configured mode (local or shared).
//
// Parameters:
//   - ctx: Context for cancellation
//   - cfg: Configuration
//   - nodeName: Name of this node
//   - ovnClient: OVN database client
//
// Returns:
//   - error: Configuration error
func configureGateway(ctx context.Context, cfg *config.Config, nodeName string, ovnClient *ovndb.Client) error {
	// Skip gateway configuration if not enabled
	if cfg.Gateway.Mode == "" || cfg.Gateway.Mode == "disabled" {
		klog.Info("Gateway configuration is disabled")
		return nil
	}

	// Create gateway controller
	gatewayController, err := node.NewGatewayController(cfg, nodeName)
	if err != nil {
		return fmt.Errorf("failed to create gateway controller: %w", err)
	}

	// Validate configuration
	if err := gatewayController.ValidateGatewayConfig(); err != nil {
		return fmt.Errorf("invalid gateway configuration: %w", err)
	}

	// Configure gateway
	if err := gatewayController.Configure(ctx); err != nil {
		return fmt.Errorf("failed to configure gateway: %w", err)
	}

	klog.Infof("Gateway configured: mode=%s, nodeIP=%s",
		gatewayController.GetGatewayMode(),
		gatewayController.GetNodeIP())

	return nil
}

// configureTunnels configures VXLAN/Geneve tunnels for cross-node communication.
//
// This function creates a TunnelController and configures tunnel encapsulation
// for OVN overlay networking.
//
// Parameters:
//   - ctx: Context for cancellation
//   - cfg: Configuration
//   - nodeName: Name of this node
//   - ovnClient: OVN database client
//
// Returns:
//   - error: Configuration error
func configureTunnels(ctx context.Context, cfg *config.Config, nodeName string, ovnClient *ovndb.Client) error {
	// Create tunnel controller
	tunnelController, err := node.NewTunnelController(cfg, nodeName)
	if err != nil {
		return fmt.Errorf("failed to create tunnel controller: %w", err)
	}

	// Validate configuration
	if err := tunnelController.ValidateTunnelConfig(); err != nil {
		return fmt.Errorf("invalid tunnel configuration: %w", err)
	}

	// Configure tunnels
	if err := tunnelController.Configure(ctx); err != nil {
		return fmt.Errorf("failed to configure tunnels: %w", err)
	}

	klog.Infof("Tunnels configured: type=%s, localIP=%s, port=%d",
		tunnelController.GetTunnelType(),
		tunnelController.GetLocalIP(),
		tunnelController.GetTunnelPort())

	return nil
}
