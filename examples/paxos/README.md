# Multi-Paxos Implementation with Gorums

This is a Multi-Paxos consensus protocol implementation using Gorums quorum calls and server-to-server broadcasting.

## Overview

This implementation demonstrates Multi-Paxos, an optimized variant of Basic Paxos that efficiently handles multiple consensus instances:

1. **Phase 1 (Prepare/Promise)**: Proposer establishes leadership by getting promises from acceptors
2. **Phase 2 (Accept/Accepted)**: Proposer (now leader) proposes values for consensus
3. **Phase 3 (Learn)**: Nodes learn and broadcast chosen values using ServerCtx

**Key Multi-Paxos Optimization**: Once a proposer becomes a leader (completes Phase 1), it can skip Phase 1 for subsequent proposals, going directly to Phase 2. This dramatically improves performance for multiple consensus decisions.

## Architecture

- **Acceptors**: Servers that vote on proposals (nodes 1, 2, 3)
- **Proposer**: Client that initiates consensus on values for multiple instances
- **Learners**: All nodes learn chosen values and broadcast to peers
- **Instances**: Each consensus decision is assigned a unique instance number

Each server acts as both an acceptor and a learner, maintaining per-instance state for concurrent consensus decisions.

## Key Multi-Paxos Features

- **Instance-based consensus**: Multiple values can be decided using separate instances
- **Leader optimization**: Phase 1 is skipped for subsequent proposals after leadership is established
- **Per-instance state**: Servers track promises and acceptances separately for each instance
- **Server-to-server broadcasting**: Nodes use `ServerCtx.Config()` to broadcast learned values to peers
- **Safety**: Only one value can be chosen per instance
- **Quorum-based**: Requires majority (2 out of 3) for progress
- **Fault-tolerant**: Can tolerate minority failures

## Building

From the examples directory:

```bash
cd paxos
make -C .. paxos/paxos
```

Or manually:

```bash
# Generate protobuf code
protoc -I="../..:." \
    --go_out=paths=source_relative:. \
    --gorums_out=paths=source_relative:. \
    --go_opt=default_api_level=API_OPAQUE \
    proto/paxos.proto

# Build binary
go build -o paxos
```

## Running

### Start 3 Acceptor Nodes

In separate terminals:

```bash
# Terminal 1
./paxos -id 1

# Terminal 2
./paxos -id 2

# Terminal 3
./paxos -id 3
```

### Propose Values

In another terminal:

```bash
# Propose 3 values (default) - demonstrates Multi-Paxos leader optimization
./paxos -propose

# Propose specific values (comma-separated)
./paxos -propose -values "value1,value2,value3"

# Propose with custom proposer ID
./paxos -propose -id 200 -values "alpha,beta,gamma"
```

## Example Output

### Proposer Output (Multi-Paxos)

```
=== Proposer 100: Proposing value 'value-1' for instance 1 ===

--- Phase 1: PREPARE(inst=1, num=100001) ---
Proposer 100: ✓ PROMISE from acceptor 1 (accepted_num=0, accepted_val='')
Proposer 100: ✓ PROMISE from acceptor 2 (accepted_num=0, accepted_val='')
Proposer 100: ✓ PROMISE from acceptor 3 (accepted_num=0, accepted_val='')
Proposer 100: Received 3 promises (quorum: 2)
Proposer 100: 🎖️  Became LEADER for subsequent proposals

--- Phase 2: ACCEPT(inst=1, num=100001, val='value-1') ---
Proposer 100: ✓ ACCEPTED from acceptor 1
Proposer 100: ✓ ACCEPTED from acceptor 2
Proposer 100: ✓ ACCEPTED from acceptor 3
Proposer 100: Received 3 acceptances (quorum: 2)

--- Phase 3: LEARN (broadcasting chosen value) ---
Proposer 100: ✓ Node 1 learned the value
Proposer 100: ✓ Node 2 learned the value
Proposer 100: ✓ Node 3 learned the value

🎉 Proposer 100: SUCCESS! Value 'value-1' was chosen (inst=1, proposal=100001)
   3 nodes learned the value

=== Proposer 100: Proposing value 'value-2' for instance 2 ===

--- Phase 1: SKIPPED (already leader) ---

--- Phase 2: ACCEPT(inst=2, num=100002, val='value-2') ---
Proposer 100: ✓ ACCEPTED from acceptor 1
Proposer 100: ✓ ACCEPTED from acceptor 2
Proposer 100: ✓ ACCEPTED from acceptor 3
Proposer 100: Received 3 acceptances (quorum: 2)

--- Phase 3: LEARN (broadcasting chosen value) ---
Proposer 100: ✓ Node 1 learned the value
Proposer 100: ✓ Node 2 learned the value
Proposer 100: ✓ Node 3 learned the value

🎉 Proposer 100: SUCCESS! Value 'value-2' was chosen (inst=2, proposal=100002)
   3 nodes learned the value

✅ All 3 values proposed successfully!
```

Notice how **Phase 1 is skipped** for the second and third values - this is the Multi-Paxos optimization in action!

### Acceptor Output

```
Node 1: Received PREPARE(inst=1, num=100001) from proposer 100
Node 1: PROMISE(inst=1, num=100001) - accepted_num=0, accepted_val=''
Node 1: Received ACCEPT(inst=1, num=100001, val='value-1') from proposer 100
Node 1: ACCEPTED(inst=1, num=100001, val='value-1')

🎉 Node 1: LEARNED value 'value-1' (inst=1, proposal=100001 from proposer 100)

Node 1: Received ACCEPT(inst=2, num=100002, val='value-2') from proposer 100
Node 1: ACCEPTED(inst=2, num=100002, val='value-2')

🎉 Node 1: LEARNED value 'value-2' (inst=2, proposal=100002 from proposer 100)
```

