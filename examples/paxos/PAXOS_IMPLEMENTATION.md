# Paxos Implementation with Gorums: Complete Technical Guide

This document explains the Paxos consensus protocol implementation using Gorums, focusing on how broadcasting, quorum calls, ServerCtx, and the Server-Client-Node architecture work together.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Core Components](#core-components)
3. [The Gorums Model: Configuration, Manager, and Nodes](#the-gorums-model)
4. [Paxos Protocol Flow](#paxos-protocol-flow)
5. [ServerCtx: The Server-Side Context](#serverctx-the-server-side-context)
6. [Broadcasting and Quorum Calls](#broadcasting-and-quorum-calls)
7. [Complete Request Flow](#complete-request-flow)
8. [Code Walkthrough](#code-walkthrough)

---

## Architecture Overview

Our Paxos implementation has three main roles:

```
┌─────────────────────────────────────────────────────────────┐
│                        Paxos System                          │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  Proposer (Client)          Acceptors (Servers)             │
│  ┌──────────────┐          ┌─────────┐ ┌─────────┐          │
│  │ Proposer     │          │ Node 1  │ │ Node 2  │          │
│  │              │──────────▶│Acceptor │ │Acceptor │          │
│  │ - Initiates  │          │ Learner │ │ Learner │          │
│  │   consensus  │◀─────────┤         │ │         │          │
│  │ - Broadcasts │          └─────────┘ └─────────┘          │
│  │   to all     │                        ▲                   │
│  │   acceptors  │          ┌─────────┐   │                  │
│  └──────────────┘          │ Node 3  │───┘                  │
│                             │Acceptor │                       │
│                             │ Learner │                       │
│                             └─────────┘                       │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

### Roles

1. **Proposer (Client Role)**: Initiates consensus, broadcasts proposals to all acceptors
2. **Acceptor (Server Role)**: Votes on proposals, maintains promises and accepted values
3. **Learner (Server Role)**: Learns the final chosen value

In our implementation, **each server acts as both an Acceptor and a Learner**.

---

## Core Components

### 1. Manager

The `Manager` manages connections to remote nodes. It's responsible for:
- Establishing gRPC connections
- Managing connection lifecycle
- Providing nodes for configurations

```go
// Client-side (Proposer)
mgr := pb.NewManager(
    gorums.WithDialOptions(
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    ),
)
```

### 2. Configuration

A `Configuration` represents a **set of nodes** that participate in operations. It:
- Groups nodes together
- Provides context for quorum calls
- Determines quorum size (majority)

```go
// Create configuration with all nodes
cfg, err := pb.NewConfiguration(mgr, gorums.WithNodeList(addrs))

// Configuration provides:
// - cfg.Size()      → number of nodes
// - cfg.Context()   → context for quorum calls
// - iteration over nodes
```

### 3. Node

A `Node` represents a **single remote server**. Each node has:
- ID (identifier)
- Address (network location)
- Connection state

```go
// Iterate over nodes in configuration
for _, node := range config {
    nodeID := node.ID()
    // Send individual requests...
}
```

### 4. ServerCtx

`ServerCtx` is the **server-side context** for handling requests. It provides:
- Standard context operations (timeouts, cancellation)
- Access to the server's configuration via `ctx.Config()`
- Request ordering control via `ctx.Release()`
- Response sending via `ctx.SendMessage()`

---

## The Gorums Model: Configuration, Manager, and Nodes

### Client Side (Proposer)

```go
// 1. Create Manager (manages connections)
mgr := pb.NewManager(opts...)

// 2. Create Configuration (group of acceptor nodes)
cfg, err := pb.NewConfiguration(mgr,
    gorums.WithNodeList([]string{
        "localhost:9091",  // Node 1
        "localhost:9092",  // Node 2
        "localhost:9093",  // Node 3
    }))

// 3. Configuration provides quorum call capabilities
cfgCtx := cfg.Context(context.Background())

// 4. Broadcast to ALL nodes via quorum call
responses := pb.Prepare(cfgCtx, prepareRequest)

// 5. Collect responses from nodes
for resp := range responses.Seq() {
    // resp.NodeID identifies which node responded
    // resp.Value contains the response message
    // resp.Err contains any error
}
```

**Key Point**: The configuration **automatically broadcasts** to ALL nodes in the group when you call a quorum function like `pb.Prepare()`.

### Server Side (Acceptor/Learner)

```go
// 1. Create Manager (for making proposals to peers)
mgr := pb.NewManager(opts...)

// 2. Create Configuration (includes self and all peers)
nodeMap := map[uint32]nodeAddr{
    1: {addr: "localhost:9091"},
    2: {addr: "localhost:9092"},
    3: {addr: "localhost:9093"},
}
config, err := gorums.NewConfiguration(mgr, gorums.WithNodes(nodeMap))

// 3. Create Gorums Server (listens for incoming requests)
srv := gorums.NewServer(gorums.WithConfiguration(&config))

// 4. Register your service implementation
pb.RegisterPaxosServer(srv, paxosServerImpl)

// 5. Start listening
srv.Serve(listener)
```

**Key Point**: Each server maintains a configuration that includes **all nodes** (including itself), allowing it to act as a proposer when needed.

---

## Paxos Protocol Flow

### Phase 1: Prepare/Promise

```
Proposer                        Acceptor 1    Acceptor 2    Acceptor 3
   ├─────PREPARE(n)────────────────►├───────────►├───────────►├
   │                                 │            │            │
   │                             [Check: n > promised?]       │
   │                                 │            │            │
   │◄────PROMISE(n, acc_n, acc_v)───┤◄───────────┤◄───────────┤
   │
   ├─ Collect quorum (2/3) promises
   │
   └─ Find highest accepted value (if any)
```

**Code Flow:**

```go
// Proposer (client.go)
prepareReq := pb.PrepareRequest_builder{
    ProposalNum: currentProposal,
    ProposerId:  p.id,
}.Build()

// Broadcast to ALL acceptors via Configuration
cfgCtx := p.cfg.Context(ctx)
promises := pb.Prepare(cfgCtx, prepareReq)  // ← BROADCASTS to all nodes

// Collect responses
for promise := range promises.Seq() {
    // Each acceptor responds...
}
```

```go
// Acceptor (server.go)
func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *pb.PrepareRequest) (*pb.PromiseResponse, error) {
    // ctx is ServerCtx - provides access to configuration

    proposalNum := req.GetProposalNum()

    // Check if we can promise
    if proposalNum > p.promisedProposalNum {
        p.promisedProposalNum = proposalNum

        // Return promise with any previously accepted value
        return pb.PromiseResponse_builder{
            AcceptorId:          p.id,
            Promised:            true,
            AcceptedProposalNum: p.acceptedProposalNum,
            AcceptedValue:       p.acceptedValue,
        }.Build(), nil
    }

    // Reject if already promised higher
    return pb.PromiseResponse_builder{
        AcceptorId: p.id,
        Promised:   false,
    }.Build(), nil
}
```

### Phase 2: Accept/Accepted

```
Proposer                        Acceptor 1    Acceptor 2    Acceptor 3
   ├─────ACCEPT(n, value)──────────►├───────────►├───────────►├
   │                                 │            │            │
   │                         [Check: n >= promised?]          │
   │                                 │            │            │
   │◄────ACCEPTED(n)────────────────┤◄───────────┤◄───────────┤
   │
   ├─ Collect quorum (2/3) acceptances
   │
   └─ Value is now CHOSEN!
```

**Code Flow:**

```go
// Proposer
acceptReq := pb.AcceptRequest_builder{
    ProposalNum: currentProposal,
    Value:       valueToPropose,  // Original or adopted value
    ProposerId:  p.id,
}.Build()

// Broadcast to ALL acceptors
cfgCtx2 := p.cfg.Context(ctx2)
accepteds := pb.Accept(cfgCtx2, acceptReq)  // ← BROADCASTS to all nodes

// Collect acceptances
for accepted := range accepteds.Seq() {
    // Each acceptor responds...
}
```

```go
// Acceptor
func (p *PaxosServer) Accept(ctx gorums.ServerCtx, req *pb.AcceptRequest) (*pb.AcceptedResponse, error) {
    proposalNum := req.GetProposalNum()
    value := req.GetValue()

    // Accept if proposal number is >= promised
    if proposalNum >= p.promisedProposalNum {
        p.promisedProposalNum = proposalNum
        p.acceptedProposalNum = proposalNum
        p.acceptedValue = value  // Store the accepted value

        return pb.AcceptedResponse_builder{
            AcceptorId:  p.id,
            Accepted:    true,
            ProposalNum: proposalNum,
        }.Build(), nil
    }

    // Reject if promised higher
    return pb.AcceptedResponse_builder{
        AcceptorId:  p.id,
        Accepted:    false,
        ProposalNum: p.promisedProposalNum,
    }.Build(), nil
}
```

### Phase 3: Learn

```
Proposer                        Learner 1     Learner 2     Learner 3
   ├─────LEARN(n, value)───────────►├───────────►├───────────►├
   │                                 │            │            │
   │                           [Store value]                  │
   │                                 │            │            │
   │◄────LEARNED(true)──────────────┤◄───────────┤◄───────────┤
   │
   └─ All nodes have learned the value
```

**Code Flow:**

```go
// Proposer
learnReq := pb.LearnRequest_builder{
    ProposalNum: currentProposal,
    Value:       valueToPropose,
    ProposerId:  p.id,
}.Build()

// Broadcast to ALL learners
cfgCtx3 := p.cfg.Context(ctx3)
learned := pb.Learn(cfgCtx3, learnReq)  // ← BROADCASTS to all nodes
```

```go
// Learner
func (p *PaxosServer) Learn(ctx gorums.ServerCtx, req *pb.LearnRequest) (*pb.LearnResponse, error) {
    value := req.GetValue()

    // Store the learned value
    p.learnedValue = value
    p.learnedFromNum = req.GetProposalNum()

    return pb.LearnResponse_builder{
        LearnerId: p.id,
        Learned:   true,
    }.Build(), nil
}
```

---

## ServerCtx: The Server-Side Context

`ServerCtx` is a special context type passed to **every server handler**. It extends `context.Context` with Gorums-specific functionality.

### What ServerCtx Provides

```go
type ServerCtx interface {
    context.Context  // Standard context operations

    // Access to server's configuration (all nodes)
    Config() *Configuration

    // Release mutex for next request (ordering)
    Release()

    // Send message back to client
    SendMessage(msg *Message) error
}
```

### ServerCtx in Our Paxos Implementation

```go
func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *pb.PrepareRequest) (*pb.PromiseResponse, error) {
    // 1. Standard context operations work
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    default:
    }

    // 2. Access configuration (not used in this handler, but available)
    config := ctx.Config()
    // Could iterate over nodes: for _, node := range *config { ... }

    // 3. Process request
    // ... Paxos logic ...

    // 4. Return response (automatically sent back via Gorums)
    return response, nil

    // Note: ctx.Release() is called automatically when handler returns
}
```

### When ServerCtx.Config() Is Useful

In the broadcast example, servers use `ctx.Config()` to forward messages to peers:

```go
func (b *BroadcastServer) Broadcast(ctx gorums.ServerCtx, msg *BroadcastMsg) {
    // Deliver locally
    b.deliverMessage(msg)

    // Forward to all other nodes using ServerCtx.Config()
    config := ctx.Config()
    if config != nil {
        for _, node := range *config {
            if node.ID() == b.id {
                continue  // Skip self
            }
            // Forward to this peer
            go func(n *gorums.Node) {
                nodeCtx := n.Context(context.Background())
                _ = pb.Broadcast(nodeCtx, msg)
            }(node)
        }
    }
}
```

**In our Paxos implementation**, acceptors don't forward to peers, so we don't use `ctx.Config()` in the handlers. But it's available if needed for extensions like:
- Leader-based optimizations
- Forwarding proposals to a designated leader
- Peer-to-peer learning broadcasts

---

## Broadcasting and Quorum Calls

### How Gorums Broadcasting Works

When you call a quorum function, Gorums:

1. **Sends the request to ALL nodes** in the configuration simultaneously
2. **Collects responses** as they arrive
3. **Returns when quorum is met** (or timeout)
4. **Provides iterator** to process all responses

```go
// This ONE line broadcasts to ALL nodes!
responses := pb.Prepare(cfgCtx, prepareRequest)

// Under the hood:
// - Node 1 receives request → processes → responds
// - Node 2 receives request → processes → responds
// - Node 3 receives request → processes → responds
// All happen concurrently!

// Process responses
for resp := range responses.Seq() {
    fmt.Printf("Response from Node %d: %v\n", resp.NodeID, resp.Value)
}
```

### Quorum Call vs Broadcast vs Multicast vs Unicast

Gorums provides different RPC patterns defined in the `.proto` file:

```protobuf
service Paxos {
  rpc Prepare(PrepareRequest) returns (PromiseResponse) {
    option (gorums.quorumcall) = true;  // ← Broadcasts, requires quorum
  }
}
```

| Pattern | Behavior | Returns When | Use Case |
|---------|----------|--------------|----------|
| **quorumcall** | Sends to ALL, collects responses | Quorum reached (majority) | Voting, consensus |
| **multicast** | Sends to ALL, collects all responses | ALL respond or timeout | Data collection |
| **broadcast** | Sends to ALL, no response expected | Immediately | One-way notifications |
| **unicast** | Sends to ONE node | Single response | Point-to-point |

### Quorum Functions in Paxos

All three Paxos phases use **quorumcall** pattern:

```go
// Phase 1: Broadcast PREPARE to all acceptors
promises := pb.Prepare(cfgCtx, prepareReq)
// Returns when majority (2 out of 3) respond

// Phase 2: Broadcast ACCEPT to all acceptors
accepteds := pb.Accept(cfgCtx, acceptReq)
// Returns when majority (2 out of 3) respond

// Phase 3: Broadcast LEARN to all learners
learned := pb.Learn(cfgCtx, learnReq)
// Returns when majority (2 out of 3) respond
```

### Response Processing

Gorums provides an iterator pattern for processing responses:

```go
responses := pb.Prepare(cfgCtx, prepareReq)

// Process each response as it arrives
for resp := range responses.Seq() {
    // resp contains:
    // - NodeID: which node sent this response
    // - Value: the response message
    // - Err: any error from this node

    if resp.Err != nil {
        log.Printf("Error from node %d: %v", resp.NodeID, resp.Err)
        continue
    }

    promise := resp.Value
    if promise.GetPromised() {
        // This acceptor promised!
    }
}
```

---

## Complete Request Flow

Let's trace a complete Paxos proposal through the system:

### Step-by-Step: Proposer 101 Proposes "Hello"

```
1. Proposer Creates Configuration
   ┌─────────────┐
   │ Proposer    │
   │ ID: 101     │
   │             │
   │ cfg ───────┼───► Configuration {
   │             │       Nodes: [1, 2, 3]
   └─────────────┘       Manager: [connections to all nodes]
                       }

2. Phase 1: PREPARE Broadcast
   ┌─────────────┐
   │ Proposer    │
   │             │                    ┌──────────┐
   │ Prepare()   ├────────────────────►│ Node 1  │
   │             │                    └──────────┘
   │ cfgCtx ─────┼─────► Gorums      ┌──────────┐
   │             │       broadcasts ──►│ Node 2  │
   │             │                    └──────────┘
   │             │                    ┌──────────┐
   │             ├────────────────────►│ Node 3  │
   └─────────────┘                    └──────────┘
         ▲
         │
         └─── promises.Seq() iterator

3. Acceptors Process PREPARE
   ┌──────────────────────────────────────┐
   │ Node 1 (Acceptor)                    │
   ├──────────────────────────────────────┤
   │ func Prepare(                        │
   │     ctx gorums.ServerCtx,  ◄─────────┼─── ServerCtx provided
   │     req *PrepareRequest    ◄─────────┼─── Request message
   │ ) (*PromiseResponse, error) {        │
   │                                      │
   │   // Access configuration if needed  │
   │   config := ctx.Config()             │
   │                                      │
   │   // Process request                 │
   │   if req.ProposalNum > promised {    │
   │       promised = req.ProposalNum     │
   │       return Promise(accepted_value) │
   │   }                                  │
   │   return Reject()                    │
   │ }                                    │
   └──────────────────────────────────────┘
                   │
                   ▼
         Response sent back via Gorums

4. Proposer Collects Promises
   ┌─────────────┐
   │ Proposer    │       ┌─── Promise from Node 1
   │             │       │    (NodeID=1, Promised=true, ...)
   │ for resp    │◄──────┤
   │   in        │       ├─── Promise from Node 2
   │   promises  │◄──────┤    (NodeID=2, Promised=true, ...)
   │   .Seq()    │       │
   │             │◄──────┴─── Promise from Node 3
   │ Check       │            (NodeID=3, Promised=true, ...)
   │ quorum      │
   │ (2/3)       │
   │             │
   │ ✓ Continue  │
   │   to Phase 2│
   └─────────────┘

5. Phase 2: ACCEPT Broadcast
   [Same process as Phase 1, with AcceptRequest]

6. Phase 3: LEARN Broadcast
   [Same process, with LearnRequest]

7. Final State
   ┌──────────┐  ┌──────────┐  ┌──────────┐
   │ Node 1   │  │ Node 2   │  │ Node 3   │
   │ Learned: │  │ Learned: │  │ Learned: │
   │ "Hello"  │  │ "Hello"  │  │ "Hello"  │
   └──────────┘  └──────────┘  └──────────┘
```

---

## Code Walkthrough

### Server Initialization

```go
func runServer(id uint32) {
    // 1. Create Manager for outgoing connections
    mgr := pb.NewManager(
        gorums.WithDialOptions(
            grpc.WithTransportCredentials(insecure.NewCredentials()),
        ),
    )

    // 2. Build node map (all nodes including self)
    nodeMap := make(map[uint32]nodeAddr)
    for _, node := range nodes {
        nodeMap[node.id] = nodeAddr{addr: node.addr}
    }

    // 3. Create configuration (connects to all nodes)
    config, err := gorums.NewConfiguration(mgr, gorums.WithNodes(nodeMap))
    // config now contains Node objects for all 3 nodes

    // 4. Create server implementation
    paxosSrv := NewPaxosServer(id, config)
    // Server can access config to communicate with peers

    // 5. Create Gorums server (for incoming requests)
    srv := gorums.NewServer(gorums.WithConfiguration(&config))

    // 6. Register handler
    pb.RegisterPaxosServer(srv, paxosSrv)
    // This registers: Prepare, Accept, Learn handlers

    // 7. Listen and serve
    lis, _ := net.Listen("tcp", addr)
    srv.Serve(lis)
}
```

### Client (Proposer) Workflow

```go
func (p *Proposer) Propose(value string) error {
    // --- PHASE 1: PREPARE ---

    // Build request
    prepareReq := pb.PrepareRequest_builder{
        ProposalNum: p.proposalNum,
        ProposerId:  p.id,
    }.Build()

    // Create context from configuration
    cfgCtx := p.cfg.Context(context.Background())

    // BROADCAST: This sends to ALL nodes in p.cfg
    promises := pb.Prepare(cfgCtx, prepareReq)

    // Collect responses
    var promiseCount int
    var highestAcceptedNum uint32
    var highestAcceptedValue string

    for promise := range promises.Seq() {
        // promise.NodeID tells us which node responded
        // promise.Value contains the PromiseResponse
        // promise.Err contains any error

        if promise.Err != nil {
            continue  // Handle error
        }

        resp := promise.Value
        if resp.GetPromised() {
            promiseCount++

            // Track highest previously accepted value
            if resp.GetAcceptedProposalNum() > highestAcceptedNum {
                highestAcceptedNum = resp.GetAcceptedProposalNum()
                highestAcceptedValue = resp.GetAcceptedValue()
            }
        }
    }

    // Check if we have quorum
    quorumSize := int(p.cfg.Size())/2 + 1  // Majority
    if promiseCount < quorumSize {
        return fmt.Errorf("no quorum")
    }

    // --- PHASE 2: ACCEPT ---

    // Must use previously accepted value if any
    valueToPropose := value
    if highestAcceptedValue != "" {
        valueToPropose = highestAcceptedValue
    }

    acceptReq := pb.AcceptRequest_builder{
        ProposalNum: p.proposalNum,
        Value:       valueToPropose,
        ProposerId:  p.id,
    }.Build()

    // BROADCAST again
    cfgCtx2 := p.cfg.Context(context.Background())
    accepteds := pb.Accept(cfgCtx2, acceptReq)

    // Collect acceptances (similar to promises)
    var acceptCount int
    for accepted := range accepteds.Seq() {
        if accepted.Err != nil {
            continue
        }
        if accepted.Value.GetAccepted() {
            acceptCount++
        }
    }

    if acceptCount < quorumSize {
        return fmt.Errorf("no quorum")
    }

    // --- PHASE 3: LEARN ---

    learnReq := pb.LearnRequest_builder{
        ProposalNum: p.proposalNum,
        Value:       valueToPropose,
        ProposerId:  p.id,
    }.Build()

    // BROADCAST to learners
    cfgCtx3 := p.cfg.Context(context.Background())
    learned := pb.Learn(cfgCtx3, learnReq)

    // Wait for responses (optional, for confirmation)
    for learn := range learned.Seq() {
        // Process learning confirmations
    }

    return nil  // Success!
}
```

### Server Handler with ServerCtx

```go
func (p *PaxosServer) Prepare(
    ctx gorums.ServerCtx,        // ← Special Gorums context
    req *pb.PrepareRequest,       // ← Request message
) (*pb.PromiseResponse, error) { // ← Response message

    // --- Standard context operations ---
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    default:
    }

    // --- Access configuration (all nodes) ---
    config := ctx.Config()
    // Could use to forward or communicate with peers
    // In basic Paxos, acceptors don't need this

    // --- Process Paxos logic ---
    p.mu.Lock()
    defer p.mu.Unlock()

    proposalNum := req.GetProposalNum()

    if proposalNum > p.promisedProposalNum {
        // Promise this proposal
        p.promisedProposalNum = proposalNum

        return pb.PromiseResponse_builder{
            AcceptorId:          p.id,
            Promised:            true,
            AcceptedProposalNum: p.acceptedProposalNum,
            AcceptedValue:       p.acceptedValue,
        }.Build(), nil
    }

    // Reject
    return pb.PromiseResponse_builder{
        AcceptorId: p.id,
        Promised:   false,
    }.Build(), nil

    // Note: Response is automatically sent back via Gorums
    // Note: ctx.Release() is automatically called when handler returns
}
```

---

## Key Concepts Summary

### 1. Broadcasting in Paxos

**Every phase broadcasts to all nodes:**
- Proposer → ALL acceptors (Phase 1: Prepare)
- Proposer → ALL acceptors (Phase 2: Accept)
- Proposer → ALL learners (Phase 3: Learn)

**Code pattern:**
```go
cfgCtx := cfg.Context(ctx)
responses := pb.SomeRPC(cfgCtx, request)  // Broadcasts to ALL nodes
```

### 2. Configuration = Group of Nodes

```go
cfg, _ := pb.NewConfiguration(mgr, gorums.WithNodeList(addrs))
// cfg contains ALL nodes that will receive broadcasts
// cfg.Size() = 3 nodes
// Quorum = cfg.Size()/2 + 1 = 2 nodes
```

### 3. ServerCtx in Handlers

```go
func Handler(ctx gorums.ServerCtx, req *Request) (*Response, error) {
    // ctx.Context()     → standard context operations
    // ctx.Config()      → access to all nodes (for forwarding, etc.)
    // ctx.Release()     → release ordering mutex (automatic)
    // ctx.SendMessage() → send additional messages (advanced)

    // Return response - automatically sent to client
    return response, nil
}
```

### 4. Response Collection

```go
responses := pb.Prepare(cfgCtx, req)

for resp := range responses.Seq() {
    nodeID := resp.NodeID    // Which node sent this
    value := resp.Value      // The response message
    err := resp.Err          // Any error from this node

    // Process response...
}
```

### 5. Server-Client-Node Relationship

```
Client Side (Proposer):
  Manager ─────► Configuration ─────► [Node1, Node2, Node3]
                      │
                      └─ Broadcasts to all nodes

Server Side (Acceptor):
  Manager ─────► Configuration ─────► [Node1, Node2, Node3]
       │              │
       │              └─ Available in ServerCtx
       │
       └─ Server ─────► Handlers receive ServerCtx
```

---

## Comparison: Broadcast Example vs Paxos

| Aspect | Broadcast Example | Paxos Implementation |
|--------|------------------|---------------------|
| **Pattern** | Multicast with forwarding | Quorum call (voting) |
| **Purpose** | Message dissemination | Consensus on value |
| **Phases** | Single broadcast | Three phases |
| **Responses** | Acknowledgments | Votes (promises, accepts) |
| **ServerCtx Use** | Used to forward to peers via `ctx.Config()` | Available but not used (no peer forwarding) |
| **Quorum** | Not required | Majority required for progress |
| **Safety** | Best-effort delivery | Guaranteed consensus |

**Key difference in ServerCtx usage:**

```go
// Broadcast: Uses ctx.Config() to forward to peers
func (b *BroadcastServer) Broadcast(ctx gorums.ServerCtx, msg *BroadcastMsg) {
    config := ctx.Config()  // ✓ Used for forwarding
    for _, node := range *config {
        // Forward to each peer
    }
}

// Paxos: Doesn't need ctx.Config() (no peer forwarding)
func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *PrepareRequest) {
    // ctx.Config() available but not needed
    // Just process request and respond
}
```

---

## Conclusion

This Paxos implementation demonstrates:

1. **Broadcasting via Configuration**: All phases broadcast to all nodes automatically
2. **Quorum Collection**: Gorums collects responses and determines when quorum is reached
3. **ServerCtx**: Provides server-side context with access to configuration and nodes
4. **Node Management**: Manager and Configuration handle connections and node grouping
5. **Clean Separation**: Client (Proposer) and Server (Acceptor/Learner) roles are clearly defined

The key insight is that **Gorums handles the broadcasting and response collection**, allowing you to focus on the **protocol logic** (voting, consensus rules) rather than the **communication mechanics** (connections, RPC calls, response aggregation).
