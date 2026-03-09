# Multi-Paxos Symmetric Communication Implementation

## Overview

This implementation successfully migrates the Multi-Paxos example from the old static configuration approach to the new symmetric communication model introduced in Gorums v0.11.0.

## Key Changes

### 1. From Dual Connections to Symmetric Channels

**Before:**
- Nodes created separate client (Manager) and server (Server) connections
- Each node maintained 2 connections per peer (one outbound, one inbound)
- Static configuration created at startup

**After:**
- Single `gorums.System` manages both client and server roles
- Bidirectional channels reused for both directions
- Dynamic configuration via `System.NewOutboundConfig()`
- ~50% reduction in total connections

### 2. Server-to-Server Communication

The Learn phase now demonstrates true server-initiated communication:

```go
// In Learn RPC handler
if p.proposerConfig != nil {
    go p.broadcastLearn(p.proposerConfig, req)
}

// broadcastLearn uses the proposer configuration to broadcast
func (p *PaxosServer) broadcastLearn(cfg gorums.Configuration, req *pb.LearnRequest) {
    cfgCtx := cfg.Context(ctx)
    learned := pb.Learn(cfgCtx, req)

    for learn := range learned.Seq() {
        if learn.NodeID == p.id {
            continue  // Skip self
        }
        // Process responses from other nodes
    }
}
```

### 3. Key API Changes

#### Server Initialization

```go
// OLD: Separate manager and server
mgr := pb.NewManager(...)
config, _ := gorums.NewConfiguration(mgr, ...)
srv := gorums.NewServer(gorums.WithConfiguration(&config))

// NEW: Unified system
sys, _ := gorums.NewSystem(addr,
    gorums.WithConfig(id, gorums.WithNodeList(nodeAddrs)))
proposerConfig, _ := sys.NewOutboundConfig(...)
```

#### Server Struct

```go
// OLD: Static configuration
type PaxosServer struct {
    config gorums.Configuration
}

// NEW: Dynamic configuration
type PaxosServer struct {
    proposerConfig pb.Configuration
}
```

#### Shutdown

```go
// OLD: Separate cleanup
srv.Stop()
mgr.Close()

// NEW: Unified cleanup
sys.Stop()
```

## Test Results

Running `./test_symmetric.sh` demonstrates:

1. **Successful Broadcasts:** All 3 nodes broadcast Learn messages to peers
   ```
   Node 1: Broadcast complete - 0 newly learned, 2 already knew, 0 errors
   Node 2: Broadcast complete - 0 newly learned, 2 already knew, 0 errors
   Node 3: Broadcast complete - 0 newly learned, 2 already knew, 0 errors
   ```

2. **Bidirectional Communication:** Each node successfully communicates with all peers

3. **Connection Efficiency:** Symmetric channels reduce connection overhead

## Implementation Details

### Timing Considerations

Bidirectional channel establishment requires proper sequencing:

```go
// 1. Start server listening
go func() {
    if err := sys.Serve(); err != nil {
        log.Fatalf("Server error: %v", err)
    }
}()

// 2. Wait for server to be ready
time.Sleep(200 * time.Millisecond)

// 3. Create outbound configuration
proposerConfig, err := sys.NewOutboundConfig(...)

// 4. Wait for connections to establish
time.Sleep(500 * time.Millisecond)
```

### proposerConfig vs ctx.Config()

Two configuration sources serve different purposes:

- **`proposerConfig`**: Created via `System.NewOutboundConfig()`, contains all nodes explicitly configured with outbound connections and NodeID metadata. Used for initiating calls to other nodes.

- **`ctx.Config()` (ServerCtx)**: Returns InboundManager's view of peers that have connected TO this node. Dynamic and reflects current active connections.

For server-initiated broadcasts, we use `proposerConfig` because it has complete peer information and established outbound channels.

### Learn Response Semantics

The Learn RPC returns `Learned: true` only if the node newly learned the value, `Learned: false` if it already knew it. Both are successful responses:

```go
// Already knew the value - still a successful delivery
if state.learnedValue == value {
    return &LearnResponse{Learned: false}
}

// Newly learned - also successful
state.learnedValue = value
return &LearnResponse{Learned: true}
```

Broadcast counting properly handles both cases:
- `newlyLearned`: Node learned value for the first time
- `alreadyLearned`: Node already had the value (successful delivery)
- `errorCount`: Connection or RPC failures

## Benefits

1. **Reduced Connections:** ~50% fewer TCP connections
2. **Simpler Code:** No separate manager and server lifecycle management
3. **Dynamic Discovery:** Nodes can discover peers at runtime via InboundManager
4. **Bidirectional Reuse:** Same channels for both directions
5. **Automatic Metadata:** NodeID automatically included in outbound calls

## Files Modified

- `server.go`: Complete rewrite of server initialization and Learn broadcasting
- `test_symmetric.sh`: Updated test script to verify symmetric communication
- Documentation: `SYMMETRIC_COMMUNICATION.md`, `MIGRATION_GUIDE.md`

## Running the Example

```bash
# Build
go build

# Start nodes (in separate terminals)
./paxos -id 1
./paxos -id 2
./paxos -id 3

# Propose a value
./paxos -propose -id 100 -values "hello"

# Or run automated test
./test_symmetric.sh
```

## Success Criteria ✅

- [x] Nodes start with symmetric channelsestablished
- [x] Proposer can reach all acceptors via quorum calls
- [x] Learn phase broadcasts from each node to all peers
- [x] Server-initiated calls work correctly
- [x] Proper response handling (both newly learned and already known)
- [x] No connection errors or timeouts
- [x] Clean shutdown of all components

## Future Improvements

1. **Dynamic Membership:** Use InboundManager to handle nodes joining/leaving
2. **Connection Retry:** Implement retry logic for failed outbound connections
3. **Health Checking:** Monitor connection health via InboundManager
4. **Load Balancing:** Use symmetric channels for more efficient request routing
