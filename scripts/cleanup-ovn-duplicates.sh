#!/bin/bash
# 清理 OVN 数据库中的重复数据
# 使用方法: 
#   kubectl exec -it -n zstack-ovn-kubernetes ovn-nb-db-xxx -- bash
#   然后运行此脚本，或者直接:
#   kubectl exec -n zstack-ovn-kubernetes ovn-nb-db-xxx -- bash -c "$(cat cleanup-ovn-duplicates.sh)"

set -e

echo "=== 清理 OVN 重复数据 ==="
echo ""

# 1. 清理重复的 ovn-cluster-router
echo "1. 清理重复的 ovn-cluster-router..."
ROUTER_UUIDS=$(ovn-nbctl --format=table --no-headings --columns=_uuid find Logical_Router name=ovn-cluster-router | awk '{print $1}')
ROUTER_COUNT=$(echo "$ROUTER_UUIDS" | grep -c . || echo 0)

if [ "$ROUTER_COUNT" -gt 1 ]; then
    echo "   发现 $ROUTER_COUNT 个 ovn-cluster-router，保留第一个，删除其余..."
    FIRST=true
    for uuid in $ROUTER_UUIDS; do
        if [ "$FIRST" = true ]; then
            echo "   保留: $uuid"
            FIRST=false
        else
            echo "   删除: $uuid"
            ovn-nbctl lr-del "$uuid" || true
        fi
    done
elif [ "$ROUTER_COUNT" -eq 1 ]; then
    echo "   只有 1 个 ovn-cluster-router，无需清理"
else
    echo "   没有找到 ovn-cluster-router"
fi

# 2. 清理重复的 join switch
echo ""
echo "2. 清理重复的 join switch..."
JOIN_UUIDS=$(ovn-nbctl --format=table --no-headings --columns=_uuid find Logical_Switch name=join | awk '{print $1}')
JOIN_COUNT=$(echo "$JOIN_UUIDS" | grep -c . || echo 0)

if [ "$JOIN_COUNT" -gt 1 ]; then
    echo "   发现 $JOIN_COUNT 个 join switch，保留第一个，删除其余..."
    FIRST=true
    for uuid in $JOIN_UUIDS; do
        if [ "$FIRST" = true ]; then
            echo "   保留: $uuid"
            FIRST=false
        else
            echo "   删除: $uuid"
            ovn-nbctl ls-del "$uuid" || true
        fi
    done
elif [ "$JOIN_COUNT" -eq 1 ]; then
    echo "   只有 1 个 join switch，无需清理"
else
    echo "   没有找到 join switch"
fi

# 3. 清理重复的 node-xxx switch
echo ""
echo "3. 清理重复的 node-* switch..."
NODE_NAMES=$(ovn-nbctl --format=table --no-headings --columns=name find Logical_Switch name~="node-" | sort -u)
for name in $NODE_NAMES; do
    UUIDS=$(ovn-nbctl --format=table --no-headings --columns=_uuid find Logical_Switch name="$name" | awk '{print $1}')
    COUNT=$(echo "$UUIDS" | grep -c . || echo 0)
    if [ "$COUNT" -gt 1 ]; then
        echo "   发现 $COUNT 个 $name，保留第一个，删除其余..."
        FIRST=true
        for uuid in $UUIDS; do
            if [ "$FIRST" = true ]; then
                FIRST=false
            else
                echo "   删除: $uuid"
                ovn-nbctl ls-del "$uuid" || true
            fi
        done
    fi
done

# 4. 清理重复的 subnet-default switch
echo ""
echo "4. 清理重复的 subnet-default switch..."
SUBNET_UUIDS=$(ovn-nbctl --format=table --no-headings --columns=_uuid find Logical_Switch name=subnet-default | awk '{print $1}')
SUBNET_COUNT=$(echo "$SUBNET_UUIDS" | grep -c . || echo 0)

if [ "$SUBNET_COUNT" -gt 1 ]; then
    echo "   发现 $SUBNET_COUNT 个 subnet-default，保留第一个，删除其余..."
    FIRST=true
    for uuid in $SUBNET_UUIDS; do
        if [ "$FIRST" = true ]; then
            echo "   保留: $uuid"
            FIRST=false
        else
            echo "   删除: $uuid"
            ovn-nbctl ls-del "$uuid" || true
        fi
    done
elif [ "$SUBNET_COUNT" -eq 1 ]; then
    echo "   只有 1 个 subnet-default，无需清理"
else
    echo "   没有找到 subnet-default"
fi

# 5. 清理测试用的 switch
echo ""
echo "5. 清理测试用的 switch (test-ls-*)..."
TEST_SWITCHES=$(ovn-nbctl --format=table --no-headings --columns=_uuid,name find Logical_Switch name~="test-ls-" | awk '{print $1}')
for uuid in $TEST_SWITCHES; do
    echo "   删除测试 switch: $uuid"
    ovn-nbctl ls-del "$uuid" || true
done

echo ""
echo "=== 清理完成 ==="
echo ""
echo "当前状态:"
ovn-nbctl show | head -50
