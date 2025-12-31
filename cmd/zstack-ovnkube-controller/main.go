// Package main provides the entry point for zstack-ovnkube-controller.
//
// zstack-ovnkube-controller is the control plane component that:
// - Watches Kubernetes resources (Pods, Services, NetworkPolicies, Nodes)
// - Manages OVN logical objects (Logical Switches, Routers, Ports, Load Balancers, ACLs)
// - Handles IP allocation for Pods
// - Implements Kubernetes Service load balancing via OVN
//
// The controller supports two deployment modes:
// - standalone: Self-managed OVN databases (for development/testing)
// - external: Connect to external OVN databases (e.g., ZStack-managed)
//
// Usage:
//
//	zstack-ovnkube-controller [flags]
//
// Flags:
//
//	--config string              Path to configuration file (default: uses env vars)
//	--kubeconfig string          Path to kubeconfig file (default: in-cluster config)
//	--leader-elect               Enable leader election for HA (default: true)
//	--leader-elect-namespace     Namespace for leader election lease (default: kube-system)
//	--metrics-bind-address       Address for metrics endpoint (default: :8080)
//	--health-probe-bind-address  Address for health probes (default: :8081)
//	--log-level string           Log level: debug, info, warn, error (default: info)
//
// Environment Variables:
//
//	ZSTACK_OVN_CONFIG_FILE       Path to configuration file
//	ZSTACK_OVN_MODE              Deployment mode: standalone or external
//	ZSTACK_OVN_NBDB_ADDRESS      OVN Northbound DB address (for external mode)
//	ZSTACK_OVN_SBDB_ADDRESS      OVN Southbound DB address (for external mode)
//	ZSTACK_OVN_CLUSTER_CIDR      Pod network CIDR (default: 10.244.0.0/16)
//	ZSTACK_OVN_SERVICE_CIDR      Service network CIDR (default: 10.96.0.0/16)
//
// Reference: OVN-Kubernetes cmd/ovnkube/ovnkube.go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovn"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

var (
	// scheme is the runtime scheme for the controller
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

	// Register additional types needed by controllers
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
	utilruntime.Must(discoveryv1.AddToScheme(scheme))
}

// Options contains command-line options for the controller
type Options struct {
	// ConfigFile is the path to the configuration file
	ConfigFile string

	// Kubeconfig is the path to kubeconfig file
	Kubeconfig string

	// LeaderElect enables leader election for HA
	LeaderElect bool

	// LeaderElectNamespace is the namespace for leader election lease
	LeaderElectNamespace string

	// LeaderElectLeaseDuration is the duration of the leader election lease
	LeaderElectLeaseDuration time.Duration

	// LeaderElectRenewDeadline is the deadline for renewing the lease
	LeaderElectRenewDeadline time.Duration

	// LeaderElectRetryPeriod is the period between lease acquisition retries
	LeaderElectRetryPeriod time.Duration

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

	klog.Infof("Starting zstack-ovnkube-controller %s (commit: %s, built: %s)",
		version, gitCommit, buildDate)

	// Load configuration
	cfg, err := loadConfiguration(opts)
	if err != nil {
		klog.Fatalf("Failed to load configuration: %v", err)
	}

	klog.Infof("Configuration loaded: mode=%s, clusterCIDR=%s, serviceCIDR=%s",
		cfg.OVN.Mode, cfg.Network.ClusterCIDR, cfg.Network.ServiceCIDR)

	// Create context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	setupSignalHandler(cancel)

	// Run the controller
	if err := runController(ctx, opts, cfg); err != nil {
		klog.Fatalf("Controller failed: %v", err)
	}

	klog.Info("Controller stopped")
}

// parseFlags parses command-line flags and returns Options
func parseFlags() *Options {
	opts := &Options{}

	flag.StringVar(&opts.ConfigFile, "config", "",
		"Path to configuration file (can also use ZSTACK_OVN_CONFIG_FILE env var)")
	flag.StringVar(&opts.Kubeconfig, "kubeconfig", "",
		"Path to kubeconfig file (default: in-cluster config)")
	flag.BoolVar(&opts.LeaderElect, "leader-elect", true,
		"Enable leader election for high availability")
	flag.StringVar(&opts.LeaderElectNamespace, "leader-elect-namespace", "kube-system",
		"Namespace for leader election lease")
	flag.DurationVar(&opts.LeaderElectLeaseDuration, "leader-elect-lease-duration", 15*time.Second,
		"Duration of the leader election lease")
	flag.DurationVar(&opts.LeaderElectRenewDeadline, "leader-elect-renew-deadline", 10*time.Second,
		"Deadline for renewing the leader election lease")
	flag.DurationVar(&opts.LeaderElectRetryPeriod, "leader-elect-retry-period", 2*time.Second,
		"Period between leader election lease acquisition retries")
	flag.StringVar(&opts.MetricsBindAddress, "metrics-bind-address", ":8080",
		"Address for metrics endpoint")
	flag.StringVar(&opts.HealthProbeBindAddress, "health-probe-bind-address", ":8081",
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
	fmt.Printf("zstack-ovnkube-controller\n")
	fmt.Printf("  Version:    %s\n", version)
	fmt.Printf("  Git Commit: %s\n", gitCommit)
	fmt.Printf("  Build Date: %s\n", buildDate)
}

// runController runs the main controller loop
func runController(ctx context.Context, opts *Options, cfg *config.Config) error {
	// Get kubeconfig
	kubeconfig := ctrl.GetConfigOrDie()
	if cfg.Kubernetes.Kubeconfig != "" {
		var err error
		kubeconfig, err = ctrl.GetConfig()
		if err != nil {
			return fmt.Errorf("failed to get kubeconfig: %w", err)
		}
	}

	// Create controller-runtime manager
	klog.Info("Creating controller manager...")
	mgr, err := ctrl.NewManager(kubeconfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: opts.MetricsBindAddress,
		},
		HealthProbeBindAddress:  opts.HealthProbeBindAddress,
		LeaderElection:          opts.LeaderElect,
		LeaderElectionID:        "zstack-ovnkube-controller-leader",
		LeaderElectionNamespace: opts.LeaderElectNamespace,
		LeaseDuration:           &opts.LeaderElectLeaseDuration,
		RenewDeadline:           &opts.LeaderElectRenewDeadline,
		RetryPeriod:             &opts.LeaderElectRetryPeriod,
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
		Component: "zstack-ovnkube-controller",
	})

	// Register controllers
	if err := registerControllers(mgr, cfg, ovnClient, recorder); err != nil {
		return fmt.Errorf("failed to register controllers: %w", err)
	}

	// Add health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to add readyz check: %w", err)
	}

	// Start the manager
	klog.Info("Starting controller manager...")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager failed: %w", err)
	}

	return nil
}

