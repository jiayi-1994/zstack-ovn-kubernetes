#!/bin/bash
# Cleanup duplicate logical switches in OVN
# This script removes duplicate switches with the same name, keeping only one

set -e

# Get OVN NB pod
OVN_NB_POD=$(kubectl -n zstack-ovn-kubernetes get pods -l app=ovn-nb-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

if [ -z "$OVN_NB_POD" ]; then
    echo "Error: OVN NB DB pod not found"
    exit 1
fi

echo "Using OVN NB pod: $OVN_NB_POD"

# Function to run ovn-nbctl command
ovn_nbctl() {
    kubectl -n zstack-ovn-kubernetes exec -i "$OVN_NB_POD" -- ovn-nbctl "$@"
}

# Get all switches with their UUIDs
echo "Listing all logical switches..."
SWITCHES=$(ovn_nbctl --format=table --no-headings --columns=_uuid,name find Logical_Switch)

# Group switches by name
declare -A SWITCH_GROUPS
while IFS= read -r line; do
    if [ -z "$line" ]; then continue; fi
    UUID=$(echo "$line" | awk '{print $1}')
    NAME=$(echo "$line" | awk '{print $2}')
    if [ -n "$NAME" ]; then
        SWITCH_GROUPS["$NAME"]="${SWITCH_GROUPS[$NAME]} $UUID"
    fi
done <<< "$SWITCHES"

# Find and remove duplicates
CLEANED=0
for NAME in "${!SWITCH_GROUPS[@]}"; do
    UUIDS=(${SWITCH_GROUPS[$NAME]})
    COUNT=${#UUIDS[@]}
    
    if [ $COUNT -gt 1 ]; then
        echo ""
        echo "Found $COUNT duplicate switches named '$NAME'"
        
        # Find the best switch to keep (one with ports)
        KEEP_UUID=""
        for UUID in "${UUIDS[@]}"; do
            PORTS=$(ovn_nbctl --format=table --no-headings get Logical_Switch "$UUID" ports 2>/dev/null || echo "")
            if [ -n "$PORTS" ] && [ "$PORTS" != "[]" ]; then
                KEEP_UUID="$UUID"
                echo "  Keeping $UUID (has ports)"
                break
            fi
        done
        
        # If no switch has ports, keep the first one
        if [ -z "$KEEP_UUID" ]; then
            KEEP_UUID="${UUIDS[0]}"
            echo "  Keeping $KEEP_UUID (first one, no ports found)"
        fi
        
        # Delete all other switches
        for UUID in "${UUIDS[@]}"; do
            if [ "$UUID" != "$KEEP_UUID" ]; then
                echo "  Deleting duplicate: $UUID"
                ovn_nbctl ls-del "$UUID" 2>/dev/null || echo "    Warning: Failed to delete $UUID"
                ((CLEANED++)) || true
            fi
        done
    fi
done

echo ""
echo "Cleanup complete. Removed $CLEANED duplicate switches."

# Show final state
echo ""
echo "Current logical switches:"
ovn_nbctl show | grep -E "^switch|port"
