# Multi-Paxos Implementation with Gorums: Complete Technical Guide

This document explains the Multi-Paxos consensus protocol implementation using Gorums, focusing on how broadcasting, quorum calls, ServerCtx, instances, leader optimization, and the Server-Client-Node architecture work together.

**Multi-Paxos** extends Basic Paxos to efficiently decide multiple values in sequence, with significant performance improvements through leader optimization and instance-based consensus.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Multi-Paxos Concepts](#multi-paxos-concepts)
3. [Core Components](#core-components)
4. [The Gorums Model: Configuration, Manager, and Nodes](#the-gorums-model)
5. [Multi-Paxos Protocol Flow](#multi-paxos-protocol-flow)
6. [Leader Optimization and Fresh Instance Tracking](#leader-optimization-and-fresh-instance-tracking)
7. [ServerCtx: The Server-Side Context](#serverctx-the-server-side-context)
8. [Broadcasting and Quorum Calls](#broadcasting-and-quorum-calls)
9. [Complete Request Flow](#complete-request-flow)
10. [Code Walkthrough](#code-walkthrough)

---

## Architecture Overview

Our Multi-Paxos implementation has three main roles and handles multiple consensus instances:

```
┌─────────────────────────────────────────────────────────────────┐
│                     Multi-Paxos System                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Proposer (Client)            Acceptors (Servers)               │
│  ┌──────────────────┐        ┌─────────┐ ┌─────────┐           │
│  │ Proposer         │        │ Node 1  │ │ Node 2  │           │
│  │                  │────────▶│Acceptor │ │Acceptor │           │
│  │ - Proposes for   │        │ Learner │ │ Learner │           │
│  │   multiple       │◀───────┤         │ │         │           │
│  │   instances      │        │ Per-Inst│ │ Per-Inst│           │
│  │ - Tracks leader  │        │  State  │ │  State  │           │
│  │   status         │        └─────────┘ └─────────┘           │
│  │ - Skips Phase 1  │                      ▲                    │
│  │   when safe      │        ┌─────────┐   │                   │
│  └──────────────────┘        │ Node 3  │───┘                   │
│                               │Acceptor │                        │
│  Instances:                   │ Learner │                        │
│  1: "apple"                   │ Per-Inst│                        │
│  2: "banana"                  │  State  │                        │
│  3: "cherry"                  └─────────┘                        │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Roles

1. **Proposer (Client Role)**: Initiates consensus for multiple instances, broadcasts proposals to all acceptors, tracks leader status and fresh instances
2. **Acceptor (Server Role)**: Votes on proposals for each instance independently, maintains per-instance promises and accepted values
3. **Learner (Server Role)**: Learns the chosen values for each instance, can broadcast to peers using ServerCtx

In our implementation, **each server acts as both an Acceptor and a Learner**, maintaining independent state for each consensus instance.

---

## Multi-Paxos Concepts

### What is Multi-Paxos?

Multi-Paxos extends Basic Paxos to efficiently handle **multiple consensus decisions**:

- **Basic Paxos**: Decides ONE value (requires Phase 1 + Phase 2)
- **Multi-Paxos**: Decides MANY values in sequence (Phase 1 once, then Phase 2 only)

### Key Concepts

#### 1. Instances

Each consensus decision has a unique **instance number**:

```
Instance 1: Decide "apple"
Instance 2: Decide "banana"
Instance 3: Decide "cherry"
...
```

Each instance maintains **independent state**:
- Separate promises
- Separate accepted values
- Separate learned values

#### 2. Leader Optimization

Once a proposer becomes a "leader" (completes Phase 1), it can **skip Phase 1 for subsequent instances**:

```
Instance 1: Phase 1 + Phase 2 (become leader)
Instance 2: Phase 2 only (skip Phase 1!) ← 50% faster
Instance 3: Phase 2 only (skip Phase 1!) ← 50% faster
```

**Performance gain**: Reduces message complexity from 2 round-trips to 1 round-trip per value.

#### 3. Fresh vs Discovered Instances

For safety, we distinguish:

- **Fresh Instance**: Created by this proposer from scratch (safe to skip Phase 1)
- **Discovered Instance**: Already decided by another proposer (MUST run Phase 1)

**Safety rule**: Only skip Phase 1 if the previous instance was fresh and consecutive.

#### 4. Per-Instance State

Each acceptor maintains a map of instance states:

```go
type instanceState struct {
    promisedProposalNum uint32  // Highest promise for THIS instance
    acceptedProposalNum uint32  // Highest accept for THIS instance
    acceptedValue       string  // Value accepted for THIS instance
    learnedValue        string  // Chosen value for THIS instance
    learnedFromNum      uint32  // Proposal number of learned value
}

instances map[uint32]*instanceState  // One state per instance
```

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

## Multi-Paxos Protocol Flow

### Phase 1: Prepare/Promise (May Be Skipped!)

```
Proposer                        Acceptor 1    Acceptor 2    Acceptor 3
   ├─────PREPARE(inst=1, n)────────►├───────────►├───────────►├
   │                                 │            │            │
   │                   [Check: n > promised FOR INSTANCE 1?] │
   │                                 │            │            │
   │◄────PROMISE(inst=1, acc_n, v)──┤◄───────────┤◄───────────┤
   │
   ├─ Collect quorum (2/3) promises
   ├─ Find highest accepted value (if any)
   ├─ Mark instance 1 as PREPARED
   └─ Mark instance 1 as FRESH (if no previous value found)
```

**Multi-Paxos Optimization**: For instance 2, if instance 1 was fresh, **skip Phase 1**!

**Code Flow with Instance Numbers:**

```go
// Proposer (client.go)
instance := p.nextInstance  // Instance number (1, 2, 3, ...)
p.nextInstance++

// Check if we can skip Phase 1 (leader optimization)
skipPhase1 := p.preparedInstances[instance] ||
    (p.highestSeenInstance > 0 &&
     instance == p.highestSeenInstance+1 &&
     p.preparedInstances[p.highestSeenInstance] &&
     p.freshInstances[p.highestSeenInstance])  // ← Safety check!

if !skipPhase1 {
    prepareReq := pb.PrepareRequest_builder{
        Instance:    instance,      // ← Instance number
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
    instance := req.GetInstance()        // ← Get instance number
    proposalNum := req.GetProposalNum()

    state := p.getInstance(instance)     // ← Get per-instance state

    p.mu.Lock()
    defer p.mu.Unlock()

    // Check if we can promise FOR THIS INSTANCE
    if proposalNum > state.promisedProposalNum {
        state.promisedProposalNum = proposalNum

        // Return promise with previously accepted value FOR THIS INSTANCE
        return pb.PromiseResponse_builder{
            AcceptorId:          p.id,
            Instance:            instance,  // ← Instance number
            Promised:            true,
            AcceptedProposalNum: state.acceptedProposalNum,  // ← Per-instance
            AcceptedValue:       state.acceptedValue,        // ← Per-instance
        }.Build(), nil
    }

    // Reject if already promised higher FOR THIS INSTANCE
    return pb.PromiseResponse_builder{
        AcceptorId: p.id,
        Instance:   instance,
        Promised:   false,
    }.Build(), nil
}

// getInstance creates or retrieves instance state
func (p *PaxosServer) getInstance(instance uint32) *instanceState {
    if state, exists := p.instances[instance]; exists {
        return state
    }
    state := &instanceState{}
    p.instances[instance] = state
    return state
}
```

### Phase 2: Accept/Accepted (Always Required)

```
Proposer                        Acceptor 1    Acceptor 2    Acceptor 3
   ├─────ACCEPT(inst=1, n, val)────►├───────────►├───────────►├
   │                                 │            │            │
   │              [Check: n >= promised FOR INSTANCE 1?]      │
   │                                 │            │            │
   │◄────ACCEPTED(inst=1, n)────────┤◄───────────┤◄───────────┤
   │
   ├─ Collect quorum (2/3) acceptances
   │
   └─ Value is now CHOSEN for instance 1!
```

**Multi-Paxos**: Phase 2 is ALWAYS needed even when Phase 1 is skipped.

**Code Flow:**

```go
// Proposer
acceptReq := pb.AcceptRequest_builder{
    Instance:    instance,            // ← Instance number
    ProposalNum: currentProposal,
    Value:       valueToPropose,      // Original or adopted value
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
    instance := req.GetInstance()     // ← Get instance number
    proposalNum := req.GetProposalNum()
    value := req.GetValue()

    state := p.getInstance(instance)  // ← Get per-instance state

    p.mu.Lock()
    defer p.mu.Unlock()

    // Accept if proposal number >= promised FOR THIS INSTANCE
    if proposalNum >= state.promisedProposalNum {
        state.promisedProposalNum = proposalNum
        state.acceptedProposalNum = proposalNum
        state.acceptedValue = value  // Store accepted value FOR THIS INSTANCE

        return pb.AcceptedResponse_builder{
            AcceptorId:  p.id,
            Instance:    instance,     // ← Instance number
            Accepted:    true,
            ProposalNum: proposalNum,
        }.Build(), nil
    }

    // Reject if promised higher FOR THIS INSTANCE
    return pb.AcceptedResponse_builder{
        AcceptorId:  p.id,
        Instance:    instance,
        Accepted:    false,
        ProposalNum: state.promisedProposalNum,
    }.Build(), nil
}
```

### Phase 3: Learn (With Server-to-Server Broadcasting)

```
Proposer                        Learner 1     Learner 2     Learner 3
   ├─────LEARN(inst=1, n, val)─────►├───────────►├───────────►├
   │                                 │            │            │
   │                  [Store value FOR INSTANCE 1]           │
   │                                 │            │            │
   │                                 ├─Broadcast──►├  ◄────────┤
   │                                 │  to peers   │           │
   │                                 │            │            │
   │◄────LEARNED(inst=1)────────────┤◄───────────┤◄───────────┤
   │
   └─ All nodes have learned the value for instance 1
```

**Multi-Paxos Enhancement**: Learners can broadcast to peers using `ServerCtx.Config()`!

**Code Flow:**

```go
// Proposer
learnReq := pb.LearnRequest_builder{
    Instance:    instance,          // ← Instance number
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
    instance := req.GetInstance()     // ← Get instance number
    value := req.GetValue()
    proposalNum := req.GetProposalNum()

    state := p.getInstance(instance)  // ← Get per-instance state

    p.mu.Lock()
    // Store learned value FOR THIS INSTANCE
    if state.learnedValue == "" || proposalNum > state.learnedFromNum {
        state.learnedValue = value
        state.learnedFromNum = proposalNum
        p.mu.Unlock()

        // Broadcast to peers using ServerCtx.Config()!
        if cfg := ctx.Config(); cfg != nil && cfg.Size() > 1 {
            go p.broadcastLearn(cfg, req)
        }

        return pb.LearnResponse_builder{
            LearnerId: p.id,
            Instance:  instance,  // ← Instance number
            Learned:   true,
        }.Build(), nil
    }
    p.mu.Unlock()

    return pb.LearnResponse_builder{
        LearnerId: p.id,
        Instance:  instance,
        Learned:   false,
    }.Build(), nil
}

// broadcastLearn demonstrates server-to-server broadcasting
func (p *PaxosServer) broadcastLearn(cfg *gorums.Configuration, req *pb.LearnRequest) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    cfgCtx := (*cfg).Context(ctx)
    learned := pb.Learn(cfgCtx, req)  // Broadcast to peers!

    for learn := range learned.Seq() {
        if learn.Err == nil && learn.Value.GetLearned() {
            fmt.Printf("Node %d: Broadcast learned to node %d\n", p.id, learn.NodeID)
        }
    }
}
```

---

## Leader Optimization and Fresh Instance Tracking

Multi-Paxos achieves its performance improvement through **leader optimization**: once a proposer completes Phase 1, it can skip Phase 1 for subsequent instances. However, this optimization must preserve Paxos safety guarantees.

### The Safety Challenge

Consider this scenario:

```
Timeline:
1. Proposer A (ID=100): Runs Phase 1+2 for instance 1, chooses "apple"
2. Proposer B (ID=200): Runs Phase 1 for instance 2
   - Learns nothing (instance 2 is empty)
   - Skips Phase 1 for instance 3 (unsafe!)
   - Tries to choose "banana" for instance 3
3. BUT: Proposer A may have already chosen another value for instance 3!
```

**Problem**: Proposer B thinks it's the leader, but hasn't checked if instance 3 is actually fresh!

### Fresh vs Discovered Instances

To solve this, we track two categories:

#### Fresh Instance
- **Definition**: Instance created by THIS proposer from scratch
- **Characteristics**:
  - No previous value discovered in Phase 1
  - Proposer knows the instance was empty before it proposed
- **Safety**: Safe to skip Phase 1 for next consecutive instance

#### Discovered Instance
- **Definition**: Instance that already had a value when THIS proposer ran Phase 1
- **Characteristics**:
  - Phase 1 returned a previously accepted value
  - Another proposer was active for this instance
- **Safety**: MUST run Phase 1 for next instance (cannot assume leadership continues)

### Implementation

```go
type Proposer struct {
    // ... other fields ...

    preparedInstances map[uint32]bool  // Instances where Phase 1 completed
    freshInstances    map[uint32]bool  // Instances created fresh (no discovered value)
    nextInstance      uint32           // Next instance number to use
    highestSeenInstance uint32         // Highest instance we've seen any activity on
}
```

### Skip Phase 1 Logic

The proposer can skip Phase 1 for instance N if **ALL** of these conditions hold:

```go
skipPhase1 := p.preparedInstances[instance] ||    // Already prepared this instance
    (p.highestSeenInstance > 0 &&                 // Have seen at least one instance
     instance == p.highestSeenInstance+1 &&       // Next consecutive instance
     p.preparedInstances[p.highestSeenInstance] && // Prepared previous instance
     p.freshInstances[p.highestSeenInstance])     // Previous was FRESH
```

**Key insight**: Only skip Phase 1 if the **previous instance was fresh**!

### Example Flow: Two Proposers

#### Scenario 1: Safe Leader Continuation

```
Proposer A (ID=100):
  Instance 1: Phase 1 → no value found → FRESH → Phase 2 → "apple" chosen
  Instance 2: Skip Phase 1 (prev was fresh) → Phase 2 → "banana" chosen ✓
  Instance 3: Skip Phase 1 (prev was fresh) → Phase 2 → "cherry" chosen ✓
```

**Safe**: Proposer A created all instances fresh, can safely skip Phase 1.

#### Scenario 2: Discovery Forces Phase 1

```
Proposer A (ID=100):
  Instance 1: Phase 1 → "apple" (fresh) → Phase 2 → "apple" chosen
  Instance 2: Phase 1 → "banana" (fresh) → Phase 2 → "banana" chosen

Proposer B (ID=200) starts:
  Instance 3: Phase 1 → discovers "cherry" from A → NOT FRESH
              Phase 2 → must propose "cherry" (not "first")
  Instance 4: MUST do Phase 1 (prev was discovered, not fresh!) ✓
              Phase 1 → may find A's value or nothing
```

**Safe**: Proposer B discovered a value in instance 3, so it cannot assume leadership for instance 4.

#### Scenario 3: Bug Without Fresh Tracking (Fixed!)

```
Without fresh tracking (UNSAFE):

Proposer A (ID=100):
  Instance 1: Phase 1 → "apple" (fresh) → Phase 2 → "apple" chosen
  Instance 2: Phase 1 → "banana" (fresh) → Phase 2 → "banana" chosen

Proposer B (ID=200):
  Instance 3: Phase 1 → discovers "cherry" → Phase 2 → "cherry" chosen
  Instance 4: Skip Phase 1 (BUG! should check freshness) ✗
              Phase 2 → "first" chosen
              BUT A might have already chosen "delta" for instance 4! ✗✗✗

With fresh tracking (SAFE):

Proposer B (ID=200):
  Instance 3: Phase 1 → discovers "cherry" → mark NOT FRESH
  Instance 4: MUST do Phase 1 (prev not fresh) ✓
              Phase 1 → discovers "delta" from A
              Phase 2 → "delta" chosen (not "first") ✓
```

### Code: Marking Fresh Instances

```go
// In Propose() after Phase 1 completes:

foundPreviousValue := false
for promise := range promises.Seq() {
    if promise.Err == nil && promise.Value.GetPromised() {
        promiseCount++
        if promise.Value.GetAcceptedValue() != "" {
            // Found a previously accepted value!
            foundPreviousValue = true
            // ... adopt this value ...
        }
    }
}

if promiseCount >= p.quorumSize {
    p.preparedInstances[instance] = true

    // Mark as fresh ONLY if no previous value was discovered
    if !foundPreviousValue {
        p.freshInstances[instance] = true  // Safe to skip Phase 1 for next
    } else {
        // Do NOT mark as fresh - must run Phase 1 for next instance
        // (implicitly: freshInstances[instance] remains false/unset)
    }

    p.highestSeenInstance = max(p.highestSeenInstance, instance)
}
```

### Safety Guarantee

**Theorem**: If proposer P skips Phase 1 for instance N, then no other proposer has chosen a conflicting value for instance N.

**Proof sketch**:
1. P can only skip Phase 1 if instance N-1 was fresh for P
2. Fresh means P ran Phase 1 for N-1 and found no previous value
3. P had the highest proposal number for N-1 (quorum promised)
4. No other proposer could have completed Phase 2 for N-1 without P knowing
5. Since P created N-1 and immediately proceeds to N, no other proposer has started N
6. Therefore, P can safely skip Phase 1 for N

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

**In our Multi-Paxos implementation**, we **DO** use `ctx.Config()` in the `Learn` handler to broadcast learned values to peer learners:

```go
func (p *PaxosServer) Learn(ctx gorums.ServerCtx, req *pb.LearnRequest) (*pb.LearnResponse, error) {
    // ... store learned value ...

    // Broadcast to peers using ServerCtx.Config()!
    if cfg := ctx.Config(); cfg != nil && cfg.Size() > 1 {
        go p.broadcastLearn(cfg, req)
    }

    return response, nil
}

func (p *PaxosServer) broadcastLearn(cfg *gorums.Configuration, req *pb.LearnRequest) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    cfgCtx := (*cfg).Context(ctx)
    learned := pb.Learn(cfgCtx, req)  // Broadcast to all peers!

    for learn := range learned.Seq() {
        // Process peer responses...
    }
}
```

This demonstrates **server-to-server communication** using ServerCtx, ensuring all learners eventually learn all values across all instances.

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

Let's trace a complete Multi-Paxos proposal through the system:

### Step-by-Step: Proposer 101 Proposes "Hello" for Instance 1

```
1. Proposer Creates Configuration
   ┌─────────────┐
   │ Proposer    │
   │ ID: 101     │
   │ Instance: 1 │
   │             │
   │ cfg ───────┼───► Configuration {
   │             │       Nodes: [1, 2, 3]
   └─────────────┘       Manager: [connections to all nodes]
                       }

2. Phase 1: PREPARE Broadcast (Instance 1)
   ┌─────────────┐
   │ Proposer    │
   │             │                    ┌──────────┐
   │ Prepare()   ├─── inst=1, n ─────►│ Node 1  │
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

3. Acceptors Process PREPARE (For Instance 1)
   ┌──────────────────────────────────────┐
   │ Node 1 (Acceptor)                    │
   ├──────────────────────────────────────┤
   │ func Prepare(                        │
   │     ctx gorums.ServerCtx,  ◄─────────┼─── ServerCtx provided
   │     req *PrepareRequest    ◄─────────┼─── Request (instance=1)
   │ ) (*PromiseResponse, error) {        │
   │                                      │
   │   instance := req.GetInstance()  // 1│
   │   state := p.getInstance(instance)   │
   │                                      │
   │   // Check promise FOR INSTANCE 1    │
   │   if req.ProposalNum >               │
   │      state.promisedProposalNum {     │
   │       state.promisedProposalNum = n  │
   │       return Promise(                │
   │         instance=1,                  │
   │         accepted_value_for_inst_1    │
   │       )                              │
   │   }                                  │
   │   return Reject(instance=1)          │
   │ }                                    │
   └──────────────────────────────────────┘
                   │
                   ▼
         Response sent back via Gorums

4. Proposer Collects Promises (Instance 1)
   ┌─────────────┐
   │ Proposer    │       ┌─── Promise from Node 1
   │             │       │    (instance=1, Promised=true)
   │ for resp    │◄──────┤
   │   in        │       ├─── Promise from Node 2
   │   promises  │◄──────┤    (instance=1, Promised=true)
   │   .Seq()    │       │
   │             │◄──────┴─── Promise from Node 3
   │ Check       │            (instance=1, Promised=true)
   │ quorum      │
   │ (2/3)       │
   │             │
   │ ✓ Mark      │
   │   prepared  │
   │   & fresh   │
   │ ✓ Continue  │
   │   to Phase 2│
   └─────────────┘

5. Phase 2: ACCEPT Broadcast (Instance 1)
   [Same process with AcceptRequest(inst=1, n, val)]

6. Phase 3: LEARN Broadcast (Instance 1)
   [Proposer broadcasts, Learners store AND broadcast to peers]

7. Instance 2: Leader Optimization!
   ┌─────────────┐
   │ Proposer    │
   │ Instance: 2 │
   │             │
   │ ✓ SKIP      │ ← Instance 1 was fresh and consecutive!
   │   Phase 1!  │
   │             │
   │ → Phase 2   │ ← Directly to ACCEPT
   │   ACCEPT    │
   └─────────────┘

8. Final State (Multiple Instances)
   ┌──────────┐  ┌──────────┐  ┌──────────┐
   │ Node 1   │  │ Node 2   │  │ Node 3   │
   │ Inst 1:  │  │ Inst 1:  │  │ Inst 1:  │
   │ "Hello"  │  │ "Hello"  │  │ "Hello"  │
   │          │  │          │  │          │
   │ Inst 2:  │  │ Inst 2:  │  │ Inst 2:  │
   │ "World"  │  │ "World"  │  │ "World"  │
   └──────────┘  └──────────┘  └──────────┘
```

---

## Code Walkthrough

### Server Initialization (Multi-Paxos)

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

    // 4. Create server implementation with per-instance state
    paxosSrv := NewPaxosServer(id, config)
    // Server maintains map[uint32]*instanceState for all instances

    // 5. Create Gorums server (for incoming requests)
    srv := gorums.NewServer(gorums.WithConfiguration(&config))

    // 6. Register handler
    pb.RegisterPaxosServer(srv, paxosSrv)
    // This registers: Prepare, Accept, Learn handlers
    // Each handler receives ServerCtx for server-side operations

    // 7. Listen and serve
    lis, _ := net.Listen("tcp", addr)
    srv.Serve(lis)
}
```

### Client (Proposer) Workflow (Multi-Paxos with Leader Optimization)

```go
func (p *Proposer) Propose(value string) error {
    // Get next instance number
    instance := p.nextInstance
    p.nextInstance++

    // --- LEADER OPTIMIZATION CHECK ---

    skipPhase1 := p.preparedInstances[instance] ||
        (p.highestSeenInstance > 0 &&
         instance == p.highestSeenInstance+1 &&
         p.preparedInstances[p.highestSeenInstance] &&
         p.freshInstances[p.highestSeenInstance])  // ← Safety check!

    var valueToPropose string

    if !skipPhase1 {
        // --- PHASE 1: PREPARE ---

        prepareReq := pb.PrepareRequest_builder{
            Instance:    instance,         // ← Instance number
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
        foundPreviousValue := false

        for promise := range promises.Seq() {
            if promise.Err != nil {
                continue
            }

            resp := promise.Value
            if resp.GetPromised() {
                promiseCount++

                // Track highest previously accepted value FOR THIS INSTANCE
                if resp.GetAcceptedValue() != "" {
                    foundPreviousValue = true
                    if resp.GetAcceptedProposalNum() > highestAcceptedNum {
                        highestAcceptedNum = resp.GetAcceptedProposalNum()
                        highestAcceptedValue = resp.GetAcceptedValue()
                    }
                }
            }
        }

        // Check if we have quorum
        quorumSize := int(p.cfg.Size())/2 + 1
        if promiseCount < quorumSize {
            return fmt.Errorf("no quorum")
        }

        // Mark instance as prepared
        p.preparedInstances[instance] = true

        // Mark as FRESH only if no previous value found
        if !foundPreviousValue {
            p.freshInstances[instance] = true
        }
        // Otherwise: NOT marked as fresh, next instance MUST run Phase 1

        p.highestSeenInstance = max(p.highestSeenInstance, instance)

        // Must use previously accepted value if any (Paxos safety)
        if highestAcceptedValue != "" {
            valueToPropose = highestAcceptedValue
        } else {
            valueToPropose = value
        }
    } else {
        // Skipping Phase 1 - we're the leader for this instance!
        valueToPropose = value
    }

    // --- PHASE 2: ACCEPT (Always Required) ---

    acceptReq := pb.AcceptRequest_builder{
        Instance:    instance,         // ← Instance number
        ProposalNum: p.proposalNum,
        Value:       valueToPropose,
        ProposerId:  p.id,
    }.Build()

    // BROADCAST again
    cfgCtx2 := p.cfg.Context(context.Background())
    accepteds := pb.Accept(cfgCtx2, acceptReq)

    // Collect acceptances
    var acceptCount int
    for accepted := range accepteds.Seq() {
        if accepted.Err != nil {
            continue
        }
        if accepted.Value.GetAccepted() {
            acceptCount++
        }
    }

    quorumSize := int(p.cfg.Size())/2 + 1
    if acceptCount < quorumSize {
        return fmt.Errorf("no quorum")
    }

    // --- PHASE 3: LEARN ---

    learnReq := pb.LearnRequest_builder{
        Instance:    instance,         // ← Instance number
        ProposalNum: p.proposalNum,
        Value:       valueToPropose,
        ProposerId:  p.id,
    }.Build()

    // BROADCAST to learners
    cfgCtx3 := p.cfg.Context(context.Background())
    learned := pb.Learn(cfgCtx3, learnReq)

    // Wait for responses
    for learn := range learned.Seq() {
        // Learners will also broadcast to peers using ServerCtx.Config()
    }

    fmt.Printf("Instance %d: Chosen value: %s\n", instance, valueToPropose)
    return nil  // Success!
}
```

### Server Handler with ServerCtx and Per-Instance State

```go
func (p *PaxosServer) Prepare(
    ctx gorums.ServerCtx,        // ← Special Gorums context
    req *pb.PrepareRequest,       // ← Request message
) (*pb.PromiseResponse, error) { // ← Response message

    // --- Extract instance number ---
    instance := req.GetInstance()

    // --- Get or create per-instance state ---
    state := p.getInstance(instance)

    // --- Standard context operations ---
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    default:
    }

    // --- Access configuration (all nodes) ---
    config := ctx.Config()
    // Available for server-to-server communication
    // Used in Learn handler for peer broadcasting

    // --- Process Paxos logic FOR THIS INSTANCE ---
    p.mu.Lock()
    defer p.mu.Unlock()

    proposalNum := req.GetProposalNum()

    // Check promise FOR THIS INSTANCE
    if proposalNum > state.promisedProposalNum {
        // Promise this proposal FOR THIS INSTANCE
        state.promisedProposalNum = proposalNum

        return pb.PromiseResponse_builder{
            AcceptorId:          p.id,
            Instance:            instance,  // ← Return instance number
            Promised:            true,
            AcceptedProposalNum: state.acceptedProposalNum,  // Per-instance
            AcceptedValue:       state.acceptedValue,        // Per-instance
        }.Build(), nil
    }

    // Reject FOR THIS INSTANCE
    return pb.PromiseResponse_builder{
        AcceptorId: p.id,
        Instance:   instance,
        Promised:   false,
    }.Build(), nil

    // Note: Response is automatically sent back via Gorums
    // Note: ctx.Release() is automatically called when handler returns
}

func (p *PaxosServer) getInstance(instance uint32) *instanceState {
    if state, exists := p.instances[instance]; exists {
        return state
    }
    // Create new state for this instance
    state := &instanceState{}
    p.instances[instance] = state
    return state
}
```

---

## Key Concepts Summary

### 1. Multi-Paxos vs Basic Paxos

| Aspect | Basic Paxos | Multi-Paxos |
|--------|-------------|-------------|
| **Scope** | Single value | Multiple values (instances) |
| **State** | Global state | Per-instance state map |
| **Performance** | 2 RTT per value | 2 RTT first, 1 RTT after |
| **Leader** | No optimization | Skip Phase 1 when leader |
| **Safety** | Value immutability | Per-instance + fresh tracking |

### 2. Broadcasting in Multi-Paxos

**Every phase broadcasts to all nodes WITH INSTANCE NUMBER:**
- Proposer → ALL acceptors (Phase 1: Prepare for instance N)
- Proposer → ALL acceptors (Phase 2: Accept for instance N)
- Proposer → ALL learners (Phase 3: Learn for instance N)
- Learners → ALL peers (Server-to-server broadcast using ServerCtx)

**Code pattern:**
```go
prepareReq := pb.PrepareRequest_builder{
    Instance:    instance,  // ← Instance number
    ProposalNum: proposalNum,
    ProposerId:  proposerId,
}.Build()

cfgCtx := cfg.Context(ctx)
responses := pb.Prepare(cfgCtx, prepareReq)  // Broadcasts to ALL nodes
```

### 3. Configuration = Group of Nodes

```go
cfg, _ := pb.NewConfiguration(mgr, gorums.WithNodeList(addrs))
// cfg contains ALL nodes that will receive broadcasts
// cfg.Size() = 3 nodes
// Quorum = cfg.Size()/2 + 1 = 2 nodes

// Available on both client and server side
// Client: cfg passed to Proposer
// Server: ctx.Config() in handlers
```

### 4. ServerCtx in Handlers (Multi-Paxos Enhancement)

```go
func Handler(ctx gorums.ServerCtx, req *Request) (*Response, error) {
    // Standard operations:
    // ctx.Context()     → standard context operations
    // ctx.Config()      → access to all nodes (for server-to-server comms)
    // ctx.Release()     → release ordering mutex (automatic)
    // ctx.SendMessage() → send additional messages (advanced)

    // Multi-Paxos specific:
    instance := req.GetInstance()       // Get instance number
    state := p.getInstance(instance)    // Get per-instance state

    // Server-to-server broadcasting example (Learn phase):
    if cfg := ctx.Config(); cfg != nil {
        go p.broadcastToPeers(cfg, req)
    }

    // Return response - automatically sent to client
    return response, nil
}
```

### 5. Leader Optimization Safety Rules

**Can skip Phase 1 if ALL conditions met:**
```go
1. Instance N-1 was PREPARED by this proposer
2. Instance N is CONSECUTIVE (N = N-1 + 1)
3. Instance N-1 was FRESH (no discovered value)
```

**Must run Phase 1 if ANY condition fails:**
```go
1. First instance ever
2. Non-consecutive instance (gap in sequence)
3. Previous instance discovered another proposer's value
```

### 6. Response Collection (With Instance Numbers)

```go
responses := pb.Prepare(cfgCtx, req)  // req contains instance number

for resp := range responses.Seq() {
    nodeID := resp.NodeID           // Which node sent this
    value := resp.Value             // The response message
    instance := value.GetInstance() // Instance number in response
    err := resp.Err                 // Any error from this node

    // Process response for this instance...
}
```

### 7. Server-Client-Node Relationship

```
Client Side (Proposer):
  Manager ─────► Configuration ─────► [Node1, Node2, Node3]
                      │
                      └─ Broadcasts to all nodes (with instance numbers)

Server Side (Acceptor/Learner):
  Manager ─────► Configuration ─────► [Node1, Node2, Node3]
       │              │
       │              └─ Available in ServerCtx.Config()
       │
       └─ Server ─────► Handlers receive ServerCtx
                        Handlers maintain per-instance state
                        Handlers can broadcast to peers
```

---

## Comparison: Basic Paxos vs Multi-Paxos

| Aspect | Basic Paxos | Multi-Paxos Implementation |
|--------|-------------|---------------------------|
| **Pattern** | Quorum call (voting) | Quorum call with leader optimization |
| **Purpose** | Consensus on ONE value | Consensus on MANY values |
| **Phases** | Always 3 phases | 3 phases first, then 2 phases |
| **State** | Single global state | Per-instance state map |
| **Responses** | Votes for single value | Votes per instance |
| **ServerCtx Use** | Used for peer broadcasting | Used in Learn for peer broadcasting |
| **Quorum** | Majority required | Majority required per instance |
| **Safety** | Value immutability | Per-instance immutability + fresh tracking |
| **Performance** | 2 RTT always | 2 RTT first, 1 RTT after (50% faster!) |

**Key difference in ServerCtx usage:**

```go
// Multi-Paxos: Uses ctx.Config() for peer broadcasting in Learn phase
func (p *PaxosServer) Learn(ctx gorums.ServerCtx, req *pb.LearnRequest) {
    instance := req.GetInstance()
    state := p.getInstance(instance)

    // Store learned value
    state.learnedValue = req.GetValue()

    // Broadcast to peers using ServerCtx.Config()!
    if cfg := ctx.Config(); cfg != nil && cfg.Size() > 1 {
        go p.broadcastLearn(cfg, req)
    }

    return response, nil
}

// Other handlers (Prepare, Accept) don't need ctx.Config()
func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *pb.PrepareRequest) {
    instance := req.GetInstance()
    state := p.getInstance(instance)

    // Process request for this instance and respond
    // No peer forwarding needed
}
```

---

## Conclusion

This Multi-Paxos implementation demonstrates:

1. **Instance-Based Consensus**: Multiple independent consensus decisions with per-instance state
2. **Leader Optimization**: Skip Phase 1 when safe, reducing latency by 50% for subsequent values
3. **Fresh Instance Tracking**: Safety mechanism to prevent value conflicts across proposers
4. **Broadcasting via Configuration**: All phases broadcast to all nodes with instance numbers
5. **Quorum Collection**: Gorums collects responses per instance and determines when quorum is reached
6. **ServerCtx Enhancement**: Provides server-side context with access to configuration for peer broadcasting
7. **Server-to-Server Communication**: Learners broadcast to peers using `ServerCtx.Config()`
8. **Node Management**: Manager and Configuration handle connections and node grouping
9. **Clean Separation**: Client (Proposer) and Server (Acceptor/Learner) roles are clearly defined

### Key Insights

1. **Instance Independence**: Each instance maintains separate promises, accepts, and learned values
2. **Performance Gains**: Leader optimization reduces message complexity from 4 messages (2 RTT) to 2 messages (1 RTT) per value after the first
3. **Safety Guarantee**: Fresh instance tracking ensures that skipping Phase 1 never violates Paxos safety
4. **Gorums Power**: Framework handles broadcasting, response collection, and connection management
5. **Focus on Logic**: Developers focus on **protocol logic** (voting, consensus rules, instance management) rather than **communication mechanics**

### Multi-Paxos Performance

```
Basic Paxos:
  Value 1: Phase 1 (2 msgs) + Phase 2 (2 msgs) = 4 messages
  Value 2: Phase 1 (2 msgs) + Phase 2 (2 msgs) = 4 messages
  Value 3: Phase 1 (2 msgs) + Phase 2 (2 msgs) = 4 messages
  Total: 12 messages

Multi-Paxos (with leader):
  Value 1 (Instance 1): Phase 1 (2 msgs) + Phase 2 (2 msgs) = 4 messages
  Value 2 (Instance 2): Phase 2 (2 msgs) = 2 messages  ← 50% faster!
  Value 3 (Instance 3): Phase 2 (2 msgs) = 2 messages  ← 50% faster!
  Total: 8 messages  ← 33% reduction overall!
```

**The key insight is that Gorums handles the broadcasting and response collection**, allowing you to focus on the **Multi-Paxos protocol** (instance management, leader optimization, safety rules) rather than the **communication mechanics** (connections, RPC calls, response aggregation).

---

## Practical Examples

### Example 1: Single Proposer (Leader Throughout)

```bash
# Terminal 1-3: Start 3 servers
./paxos -server -id 1 -port 8081
./paxos -server -id 2 -port 8082
./paxos -server -id 3 -port 8083

# Terminal 4: Propose multiple values
./paxos -client -id 100 -values "apple,banana,cherry"
```

**Output:**
```
Proposer 100: Instance 1 - Phase 1 + Phase 2 → "apple" chosen
Proposer 100: Instance 2 - Phase 2 only → "banana" chosen  ← Optimized!
Proposer 100: Instance 3 - Phase 2 only → "cherry" chosen  ← Optimized!

Performance: 2 instances optimized (50% faster each)
```

**Why optimized?** All instances were fresh, so Phase 1 only needed for first instance.

### Example 2: Two Proposers Competing (Discovery Required)

```bash
# Servers already running

# Terminal 1: First proposer
./paxos -client -id 100 -values "apple,banana"

# Terminal 2: Second proposer (starts after first)
./paxos -client -id 200 -values "first,second"
```

**Scenario A: Proposer 200 starts after Proposer 100 completes:**
```
Proposer 100:
  Instance 1: Phase 1 (fresh) + Phase 2 → "apple" chosen
  Instance 2: Phase 2 only (optimized) → "banana" chosen

Proposer 200:
  Instance 3: Phase 1 (first instance) + Phase 2 → "first" chosen
  Instance 4: Phase 2 only (optimized) → "second" chosen

Result: All values chosen as proposed (no conflicts)
```

**Scenario B: Proposer 200 starts while Proposer 100 is at instance 2:**
```
Proposer 100:
  Instance 1: Phase 1 (fresh) + Phase 2 → "apple" chosen
  Instance 2: Phase 2 only (optimized) → "banana" chosen (in progress)

Proposer 200:
  Instance 3: Phase 1 → discovers "cherry" from 100 → NOT FRESH
              Phase 2 → must propose "cherry" (not "first")
  Instance 4: MUST do Phase 1 (prev was discovered, not fresh)
              Phase 1 → discovers nothing or 100's value
              Phase 2 → proposes accordingly

Result: Safety preserved - Proposer 200 discovers and respects 100's values
```

### Example 3: Querying Learned Values

After running the examples above, you can query what values were learned:

```bash
# Each server maintains learned values per instance
Node 1 learned:
  Instance 1: "apple"
  Instance 2: "banana"
  Instance 3: "cherry"

Node 2 learned:
  Instance 1: "apple"
  Instance 2: "banana"
  Instance 3: "cherry"

Node 3 learned:
  Instance 1: "apple"
  Instance 2: "banana"
  Instance 3: "cherry"
```

**Consensus guarantee:** All nodes agree on the same value for each instance.

### Understanding the Code Flow

For the command `./paxos -client -id 100 -values "apple,banana,cherry"`:

1. **Instance 1 ("apple")**:
   ```
   ✓ Run Phase 1 (first instance)
   ✓ No previous value found → FRESH
   ✓ Run Phase 2 with "apple"
   ✓ "apple" chosen
   ```

2. **Instance 2 ("banana")**:
   ```
   ✓ Check: Instance 1 was fresh and consecutive → SKIP Phase 1!
   ✓ Run Phase 2 with "banana"
   ✓ "banana" chosen
   ```

3. **Instance 3 ("cherry")**:
   ```
   ✓ Check: Instance 2 was fresh and consecutive → SKIP Phase 1!
   ✓ Run Phase 2 with "cherry"
   ✓ "cherry" chosen
   ```

**Result:** 1 full Paxos run + 2 optimized runs = 33% fewer messages overall!
