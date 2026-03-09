# Paxos Test Suite

This test suite provides comprehensive testing for the Multi-Paxos implementation using Gorums' symmetric communication features.

## Test Coverage

### Unit Tests

- **TestPaxosBasicConsensus**: Tests basic single-value consensus with 3 nodes
- **TestPaxosMultipleValues**: Tests sequential proposal of multiple values (Multi-Paxos)
- **TestPaxosConcurrentProposers**: Tests concurrent proposals from multiple proposers
- **TestPaxosLeaderOptimization**: Verifies Phase 1 skipping optimization for consecutive proposals
- **TestPaxosSymmetricBroadcast**: Tests server-to-server Learn broadcasts using symmetric channels
- **TestPaxosQuorumSizes**: Parameterized test for 3, 5, and 7 node clusters
- **TestPaxosAcceptorPromises**: Tests that acceptors correctly track and reject lower proposals
- **TestPaxosAcceptorAccepts**: Tests acceptor accept logic and state tracking
- **TestPaxosMultiInstance**: Tests handling of multiple Paxos instances
- **TestPaxosProposerHigherProposal**: Tests higher proposal seeing previously accepted values

### Benchmarks

- **BenchmarkPaxosPropose**: Measures throughput of sequential proposals
- **BenchmarkPaxosParallelPropose**: Measures throughput with parallel competing proposers

## Running Tests

### Run all tests
```bash
go test -v -timeout=60s
```

### Run specific test
```bash
go test -v -run TestPaxosBasicConsensus
```

### Run with race detector
```bash
go test -race -timeout=60s
```

### Run benchmarks
```bash
go test -bench=. -benchtime=100ms -run=^$
```

## Test Architecture

The test suite uses a `testSetup()` helper function that:
1. Creates N Paxos nodes using `gorums.System`
2. Registers PaxosServer implementations on each node
3. Establishes bidirectional connections between all nodes
4. Creates a proposer configuration for external clients
5. Returns cleanup function for proper teardown

Example:
```go
func TestPaxosBasicConsensus(t *testing.T) {
    _, _, config, cleanup := testSetup(t, 3)
    defer cleanup()

    proposer := NewProposer(100, config)
    err := proposer.Propose("test-value-1")
    // assertions...
}
```

## Key Features Tested

### Symmetric Communication
- Tests verify bidirectional channel reuse
- Server-initiated Learn broadcasts to peers
- Dynamic peer discovery via InboundManager

### Multi-Paxos Optimization
- Phase 1 skipping for consecutive instances
- Leader stability (same proposer keeps leadership)
- Conflict resolution between competing proposers

### Correctness Properties
- Safety: All nodes agree on the same value for each instance
- Liveness: Quorum of nodes can make progress
- Consistency: Higher proposals see previously accepted values

## Expected Behavior

### Concurrent Proposers
When multiple proposers compete, some proposals may fail (by design). The test verifies:
- At least one proposal succeeds
- All nodes agree on decided values
- No safety violations occur

### Learn Broadcasts
After a value is learned from a proposer:
- Each node broadcasts Learn to all peers
- Responses may indicate "already knew" (this is correct)
- All nodes eventually learn the same value

## Test Output

Tests produce detailed output showing:
- Paxos phase progression (Prepare, Accept, Learn)
- Promise and acceptance from each node
- Leader election and Phase 1 skipping
- Symmetric broadcasts with "already knew" responses

Suppress output for cleaner results:
```bash
go test -timeout=60s > /dev/null && echo "All tests passed!"
```

## Performance

Typical performance on modern hardware:
- Basic consensus: ~400ms (includes setup/teardown)
- 100 sequential proposals: ~870 μs per proposal
- Parallel proposals: varies with contention

## Future Enhancements

Potential test additions:
- Fault tolerance (node failures)
- Network partitions
- Message delays and reordering
- Leader failover scenarios
- Performance regression tests