Notice that for instance 2, the node only receives ACCEPT (no PREPARE) due to leader optimization.

## Testing Concurrent Proposals

You can test what happens when multiple proposers try to propose different values:

```bash
# Terminal 4
./paxos -propose -id 101 -values "first,second"

# Terminal 5 (run simultaneously)
./paxos -propose -id 102 -values "alpha,beta"
```

Paxos guarantees that only one value will be chosen per instance, even with concurrent proposals!

**What happens with concurrent proposers:**
- Each proposer runs Phase 1 for instances they want to propose
- If a proposer discovers another value was already accepted, it must re-propose that value (safety)
- If Phase 2 is rejected, the proposer loses leadership and must retry Phase 1
- Eventually, consensus is reached with the same value agreed by all nodes per instance

## Protocol Details

### Instance Numbers

Multi-Paxos uses instance numbers to order multiple consensus decisions:
- Instance 1: First value to be decided
- Instance 2: Second value to be decided
- Instance 3: Third value to be decided
- ...and so on

Each instance maintains independent state.

### Leader Optimization

The key optimization in Multi-Paxos:
1. **First proposal**: Complete Phase 1 (PREPARE) to become leader
2. **Subsequent consecutive proposals**: Skip Phase 1, go directly to Phase 2 (ACCEPT)

**Safety guarantee**: Phase 1 is only skipped when it's provably safe:
- If proposing for an instance already prepared by this proposer (re-proposal)
- If proposing consecutive instances (e.g., 1→2→3) where the previous instance was **fresh** (not discovered from another proposer)

**Why this matters**: If a proposer discovers that instance N was already decided by someone else, it CANNOT skip Phase 1 for instance N+1, because that instance might also have been decided. The proposer must run Phase 1 to discover any previously accepted values and maintain safety.

This approach reduces message complexity from 2 round-trips to 1 round-trip per value after leadership is established, while maintaining strict Paxos safety guarantees even with concurrent proposers.

### Proposal Numbers

Each proposer uses a unique range of proposal numbers:
- Proposer 100 uses: 100001, 100002, 100003, ...
- Proposer 101 uses: 101001, 101002, 101003, ...

This prevents proposal number conflicts between proposers.

### Quorum Size

With 3 nodes, quorum = ⌊3/2⌋ + 1 = 2 nodes

### Per-Instance State Management

Each acceptor maintains state for each instance separately:
- `promisedProposalNum`: Highest proposal number promised for this instance
- `acceptedProposalNum`: Highest proposal number accepted for this instance
- `acceptedValue`: Value of highest accepted proposal for this instance
- `learnedValue`: The chosen value for this instance
- `learnedFromNum`: Proposal number of learned value

### Server-to-Server Broadcasting

When a node learns a value, it uses `ServerCtx.Config()` to broadcast to other nodes.
This demonstrates server-to-server communication in Gorums:

```go
if cfg := ctx.Config(); cfg != nil && cfg.Size() > 1 {
    go p.broadcastLearn(cfg, req)
}
```

## Comparison with Basic Paxos and Broadcast

**Basic Paxos vs Multi-Paxos:**
- Basic Paxos: Runs Phase 1 (PREPARE) for every single value
- Multi-Paxos: Runs Phase 1 once, then skips it for subsequent values (leader optimization)
- Performance: Multi-Paxos is much more efficient for deciding multiple values

**Paxos vs Broadcast Example:**
- Paxos achieves **consensus** - all nodes agree on a single value per instance
- Paxos handles **conflicts** - multiple concurrent proposals are resolved deterministically
- Paxos provides **safety guarantees** - no conflicting values can be chosen for the same instance
- Broadcast simply relays messages without consensus guarantees

## Implementation Highlights

1. **Instance-based state**: Server maintains `map[uint32]*instanceState` for concurrent instances
2. **Fresh instance tracking**: Proposer tracks `freshInstances` to distinguish between instances it created vs. discovered
3. **Safe Phase 1 skipping**: Only skip Phase 1 when previous instance was fresh (created by this proposer)
4. **ServerCtx broadcasting**: Servers broadcast learned values to peers using `ctx.Config()`
5. **Protobuf editions**: Uses Protocol Buffers edition 2023 with builder pattern
6. **Gorums quorum calls**: All three RPCs (Prepare, Accept, Learn) use `quorumcall` option

## Safety Example

Consider this scenario:
```bash
# Proposer 100 decides instances 1, 2, 3 with values "apple", "banana", "cherry"
./paxos -propose -values "apple,banana,cherry"

# Proposer 200 tries to propose instances 1, 2 with values "first", "second"
./paxos -propose -id 200 -values "first,second"
```

**What happens:**
- Proposer 200, instance 1: Runs Phase 1, discovers "apple", re-proposes "apple" ✓
- Proposer 200, instance 2: **Runs Phase 1** (cannot skip!), discovers "banana", re-proposes "banana" ✓

The key: Instance 1 was NOT fresh for proposer 200 (it was discovered), so proposer 200 cannot skip Phase 1 for instance 2. This preserves safety - "banana" is not overwritten with "second".
