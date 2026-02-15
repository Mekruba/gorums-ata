# Broadcast Interceptor: Complete Guide

## Table of Contents
1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Code Structure](#code-structure)
4. [Request Flow](#request-flow)
5. [Loop Prevention](#loop-prevention)
6. [Type Safety with Generics](#type-safety-with-generics)
7. [Server Setup](#server-setup)
8. [Complete Example Flow](#complete-example-flow)
9. [Trade-offs and Limitations](#trade-offs-and-limitations)

---

## Overview

The **Broadcast Interceptor** is a middleware component that automatically replicates incoming RPC requests to all other servers in a cluster. It's implemented as a Gorums interceptor that sits in the request processing pipeline.

### Key Features
- ✅ **Transparent replication** - No changes to client or service handler code
- ✅ **Asynchronous broadcasting** - Doesn't block client responses
- ✅ **Loop prevention** - Content-based deduplication prevents infinite loops
- ✅ **Type-safe** - Uses Go generics for compile-time safety
- ✅ **Method-specific** - Only broadcasts selected RPC methods
- ✅ **Composable** - Works with other interceptors in the chain

### Use Cases
- Primary-backup replication
- Multi-master replication with eventual consistency
- Distributed cache invalidation
- Event broadcasting to all nodes
- Audit logging across cluster

---

## Architecture

### Component Diagram
```
┌─────────────────────────────────────────────────────────────────┐
│                        CLIENT                                    │
│                   (writes to any server)                         │
└────────────────────────────┬────────────────────────────────────┘
                             │ RPC Request
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                        SERVER 0                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │              INTERCEPTOR CHAIN                             │ │
│  │  ┌──────────────────────────────────────────────────────┐ │ │
│  │  │ 1. LoggingInterceptor                                │ │ │
│  │  ├──────────────────────────────────────────────────────┤ │ │
│  │  │ 2. ValidationInterceptor                             │ │ │
│  │  ├──────────────────────────────────────────────────────┤ │ │
│  │  │ 3. BroadcastInterceptor ◄─── REPLICATION LOGIC      │ │ │
│  │  │    • Check if already broadcasted (loop detection)   │ │ │
│  │  │    • Process locally first                           │ │ │
│  │  │    • Broadcast to other servers (async)              │ │ │
│  │  └──────────────────────────────────────────────────────┘ │ │
│  └──────────────────────────┬─────────────────────────────────┘ │
│                             ▼                                    │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │           ACTUAL HANDLER (WriteRPC)                        │ │
│  │           storage["key"] = "value" ✓                       │ │
│  └────────────────────────────────────────────────────────────┘ │
│                             │                                    │
│                             ├─→ Response to client (immediate)   │
│                             │                                    │
│                             └─→ Async broadcast ───────┐         │
└─────────────────────────────────────────────────────────┼────────┘
                                                          │
                    ┌─────────────────────────────────────┘
                    │ Fire-and-forget broadcasts
                    │
        ┌───────────┼────────────┬──────────────┐
        ▼           ▼            ▼              ▼
    SERVER 1    SERVER 2     SERVER 3      (All receive
    Processes   Processes    Processes       same write)
    locally     locally      locally
```

---

## Code Structure

### 1. Function Signature

```go
func NewBroadcastInterceptor[Req, Resp proto.Message](
    cfg gorums.Configuration, 
    method string,
) gorums.Interceptor
```

**Type Parameters:**
- `Req` - Request message type (e.g., `*proto.WriteRequest`)
- `Resp` - Response message type (e.g., `*proto.WriteResponse`)

**Parameters:**
- `cfg` - Configuration containing the list of nodes to broadcast to
- `method` - Full RPC method name to intercept (e.g., `"proto.Storage.WriteRPC"`)

**Returns:**
- `gorums.Interceptor` - A function that processes each incoming request

### 2. Closure State (Persistent Across Requests)

```go
func NewBroadcastInterceptor[Req, Resp proto.Message](cfg gorums.Configuration, method string) gorums.Interceptor {
    // ┌─────────────────────────────────────────────────────────┐
    // │ CLOSURE STATE - Persists for lifetime of interceptor   │
    // └─────────────────────────────────────────────────────────┘
    
    var mu sync.Mutex
    broadcastedHashes := make(map[string]struct{})
    
    // ↑ This state is shared across ALL requests to this server
    // Used for loop detection
    
    return func(ctx gorums.ServerCtx, msg *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
        // Interceptor logic here...
    }
}
```

**Why closure state?**
- Each server needs to remember which messages it has already broadcasted
- Prevents infinite broadcast loops
- Thread-safe via mutex

### 3. The Returned Interceptor Function

```go
return func(ctx gorums.ServerCtx, msg *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
    // ┌──────────────────────────────────────────────────┐
    // │ STEP 1: Method filtering                         │
    // └──────────────────────────────────────────────────┘
    if msg.GetMethod() != method {
        return next(ctx, msg)  // Not the target method, pass through
    }

    // ┌──────────────────────────────────────────────────┐
    // │ STEP 2: Content-based deduplication              │
    // └──────────────────────────────────────────────────┘
    msgBytes, err := proto.Marshal(msg.Msg)
    if err != nil {
        log.Printf("BroadcastInterceptor: Failed to marshal: %v", err)
        return next(ctx, msg)
    }
    hash := sha256.Sum256(msgBytes)
    hashStr := fmt.Sprintf("%x", hash[:8])  // Use first 8 bytes

    // ┌──────────────────────────────────────────────────┐
    // │ STEP 3: Check if already broadcasted             │
    // └──────────────────────────────────────────────────┘
    mu.Lock()
    _, alreadyBroadcasted := broadcastedHashes[hashStr]
    if !alreadyBroadcasted {
        broadcastedHashes[hashStr] = struct{}{}
        
        // Cache cleanup to prevent memory leak
        if len(broadcastedHashes) > 10000 {
            count := 0
            for h := range broadcastedHashes {
                delete(broadcastedHashes, h)
                count++
                if count >= 5000 {
                    break
                }
            }
        }
    }
    mu.Unlock()

    if alreadyBroadcasted {
        // Already broadcasted, just process locally
        return next(ctx, msg)
    }

    // ┌──────────────────────────────────────────────────┐
    // │ STEP 4: Process locally FIRST (synchronous)      │
    // └──────────────────────────────────────────────────┘
    resp, err := next(ctx, msg)

    // ┌──────────────────────────────────────────────────┐
    // │ STEP 5: Broadcast to other nodes (asynchronous)  │
    // └──────────────────────────────────────────────────┘
    go func() {
        broadcastCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()

        // Type assert and clone the message
        req, ok := msg.Msg.(Req)
        if !ok {
            log.Printf("BroadcastInterceptor: Type assertion failed")
            return
        }
        msgCopy := proto.Clone(req).(Req)

        // Broadcast to all nodes in parallel
        var wg sync.WaitGroup
        for _, node := range cfg.Nodes() {
            wg.Add(1)
            go func(n *gorums.Node) {
                defer wg.Done()
                nodeCtx := n.Context(broadcastCtx)
                
                // Make typed RPC call
                _, _ = gorums.RPCCall[Req, Resp](nodeCtx, msgCopy, method)
                // Errors silently ignored (fire-and-forget)
            }(node)
        }
        wg.Wait()
    }()

    // ┌──────────────────────────────────────────────────┐
    // │ STEP 6: Return response to client immediately    │
    // └──────────────────────────────────────────────────┘
    return resp, err
}
```

---

## Request Flow

### Detailed Step-by-Step Flow

Let's trace what happens when a client writes `mykey = myvalue` to Server 0:

#### **Phase 1: Client Request Arrives at Server 0**

```
Time: T0
Event: Client executes: rpc 0 write mykey myvalue

┌────────────────────────────────────┐
│          CLIENT                    │
│   msg_id: 001                      │
│   method: "proto.Storage.WriteRPC" │
│   key: "mykey"                     │
│   value: "myvalue"                 │
└──────────────┬─────────────────────┘
               │
               ▼
      [Network: gRPC call]
               │
               ▼
┌────────────────────────────────────┐
│         SERVER 0                   │
│  Receives gRPC request             │
└────────────────────────────────────┘
```

#### **Phase 2: Interceptor Chain Processing**

```
Time: T1
Event: Request enters interceptor chain

SERVER 0 Interceptor Chain:
┌─────────────────────────────────────────┐
│ 1. LoggingInterceptor                   │
│    Action: Logs request details         │
│    Output: "WriteRPC(key=mykey...)"     │
├─────────────────────────────────────────┤
│ 2. NoFooAllowedInterceptor              │
│    Action: Check if key == "foo"        │
│    Result: ✓ Pass (key != "foo")        │
├─────────────────────────────────────────┤
│ 3. MetadataInterceptor                  │
│    Action: Add custom metadata          │
│    Result: metadata["customKey"] = "..." │
├─────────────────────────────────────────┤
│ 4. BroadcastInterceptor ◄─── WE START HERE
│    [Detailed processing below]          │
└─────────────────────────────────────────┘
```

#### **Phase 3: Broadcast Interceptor Processing**

```go
// ═══════════════════════════════════════════════════════════
// STEP 1: Method Check
// ═══════════════════════════════════════════════════════════
msg.GetMethod() == "proto.Storage.WriteRPC" ?
✓ YES → Continue processing
```

```go
// ═══════════════════════════════════════════════════════════
// STEP 2: Content Hashing
// ═══════════════════════════════════════════════════════════
Original message:
{
  "key": "mykey",
  "value": "myvalue"
}

Marshal to bytes: [0x0a, 0x05, 0x6d, 0x79, 0x6b, ...]

SHA-256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855

First 8 bytes (hex): "e3b0c442"

hash_str = "e3b0c442"
```

```go
// ═══════════════════════════════════════════════════════════
// STEP 3: Loop Detection Check
// ═══════════════════════════════════════════════════════════
Server 0's broadcast cache state:
broadcastedHashes = {} // Empty (first time seeing this message)

Check: broadcastedHashes["e3b0c442"] exists?
Result: NO (not found)

Action: Add to cache
broadcastedHashes["e3b0c442"] = struct{}{}

Decision: PROCEED WITH BROADCAST
```

```go
// ═══════════════════════════════════════════════════════════
// STEP 4: Local Processing (Synchronous)
// ═══════════════════════════════════════════════════════════
Time: T2

Call: next(ctx, msg)
↓
Enters actual handler: storageServer.WriteRPC()
↓
storage.mut.Lock()
storage.storage["mykey"] = state{
    Value: "myvalue",
    Time:  time.Now(),
}
storage.mut.Unlock()
↓
Returns: &WriteResponse{OK: true}

Time: T3 (elapsed: ~1-2ms)
```

#### **Phase 4: Response to Client**

```
Time: T3
Event: Client receives response

SERVER 0 → CLIENT
Response: WriteResponse{OK: true}
Duration: ~2ms (fast! no waiting for broadcasts)

┌────────────────────────────────────┐
│          CLIENT                    │
│   Receives: Write OK               │
│   Duration: 2ms                    │
└────────────────────────────────────┘

✓ Client is done!
```

#### **Phase 5: Asynchronous Broadcasting**

```
Time: T3 (parallel with client response)
Event: Background goroutine spawned

Thread: Background Goroutine
┌─────────────────────────────────────────────────────────────┐
│ Broadcast Goroutine (runs in background)                    │
│                                                              │
│ 1. Clone message:                                            │
│    msgCopy = proto.Clone({"key": "mykey", "value": ...})    │
│                                                              │
│ 2. For each node in cfg.Nodes():                            │
│    - cfg.Nodes() = [Server1, Server2, Server3]              │
│      (Note: Server 0 not in list - excluded during setup)   │
│                                                              │
│ 3. Spawn goroutine for each target:                         │
│    ┌──────────────────────────────────────────────────────┐ │
│    │ Goroutine 1: Broadcast to Server 1                   │ │
│    │   RPCCall[*WriteRequest, *WriteResponse](            │ │
│    │       node1.Context(ctx),                            │ │
│    │       msgCopy,                                       │ │
│    │       "proto.Storage.WriteRPC"                       │ │
│    │   )                                                  │ │
│    └──────────────────────────────────────────────────────┘ │
│    ┌──────────────────────────────────────────────────────┐ │
│    │ Goroutine 2: Broadcast to Server 2                   │ │
│    │   (same RPC call)                                    │ │
│    └──────────────────────────────────────────────────────┘ │
│    ┌──────────────────────────────────────────────────────┐ │
│    │ Goroutine 3: Broadcast to Server 3                   │ │
│    │   (same RPC call)                                    │ │
│    └──────────────────────────────────────────────────────┘ │
│                                                              │
│ 4. wg.Wait() - Wait for all broadcasts to complete          │
│    Duration: ~5-10ms (depends on network)                   │
└─────────────────────────────────────────────────────────────┘
```

#### **Phase 6: Broadcast Arrives at Other Servers**

```
Time: T4 (a few milliseconds after T3)
Event: Servers 1, 2, 3 receive the broadcast

┌────────────────────────────────────────────────────────────┐
│                       SERVER 1                              │
│  Receives WriteRPC from Server 0                            │
│                                                              │
│  Broadcast Interceptor Processing:                          │
│  1. Check method: ✓ "proto.Storage.WriteRPC"                │
│  2. Hash content: "e3b0c442"                                 │
│  3. Check cache: broadcastedHashes["e3b0c442"]?              │
│     Result: NOT FOUND (first time)                           │
│  4. Add to cache: broadcastedHashes["e3b0c442"] = {}         │
│  5. Process locally: storage["mykey"] = "myvalue" ✓          │
│  6. Spawn broadcast goroutine → broadcasts to [0,2,3]        │
│     (But Server 0 will detect it's already seen this!)       │
└────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────┐
│                       SERVER 2                              │
│  (Same processing as Server 1)                              │
│  - Processes locally ✓                                       │
│  - Broadcasts to [0,1,3]                                     │
└────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────┐
│                       SERVER 3                              │
│  (Same processing as Server 1)                              │
│  - Processes locally ✓                                       │
│  - Broadcasts to [0,1,2]                                     │
└────────────────────────────────────────────────────────────┘
```

#### **Phase 7: Loop Prevention in Action**

```
Time: T5
Event: Server 0 receives broadcast FROM Server 1

┌────────────────────────────────────────────────────────────┐
│                       SERVER 0                              │
│  Receives WriteRPC FROM Server 1 (not from client!)         │
│                                                              │
│  Message:                                                    │
│    msg_id: 456 (NEW ID! Different from original 001)        │
│    content: {"key": "mykey", "value": "myvalue"}            │
│                                                              │
│  Broadcast Interceptor Processing:                          │
│  1. Check method: ✓ "proto.Storage.WriteRPC"                │
│  2. Hash content: "e3b0c442"  ◄─ SAME HASH!                 │
│  3. Check cache: broadcastedHashes["e3b0c442"]?              │
│     Result: ✓ FOUND!  (we broadcasted this at T3)           │
│  4. Decision: SKIP BROADCAST                                 │
│  5. Only process locally (idempotent write)                  │
│                                                              │
│  🛡️ LOOP PREVENTED! No re-broadcast!                        │
└────────────────────────────────────────────────────────────┘
```

---

## Loop Prevention

### Why Loop Prevention is Critical

**Without loop detection, you get infinite cascading broadcasts:**

```
❌ WITHOUT LOOP PREVENTION:

Client → Server 0: write "test:1"
         ↓
Server 0 broadcasts to [1,2,3]
         ↓
Server 1 receives, broadcasts to [0,2,3]
         ↓
Server 0 receives, broadcasts to [1,2,3]  ← AGAIN!
         ↓
Server 1 receives, broadcasts to [0,2,3]  ← INFINITE LOOP!
         ↓
... continues forever ...
```

### Content-Based Deduplication Strategy

**Why hash the content instead of using message IDs?**

```go
// Problem with message IDs:
Original client request:  msg_id=001, content="write mykey myvalue"
Server 0 → Server 1:      msg_id=456, content="write mykey myvalue"  (NEW ID!)
Server 1 → Server 0:      msg_id=789, content="write mykey myvalue"  (ANOTHER NEW ID!)

// Each gorums.RPCCall() creates a NEW message with a NEW ID
// We can't use msg_id for deduplication!

// Solution: Hash the content
Original:       hash("write mykey myvalue") = "e3b0c442"
S0 → S1:        hash("write mykey myvalue") = "e3b0c442"  (SAME!)
S1 → S0:        hash("write mykey myvalue") = "e3b0c442"  (SAME!)

// Content is identical, so hash is identical
// Perfect for deduplication!
```

### Hash Cache Implementation

```go
// Thread-safe cache
var mu sync.Mutex
broadcastedHashes := make(map[string]struct{})

// Store hash
mu.Lock()
broadcastedHashes["e3b0c442"] = struct{}{}
mu.Unlock()

// Check hash
mu.Lock()
_, exists := broadcastedHashes["e3b0c442"]
mu.Unlock()
```

**Cache cleanup to prevent memory leak:**

```go
if len(broadcastedHashes) > 10000 {
    // When cache grows too large, evict oldest 50%
    count := 0
    for h := range broadcastedHashes {
        delete(broadcastedHashes, h)
        count++
        if count >= 5000 {
            break
        }
    }
}
```

### Complete Loop Prevention Example

```
Timeline of one write request:

T0: Client → Server 0: write "key:val"
    Server 0 cache: {}
    Server 1 cache: {}
    Server 2 cache: {}

T1: Server 0 processes:
    hash = "abc123"
    cache check: "abc123" not in cache
    Server 0 cache: {"abc123"}  ← Added
    Action: Process locally + broadcast to [1,2]

T2: Server 1 receives broadcast from Server 0:
    hash = "abc123"
    cache check: "abc123" not in cache
    Server 1 cache: {"abc123"}  ← Added
    Action: Process locally + broadcast to [0,2]

T3: Server 2 receives broadcast from Server 0:
    hash = "abc123"
    cache check: "abc123" not in cache
    Server 2 cache: {"abc123"}  ← Added
    Action: Process locally + broadcast to [0,1]

T4: Server 0 receives broadcast from Server 1:
    hash = "abc123"
    cache check: "abc123" FOUND!  ← Already in cache!
    Server 0 cache: {"abc123"}  (unchanged)
    Action: Process locally ONLY, skip broadcast
    ✓ Loop prevented!

T5: Server 0 receives broadcast from Server 2:
    hash = "abc123"
    cache check: "abc123" FOUND!  ← Already in cache!
    Action: Process locally ONLY, skip broadcast
    ✓ Loop prevented!

T6: All servers have processed the write
    Broadcasts stabilize
    System reaches quiescence
```

---

## Type Safety with Generics

### Why Generics?

The interceptor uses Go generics to ensure compile-time type safety:

```go
func NewBroadcastInterceptor[Req, Resp proto.Message](
    cfg gorums.Configuration, 
    method string,
) gorums.Interceptor
```

### Type Flow

**1. At Creation (Compile Time):**

```go
// Server setup code
broadcastInterceptor := interceptors.NewBroadcastInterceptor[
    *pb.WriteRequest,   // ← Req type specified
    *pb.WriteResponse,  // ← Resp type specified
](clientCfg, "proto.Storage.WriteRPC")

// Compiler now knows:
// - Req = *pb.WriteRequest
// - Resp = *pb.WriteResponse
```

**2. At Runtime (Type Assertions):**

```go
// Inside the interceptor
req, ok := msg.Msg.(Req)  // Type assert to *pb.WriteRequest
if !ok {
    // This should never happen if types match
    return
}

// Clone with correct type
msgCopy := proto.Clone(req).(Req)  // Returns *pb.WriteRequest
```

**3. In RPC Call:**

```go
// gorums.RPCCall is also generic
_, _ = gorums.RPCCall[Req, Resp](nodeCtx, msgCopy, method)
//                    ^^^  ^^^^
//                    |    |
//        *pb.WriteRequest |
//                         *pb.WriteResponse

// This ensures:
// - Request parameter must be *pb.WriteRequest
// - Response will be *pb.WriteResponse
// - No runtime type mismatches possible
```

### Benefits

✅ **Compile-time safety** - Type errors caught at build time
✅ **No runtime panics** - Type assertions are guaranteed to work
✅ **IDE autocomplete** - Better developer experience
✅ **Code clarity** - Explicit about what types are expected

---

## Server Setup

### 1. Server Configuration (main.go)

```go
// Allocate fixed ports for each server
listeners := []string{
    "127.0.0.1:50000",  // Server 0
    "127.0.0.1:50001",  // Server 1
    "127.0.0.1:50002",  // Server 2
    "127.0.0.1:50003",  // Server 3
}

// Start each server with broadcast config
for i, addr := range listeners {
    // Build list of OTHER servers (exclude self!)
    otherNodes := []string{}
    for j, otherAddr := range listeners {
        if i != j {  // ← Critical: don't include self
            otherNodes = append(otherNodes, otherAddr)
        }
    }
    
    // Start server with broadcast capability
    srv, realAddr := startServerWithBroadcast(addr, otherNodes)
    
    // Server 0 broadcasts to [Server1, Server2, Server3]
    // Server 1 broadcasts to [Server0, Server2, Server3]
    // etc.
}
```

### 2. Server Initialization (server.go)

```go
func startServerWithBroadcast(address string, otherNodes []string) (*gorums.Server, string) {
    // Listen on address
    lis, err := net.Listen("tcp", address)
    
    // Create storage implementation
    storage := newStorageServer()
    
    // Build interceptor chain
    interceptorChain := []gorums.Interceptor{
        interceptors.LoggingSimpleInterceptor,
        interceptors.NoFooAllowedInterceptor[*pb.WriteRequest],
        interceptors.MetadataInterceptor,
    }
    
    // If other nodes provided, add broadcast interceptor
    if len(otherNodes) > 0 {
        // ┌──────────────────────────────────────────────────┐
        // │ SERVER BECOMES CLIENT TO OTHER SERVERS           │
        // └──────────────────────────────────────────────────┘
        
        // Create client manager
        clientMgr := pb.NewManager(
            gorums.WithDialOptions(
                grpc.WithTransportCredentials(insecure.NewCredentials()),
            ),
        )
        
        // Create configuration pointing to other servers
        clientCfg, err := pb.NewConfiguration(
            clientMgr, 
            gorums.WithNodeList(otherNodes),
        )
        
        // Create broadcast interceptor with type parameters
        broadcastInterceptor := interceptors.NewBroadcastInterceptor[
            *pb.WriteRequest,
            *pb.WriteResponse,
        ](clientCfg, "proto.Storage.WriteRPC")
        
        // Add to chain
        interceptorChain = append(interceptorChain, broadcastInterceptor)
    }
    
    // Create Gorums server with interceptors
    srv := gorums.NewServer(gorums.WithInterceptors(interceptorChain...))
    
    // Register storage service
    pb.RegisterStorageServer(srv, storage)
    
    // Start serving
    go srv.Serve(lis)
    
    return srv, lis.Addr().String()
}
```

### 3. Key Setup Points

**Each server has dual roles:**
```
┌─────────────────────────────────────────┐
│           SERVER 0                      │
│                                         │
│  Role 1: SERVER                         │
│  - Listens on :50000                    │
│  - Handles client requests              │
│  - Processes writes locally             │
│                                         │
│  Role 2: CLIENT (to other servers)      │
│  - Has clientMgr                        │
│  - Has clientCfg pointing to [1,2,3]    │
│  - Can make RPC calls to other servers  │
└─────────────────────────────────────────┘
```

**Why exclude self from broadcast config?**
```go
// If Server 0 included itself:
otherNodes = [Server0, Server1, Server2, Server3]  // ❌ WRONG

// Then when broadcasting:
for _, node := range cfg.Nodes() {
    RPCCall(node, msg)  
    // Would call itself! Unnecessary self-RPC
    // Already processed locally via next()
}

// Correct approach:
otherNodes = [Server1, Server2, Server3]  // ✓ Exclude self
// Only broadcast to OTHER servers
```

---

## Complete Example Flow

### Scenario: Write Key-Value Pair

**Setup:**
- 4 servers running (Server 0, 1, 2, 3)
- Client connected to all servers
- Broadcast mode enabled

**Client Command:**
```bash
> rpc 0 write mykey myvalue
```

### Full Timeline

```
════════════════════════════════════════════════════════════════
T = 0ms: CLIENT SENDS REQUEST
════════════════════════════════════════════════════════════════
Client: Write("mykey", "myvalue") → Server 0

════════════════════════════════════════════════════════════════
T = 1ms: SERVER 0 RECEIVES REQUEST
════════════════════════════════════════════════════════════════
Server 0:
  ┌─ Interceptor Chain
  ├─ LoggingInterceptor: "WriteRPC(mykey, myvalue)"
  ├─ NoFooInterceptor: key != "foo" ✓
  ├─ MetadataInterceptor: adds metadata
  └─ BroadcastInterceptor:
      ├─ Method check: ✓ WriteRPC
      ├─ Hash: "e3b0c442"
      ├─ Cache check: NOT FOUND
      ├─ Add to cache: cache["e3b0c442"] = {}
      └─ Continue processing...

════════════════════════════════════════════════════════════════
T = 2ms: SERVER 0 PROCESSES LOCALLY
════════════════════════════════════════════════════════════════
Server 0:
  └─ Handler: storageServer.WriteRPC()
      ├─ storage["mykey"] = "myvalue" ✓
      └─ return WriteResponse{OK: true}

════════════════════════════════════════════════════════════════
T = 3ms: CLIENT RECEIVES RESPONSE
════════════════════════════════════════════════════════════════
Server 0 → Client: WriteResponse{OK: true}
Client displays: "Write OK"
Duration: 3ms ✓ (Fast! No waiting for broadcasts)

════════════════════════════════════════════════════════════════
T = 3ms: ASYNC BROADCAST STARTS (Background)
════════════════════════════════════════════════════════════════
Server 0 (background goroutine):
  ├─ Clone message
  ├─ Spawn 3 goroutines (one per target server)
  │   ├─ Goroutine A: RPCCall → Server 1
  │   ├─ Goroutine B: RPCCall → Server 2
  │   └─ Goroutine C: RPCCall → Server 3
  └─ (doesn't block client response)

════════════════════════════════════════════════════════════════
T = 5ms: SERVER 1 RECEIVES BROADCAST
════════════════════════════════════════════════════════════════
Server 1:
  ├─ Receives WriteRPC from Server 0
  └─ BroadcastInterceptor:
      ├─ Hash: "e3b0c442"
      ├─ Cache check: NOT FOUND (first time)
      ├─ Add to cache: cache["e3b0c442"] = {}
      ├─ Process locally: storage["mykey"] = "myvalue" ✓
      └─ Spawn broadcast to [0,2,3]

════════════════════════════════════════════════════════════════
T = 5ms: SERVER 2 RECEIVES BROADCAST
════════════════════════════════════════════════════════════════
Server 2:
  ├─ Receives WriteRPC from Server 0
  └─ BroadcastInterceptor:
      ├─ Hash: "e3b0c442"
      ├─ Cache check: NOT FOUND
      ├─ Add to cache
      ├─ Process locally: storage["mykey"] = "myvalue" ✓
      └─ Spawn broadcast to [0,1,3]

════════════════════════════════════════════════════════════════
T = 5ms: SERVER 3 RECEIVES BROADCAST
════════════════════════════════════════════════════════════════
Server 3:
  ├─ Receives WriteRPC from Server 0
  └─ BroadcastInterceptor:
      ├─ Hash: "e3b0c442"
      ├─ Cache check: NOT FOUND
      ├─ Add to cache
      ├─ Process locally: storage["mykey"] = "myvalue" ✓
      └─ Spawn broadcast to [0,1,2]

════════════════════════════════════════════════════════════════
T = 7ms: SERVER 0 RECEIVES BROADCAST FROM SERVER 1
════════════════════════════════════════════════════════════════
Server 0:
  ├─ Receives WriteRPC from Server 1
  └─ BroadcastInterceptor:
      ├─ Hash: "e3b0c442"
      ├─ Cache check: FOUND! ✓ (already broadcasted at T=1ms)
      ├─ Decision: SKIP BROADCAST
      └─ Process locally only (idempotent write)
      
🛡️ LOOP PREVENTED!

════════════════════════════════════════════════════════════════
T = 7-10ms: ALL SECONDARY BROADCASTS ARRIVE
════════════════════════════════════════════════════════════════
All servers receive broadcasts from each other:
- Server 0 gets broadcasts from [1,2,3] → all detected as duplicates
- Server 1 gets broadcasts from [0,2,3] → all detected as duplicates
- Server 2 gets broadcasts from [0,1,3] → all detected as duplicates
- Server 3 gets broadcasts from [0,1,2] → all detected as duplicates

All broadcasts processed locally only (no re-broadcast)
✓ System stabilizes

════════════════════════════════════════════════════════════════
T = 10ms: REPLICATION COMPLETE
════════════════════════════════════════════════════════════════
Final state:
  Server 0: storage["mykey"] = "myvalue" ✓
  Server 1: storage["mykey"] = "myvalue" ✓
  Server 2: storage["mykey"] = "myvalue" ✓
  Server 3: storage["mykey"] = "myvalue" ✓

All servers have the same data!
Total time: 10ms
Client wait time: 3ms (much faster!)
```

### Verification

```bash
# Read from different servers
> rpc 1 read mykey
mykey = myvalue  ✓

> rpc 2 read mykey
mykey = myvalue  ✓

> rpc 3 read mykey
mykey = myvalue  ✓

# All servers have the value!
```

---

## Trade-offs and Limitations

### ✅ What It Provides

1. **Automatic replication** - No client code changes needed
2. **Fast client responses** - Async broadcasting doesn't block
3. **Transparent** - Service handlers unchanged
4. **Composable** - Works with other interceptors
5. **Type-safe** - Generics prevent type errors
6. **Loop-safe** - Content hashing prevents infinite loops

### ❌ Limitations

1. **No consistency guarantees**
   - Last-write-wins based on timestamps
   - No transactions or atomic updates
   - No consensus protocol (Paxos/Raft)

2. **No ordering guarantees**
   - Concurrent writes may arrive in different orders
   - No causal ordering
   - No total ordering

3. **Fire-and-forget**
   - No acknowledgements
   - No retries on failure
   - No quorum checking

4. **Eventual consistency only**
   - Brief inconsistency windows possible
   - No read-your-writes guarantee across nodes

5. **Memory overhead**
   - Hash cache grows with unique messages
   - Cache cleanup is basic (LRU would be better)

### 💡 When to Use

**Good for:**
- Distributed caching with invalidation
- Event broadcasting/notification
- Audit logging across cluster
- Metrics collection
- Non-critical replication

**Not suitable for:**
- Bank account balances (needs strong consistency)
- Inventory management (needs transactions)
- Leader election (needs consensus)
- Critical data requiring ACID properties

### 🔧 Potential Improvements

1. **Add quorum calls** instead of individual RPCs
2. **Implement retry logic** for failed broadcasts
3. **Use vector clocks** for causal ordering
4. **Add acknowledgement tracking**
5. **Implement configurable consistency levels**
6. **Use LRU cache** with TTL for better memory management
7. **Add metrics/monitoring** for broadcast success rates

---

## Summary

The Broadcast Interceptor demonstrates Gorums' power:

- **Sophisticated distributed system features** implemented purely in middleware
- **No protocol changes** required
- **No generated code modifications** needed
- **Clean separation of concerns** between business logic and replication

This makes Gorums exceptionally flexible for building fault-tolerant distributed systems with minimal code changes!

**Key Takeaway:** By leveraging interceptors, you can add complex distributed system behaviors (like replication) without touching your core service logic. The interceptor pattern provides clean, composable, and maintainable distributed system implementations.