// createOVNClient creates an OVN database client based on configuration
func createOVNClient(ctx context.Context, cfg *config.Config) (*ovndb.Client, error) {
	// Log the deployment mode
	if cfg.IsExternalMode() {
		klog.Infof("Running in EXTERNAL mode - connecting to ZStack-managed OVN databases")
		klog.Infof("  NB DB: %s", cfg.OVN.NBDBAddress)
		klog.Infof("  SB DB: %s", cfg.OVN.SBDBAddress)

		// Validate external mode configuration
		if err := cfg.ValidateExternalModeConfig(); err != nil {
			return nil, fmt.Errorf("external mode configuration validation failed: %w", err)
		}

		// Ensure we don't start local OVN processes
		if cfg.ShouldStartLocalOVN() {
			return nil, fmt.Errorf("internal error: ShouldStartLocalOVN returned true in external mode")
		}
	} else {
		klog.Info("Running in STANDALONE mode - using self-managed OVN databases")
	}

	clientConfig := &ovndb.ClientConfig{
		NBDBAddress: cfg.GetNBDBAddress(),
		SBDBAddress: cfg.GetSBDBAddress(),
	}

	// Configure SSL if enabled
	if cfg.OVN.SSL.Enabled {
		clientConfig.SSL = &ovndb.SSLConfig{
			CACert:     cfg.OVN.SSL.CACert,
			ClientCert: cfg.OVN.SSL.ClientCert,
			ClientKey:  cfg.OVN.SSL.ClientKey,
		}
		klog.Info("SSL enabled for OVN database connections")
	}

	// Set connection timeouts
	clientConfig.ConnectTimeout = cfg.OVN.ConnectTimeout
	clientConfig.ReconnectInterval = cfg.OVN.ReconnectInterval
	clientConfig.MaxReconnectInterval = cfg.OVN.MaxReconnectInterval

	// Create OVN client
	client, err := ovndb.NewClient(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create OVN client: %w", err)
	}

	// Connect with retry
	klog.Info("Connecting to OVN databases...")
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to OVN databases: %w", err)
	}

	// Validate connection in external mode
	if cfg.IsExternalMode() {
		klog.Info("Validating external OVN database connection...")
		if err := validateExternalConnection(ctx, client); err != nil {
			client.Close()
			return nil, fmt.Errorf("external OVN database validation failed: %w", err)
		}
		klog.Info("External OVN database connection validated successfully")
	}

	return client, nil
}

// validateExternalConnection validates that the external OVN databases are accessible
func validateExternalConnection(ctx context.Context, client *ovndb.Client) error {
	// Try to list logical switches to verify NB DB access
	lsOps := ovndb.NewLogicalSwitchOps(client)
	switches, err := lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return fmt.Errorf("failed to query NB DB: %w", err)
	}

	klog.V(4).Infof("Found %d logical switches in external OVN database", len(switches))

	// Log existing logical switches for debugging
	for _, ls := range switches {
		klog.V(4).Infof("  - Logical Switch: %s (UUID: %s)", ls.Name, ls.UUID)
	}

	return nil
}

// registerControllers registers all controllers with the manager
func registerControllers(
	mgr ctrl.Manager,
	cfg *config.Config,
	ovnClient *ovndb.Client,
	recorder record.EventRecorder,
) error {
	klog.Info("Registering controllers...")

	// 1. Register Subnet Controller
	// The Subnet controller manages Subnet CRD and OVN Logical Switches
	klog.V(2).Info("Registering Subnet controller")
	subnetReconciler := ovn.NewSubnetReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		recorder,
		cfg,
		ovnClient,
	)
	if err := subnetReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup Subnet controller: %w", err)
	}

	// 2. Register Pod Controller
	// The Pod controller manages Pod network configuration and OVN LSPs
	klog.V(2).Info("Registering Pod controller")
	podReconciler := ovn.NewPodReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		recorder,
		cfg,
		ovnClient,
		subnetReconciler,
	)
	if err := podReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup Pod controller: %w", err)
	}

	// 3. Register Service Controller
	// The Service controller manages Service load balancing via OVN Load Balancers
	klog.V(2).Info("Registering Service controller")
	serviceReconciler := ovn.NewServiceReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		recorder,
		cfg,
		ovnClient,
	)
	if err := serviceReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup Service controller: %w", err)
	}

	// 4. Register NetworkPolicy Controller
	// The Policy controller manages NetworkPolicy via OVN ACLs
	klog.V(2).Info("Registering NetworkPolicy controller")
	policyController := &ovn.PolicyController{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		OVNClient: ovnClient,
	}
	if err := policyController.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup NetworkPolicy controller: %w", err)
	}

	klog.Info("All controllers registered successfully")
	return nil
}
