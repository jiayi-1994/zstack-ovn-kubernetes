#!/bin/bash
# Script to cleanup duplicate logical switches in OVN
# Run this on a node with access to ovn-nbctl

set -e

echo "=== Cleaning up duplicate OVN Logical Switches ==="

# Get all switch names and their UUIDs
echo "Listing all logical switches..."
ovn-nbctl --format=table --no-headings --columns=_uuid,name list Logical_Switch > /tmp/switches.txt

# Find duplicates
echo "Finding duplicates..."
declare -A switch_uuids
declare -A switch_counts

while read -r uuid name; do
    if [[ -n "$name" ]]; then
        switch_counts[$name]=$((${switch_counts[$name]:-0} + 1))
        switch_uuids[$name]="${switch_uuids[$name]} $uuid"
    fi
done < /tmp/switches.txt

# Process duplicates
for name in "${!switch_counts[@]}"; do
    count=${switch_counts[$name]}
    if [[ $count -gt 1 ]]; then
        echo ""
        echo "Found $count switches with name: $name"
        
        # Get UUIDs for this name
        uuids=(${switch_uuids[$name]})
        
        # Find which one has ports (we'll keep that one)
        keep_uuid=""
        for uuid in "${uuids[@]}"; do
            ports=$(ovn-nbctl --format=table --no-headings get Logical_Switch $uuid ports 2>/dev/null || echo "[]")
            if [[ "$ports" != "[]" && -n "$ports" ]]; then
                keep_uuid=$uuid
                echo "  Keeping $uuid (has ports)"
                break
            fi
        done
        
        # If none have ports, keep the first one
        if [[ -z "$keep_uuid" ]]; then
            keep_uuid=${uuids[0]}
            echo "  Keeping $keep_uuid (first one, no ports)"
        fi
        
        # Delete the others
        for uuid in "${uuids[@]}"; do
            if [[ "$uuid" != "$keep_uuid" ]]; then
                echo "  Deleting duplicate: $uuid"
                ovn-nbctl ls-del $uuid 2>/dev/null || echo "    Failed to delete $uuid (may have references)"
            fi
        done
    fi
done

echo ""
echo "=== Cleanup complete ==="
echo ""
echo "Current switches:"
ovn-nbctl ls-list
