#!/bin/bash
# Simple test script for Multi-Paxos

cd /home/ninh/gorums-ata/examples/paxos

echo "=== Starting Multi-Paxos Test ==="
echo ""

# Clean up any existing processes
pkill -f "paxos.*-id" 2>/dev/null || true
sleep 1

# Start three nodes in background (output goes to terminal)
echo "Starting 3 Paxos nodes..."
./paxos -id 1 &
PID1=$!
./paxos -id 2 &
PID2=$!
./paxos -id 3 &
PID3=$!

# Wait for nodes to start
sleep 1

echo "Nodes started: PIDs $PID1, $PID2, $PID3"
echo ""

# Run some proposals
echo "=== Proposing Values ==="
echo ""

./paxos -propose -id 100 -values "hello"
echo ""

./paxos -propose -id 200 -values "world"
echo ""

./paxos -propose -id 100 -values "alpha,beta,gamma"
echo ""

# Cleanup
echo "=== Cleaning Up ==="
kill $PID1 $PID2 $PID3 2>/dev/null
sleep 1
pkill -f "paxos.*-id" 2>/dev/null || true

echo ""
echo "=== Test Complete ==="
