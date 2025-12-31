#!/bin/bash
# node-entrypoint.sh
# Entrypoint script for zstack-ovnkube-node container
#
# This script handles:
# 1. Installing CNI binary to host (if not using init container)
# 2. Installing CNI configuration to host
# 3. Starting the node agent
#
# Environment variables:
#   INSTALL_CNI         - Set to "true" to install CNI binary (default: false)
#   CNI_BIN_DIR         - Host CNI binary directory (default: /host/opt/cni/bin)
#   CNI_CONF_DIR        - Host CNI config directory (default: /host/etc/cni/net.d)
#   CNI_CONF_NAME       - CNI config filename (default: 10-zstack-ovn.conflist)
#   NODE_NAME           - Kubernetes node name (required)
#   ZSTACK_OVN_MODE     - Deployment mode: standalone or external

set -e

echo "Starting zstack-ovnkube-node..."

# Default values
CNI_BIN_DIR="${CNI_BIN_DIR:-/host/opt/cni/bin}"
CNI_CONF_DIR="${CNI_CONF_DIR:-/host/etc/cni/net.d}"
CNI_CONF_NAME="${CNI_CONF_NAME:-10-zstack-ovn.conflist}"
INSTALL_CNI="${INSTALL_CNI:-false}"

# Install CNI binary if requested
if [ "$INSTALL_CNI" = "true" ]; then
    echo "Installing CNI binary..."
    if [ -d "$CNI_BIN_DIR" ]; then
        cp -f /opt/cni/bin/zstack-ovn-cni "$CNI_BIN_DIR/"
        chmod +x "$CNI_BIN_DIR/zstack-ovn-cni"
        echo "CNI binary installed to $CNI_BIN_DIR/zstack-ovn-cni"
    else
        echo "Warning: CNI binary directory $CNI_BIN_DIR not found"
    fi
fi

# Install CNI configuration
if [ -d "$CNI_CONF_DIR" ]; then
    # Check if we should install configuration
    if [ ! -f "$CNI_CONF_DIR/$CNI_CONF_NAME" ] || [ "$FORCE_CNI_CONF" = "true" ]; then
        echo "Installing CNI configuration..."
        
        # Generate CNI configuration from template
        if [ -f "/etc/cni/net.d/10-zstack-ovn.conflist.template" ]; then
            # Use template if available
            cp /etc/cni/net.d/10-zstack-ovn.conflist.template "$CNI_CONF_DIR/$CNI_CONF_NAME"
        else
            # Generate default configuration
            cat > "$CNI_CONF_DIR/$CNI_CONF_NAME" << EOF
{
  "cniVersion": "1.0.0",
  "name": "zstack-ovn",
  "plugins": [
    {
      "type": "zstack-ovn-cni",
      "serverSocket": "/var/run/zstack-ovn/cni-server.sock",
      "logFile": "/var/log/zstack-ovn/cni.log",
      "logLevel": "${LOG_LEVEL:-info}"
    }
  ]
}
EOF
        fi
        echo "CNI configuration installed to $CNI_CONF_DIR/$CNI_CONF_NAME"
    else
        echo "CNI configuration already exists at $CNI_CONF_DIR/$CNI_CONF_NAME"
    fi
else
    echo "Warning: CNI config directory $CNI_CONF_DIR not found"
fi

# Validate required environment variables
if [ -z "$NODE_NAME" ]; then
    echo "Error: NODE_NAME environment variable is required"
    exit 1
fi

echo "Node name: $NODE_NAME"
echo "OVN mode: ${ZSTACK_OVN_MODE:-standalone}"

# Create runtime directories
mkdir -p /var/run/zstack-ovn
mkdir -p /var/log/zstack-ovn

# Start the node agent
echo "Starting node agent..."
exec /usr/local/bin/zstack-ovnkube-node "$@"
