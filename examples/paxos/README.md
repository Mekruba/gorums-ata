# Paxos Implementation with Gorums

This is a Paxos consensus protocol implementation using Gorums broadcast/quorum calls.

## Overview

This implementation demonstrates the classic Paxos consensus protocol with three phases:

1. **Phase 1 (Prepare/Promise)**: Proposer asks acceptors to promise not to accept lower-numbered proposals
2. **Phase 2 (Accept/Accepted)**: Proposer asks acceptors to accept a value
3. **Phase 3 (Learn)**: Proposer notifies all nodes of the chosen value

## Architecture

- **Acceptors**: Servers that vote on proposals (nodes 1, 2, 3)
- **Proposer**: Client that initiates consensus on a value
- **Learners**: All nodes learn the chosen value

Each server acts as both an acceptor and a learner.

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

### Propose a Value

In another terminal:

```bash
./paxos -propose -value "Hello Paxos!"
```

## Example Output

### Proposer Output

```
=== Proposer 100: Proposing value 'Hello Paxos!' ===

--- Phase 1: PREPARE(100001) ---
Proposer 100: ✓ PROMISE from acceptor 1 (accepted_num=0, accepted_val='')
Proposer 100: ✓ PROMISE from acceptor 2 (accepted_num=0, accepted_val='')
Proposer 100: ✓ PROMISE from acceptor 3 (accepted_num=0, accepted_val='')
Proposer 100: Received 3 promises (quorum: 2)

--- Phase 2: ACCEPT(100001, 'Hello Paxos!') ---
Proposer 100: ✓ ACCEPTED from acceptor 1
Proposer 100: ✓ ACCEPTED from acceptor 2
Proposer 100: ✓ ACCEPTED from acceptor 3
Proposer 100: Received 3 acceptances (quorum: 2)

--- Phase 3: LEARN (broadcasting chosen value) ---
Proposer 100: ✓ Node 1 learned the value
Proposer 100: ✓ Node 2 learned the value
Proposer 100: ✓ Node 3 learned the value

🎉 Proposer 100: SUCCESS! Value 'Hello Paxos!' was chosen (proposal 100001)
   3 nodes learned the value
```

### Acceptor Output

```
Node 1: Received PREPARE(100001) from proposer 100
Node 1: PROMISE(100001) - accepted_num=0, accepted_val=''
Node 1: Received ACCEPT(100001, 'Hello Paxos!') from proposer 100
Node 1: ACCEPTED(100001, 'Hello Paxos!')

🎉 Node 1: LEARNED value 'Hello Paxos!' (proposal 100001 from proposer 100)
```

## Testing Concurrent Proposals

You can test what happens when multiple proposers try to propose different values:

```bash
# Terminal 4
./paxos -propose -id 101 -value "First value"

# Terminal 5 (run immediately)
./paxos -propose -id 102 -value "Second value"
```

Paxos guarantees that only one value will be chosen, even with concurrent proposals!

## Key Features

- **Safety**: Only one value can be chosen
- **Quorum-based**: Requires majority (2 out of 3) for progress
- **Fault-tolerant**: Can tolerate minority failures
- **Deterministic**: Same value learned by all nodes

## Protocol Details

### Proposal Numbers

Each proposer uses a unique range of proposal numbers:
- Proposer 100 uses: 100001, 100002, 100003, ...
- Proposer 101 uses: 101001, 101002, 101003, ...

This prevents proposal number conflicts.

### Quorum Size

With 3 nodes, quorum = ⌊3/2⌋ + 1 = 2 nodes

### State Management

Each acceptor maintains:
- `promisedProposalNum`: Highest proposal number promised
- `acceptedProposalNum`: Highest proposal number accepted
- `acceptedValue`: Value of highest accepted proposal

## Comparison with Broadcast Example

Unlike the broadcast example where messages are simply relayed:
- Paxos achieves **consensus** - all nodes agree on a single value
- Paxos handles **conflicts** - multiple concurrent proposals are resolved
- Paxos provides **safety guarantees** - no conflicting values can be chosen
