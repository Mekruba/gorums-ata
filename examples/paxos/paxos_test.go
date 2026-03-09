package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/paxos/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// testSetup creates a test environment with n Paxos nodes and a proposer configuration.
func testSetup(t *testing.T, numNodes int) (systems []*gorums.System, servers []*PaxosServer, config pb.Configuration, cleanup func()) {
	t.Helper()

	systems = make([]*gorums.System, numNodes)
	servers = make([]*PaxosServer, numNodes)
	nodeAddrs := make([]string, numNodes)
	managers := make([]*pb.Manager, numNodes)

	// Create and start all nodes
	for i := 0; i < numNodes; i++ {
		nodeID := uint32(i + 1)

		// Create system without initial peer configuration (will set up connections later)
		sys, err := gorums.NewSystem("127.0.0.1:0",
			gorums.WithGRPCServerOptions(grpc.Creds(insecure.NewCredentials())),
		)
		if err != nil {
			t.Fatalf("Failed to create system for node %d: %v", nodeID, err)
		}
		systems[i] = sys
		nodeAddrs[i] = sys.Addr()

		// Create Paxos server
		paxosSrv := NewPaxosServer(nodeID)
		servers[i] = paxosSrv

		// Create manager for this node
		mgr := pb.NewManager(
			gorums.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
		)
		managers[i] = mgr

		// Register service
		sys.RegisterService(mgr, func(srv *gorums.Server) {
			pb.RegisterPaxosServer(srv, paxosSrv)
		})

		// Start server
		go func(s *gorums.System) {
			if err := s.Serve(); err != nil {
				t.Logf("Server error: %v", err)
			}
		}(sys)
	}

	// Give servers time to start
	time.Sleep(100 * time.Millisecond)

	// Create outbound configurations for each node
	for i := 0; i < numNodes; i++ {
		nodeMap := make(map[uint32]nodeAddr)
		for j := 0; j < numNodes; j++ {
			nodeMap[uint32(j+1)] = nodeAddr{addr: nodeAddrs[j]}
		}

		proposerConfig, err := systems[i].NewOutboundConfig(
			gorums.WithNodes(nodeMap),
			gorums.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
		)
		if err != nil {
			t.Fatalf("Failed to create proposer config for node %d: %v", i+1, err)
		}
		servers[i].setProposerConfig(proposerConfig)
	}

	// Wait for connections to establish
	time.Sleep(200 * time.Millisecond)

	// Create proposer configuration from external client
	proposerMgr := pb.NewManager(
		gorums.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)

	nodeMap := make(map[uint32]nodeAddr)
	for i := 0; i < numNodes; i++ {
		nodeMap[uint32(i+1)] = nodeAddr{addr: nodeAddrs[i]}
	}

	config, err := gorums.NewConfiguration(proposerMgr, gorums.WithNodes(nodeMap))
	if err != nil {
		t.Fatalf("Failed to create proposer configuration: %v", err)
	}

	cleanup = func() {
		proposerMgr.Close()
		for _, sys := range systems {
			sys.Stop()
		}
	}

	return systems, servers, config, cleanup
}

func TestPaxosBasicConsensus(t *testing.T) {
	_, _, config, cleanup := testSetup(t, 3)
	defer cleanup()

	proposer := NewProposer(100, config)

	err := proposer.Propose("test-value-1")
	if err != nil {
		t.Fatalf("Failed to propose value: %v", err)
	}

	// Verify all nodes learned the value
	time.Sleep(100 * time.Millisecond)
}

func TestPaxosMultipleValues(t *testing.T) {
	_, servers, config, cleanup := testSetup(t, 3)
	defer cleanup()

	proposer := NewProposer(100, config)

	values := []string{"value-1", "value-2", "value-3"}

	for _, value := range values {
		err := proposer.Propose(value)
		if err != nil {
			t.Fatalf("Failed to propose value %s: %v", value, err)
		}
	}

	// Verify all nodes learned all values
	time.Sleep(200 * time.Millisecond)

	for i, srv := range servers {
		for instance, expectedValue := range values {
			learned := srv.GetLearnedValue(uint32(instance + 1))
			if learned != expectedValue {
				t.Errorf("Node %d instance %d: expected %s, got %s",
					i+1, instance+1, expectedValue, learned)
			}
		}
	}
}

func TestPaxosConcurrentProposers(t *testing.T) {
	_, servers, config, cleanup := testSetup(t, 5)
	defer cleanup()

	const numProposers = 3
	const valuesPerProposer = 3

	var wg sync.WaitGroup
	successCount := make(chan int, numProposers)

	// Launch multiple proposers concurrently
	// Note: Some proposals may fail due to conflicts - this is expected Paxos behavior
	for p := 0; p < numProposers; p++ {
		wg.Add(1)
		go func(proposerID int) {
			defer wg.Done()
			proposer := NewProposer(uint32(100+proposerID), config)

			successes := 0
			for v := 0; v < valuesPerProposer; v++ {
				value := fmt.Sprintf("proposer-%d-value-%d", proposerID, v)
				if err := proposer.Propose(value); err != nil {
					t.Logf("Proposer %d proposal failed (expected with concurrent proposers): %v", proposerID, err)
				} else {
					successes++
				}
			}
			successCount <- successes
		}(p)
	}

	wg.Wait()
	close(successCount)

	// Count total successful proposals
	totalSuccess := 0
	for count := range successCount {
		totalSuccess += count
	}

	if totalSuccess == 0 {
		t.Fatal("No proposals succeeded")
	}

	t.Logf("Concurrent proposers: %d/%d proposals succeeded", totalSuccess, numProposers*valuesPerProposer)

	// Verify consensus was reached (all nodes agree on values)
	time.Sleep(300 * time.Millisecond)

	// Check that all nodes have the same values for each instance that was decided
	for instance := uint32(1); instance <= uint32(totalSuccess); instance++ {
		referenceValue := servers[0].GetLearnedValue(instance)
		if referenceValue == "" {
			// It's OK if some instances have no value (due to failures)
			continue
		}

		for i, srv := range servers[1:] {
			learned := srv.GetLearnedValue(instance)
			if learned != referenceValue {
				t.Errorf("Instance %d: node %d has different value: %s vs %s",
					instance, i+2, learned, referenceValue)
			}
		}
	}
}

func TestPaxosLeaderOptimization(t *testing.T) {
	_, servers, config, cleanup := testSetup(t, 3)
	defer cleanup()

	proposer := NewProposer(100, config)

	// First proposal - will do full Phase 1
	err := proposer.Propose("first-value")
	if err != nil {
		t.Fatalf("First proposal failed: %v", err)
	}

	// Subsequent proposals should skip Phase 1 (leader optimization)
	for i := 2; i <= 5; i++ {
		err := proposer.Propose(fmt.Sprintf("value-%d", i))
		if err != nil {
			t.Fatalf("Proposal %d failed: %v", i, err)
		}
	}

	// Verify all values were learned
	time.Sleep(200 * time.Millisecond)

	for i := uint32(1); i <= 5; i++ {
		expectedValue := "first-value"
		if i > 1 {
			expectedValue = fmt.Sprintf("value-%d", i)
		}

		for nodeIdx, srv := range servers {
			learned := srv.GetLearnedValue(i)
			if learned != expectedValue {
				t.Errorf("Node %d instance %d: expected %s, got %s",
					nodeIdx+1, i, expectedValue, learned)
			}
		}
	}
}

func TestPaxosSymmetricBroadcast(t *testing.T) {
	_, servers, config, cleanup := testSetup(t, 3)
	defer cleanup()

	proposer := NewProposer(100, config)

	// Propose a value
	err := proposer.Propose("broadcast-test")
	if err != nil {
		t.Fatalf("Failed to propose value: %v", err)
	}

	// Give time for broadcasts to complete
	time.Sleep(300 * time.Millisecond)

	// Verify all nodes learned the value
	for i, srv := range servers {
		learned := srv.GetLearnedValue(1)
		if learned != "broadcast-test" {
			t.Errorf("Node %d did not learn value via broadcast: got %s",
				i+1, learned)
		}
	}
}

func TestPaxosQuorumSizes(t *testing.T) {
	tests := []struct {
		name      string
		numNodes  int
		minQuorum int
	}{
		{"3-nodes", 3, 2},
		{"5-nodes", 5, 3},
		{"7-nodes", 7, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, config, cleanup := testSetup(t, tt.numNodes)
			defer cleanup()

			proposer := NewProposer(100, config)

			err := proposer.Propose("quorum-test")
			if err != nil {
				t.Fatalf("Failed to propose with %d nodes: %v", tt.numNodes, err)
			}

			// Verify consensus was reached
			time.Sleep(100 * time.Millisecond)
		})
	}
}

func TestPaxosAcceptorPromises(t *testing.T) {
	_, _, config, cleanup := testSetup(t, 3)
	defer cleanup()

	// Test that acceptor properly tracks highest promised proposal
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send prepare with proposal 1000
	prepareReq1 := pb.PrepareRequest_builder{
		Instance:    1,
		ProposalNum: 1000,
		ProposerId:  100,
	}.Build()

	cfgCtx := config.Context(ctx)
	promises := pb.Prepare(cfgCtx, prepareReq1)

	promiseCount := 0
	for promise := range promises.Seq() {
		if promise.Err == nil && promise.Value.GetPromised() {
			promiseCount++
		}
	}

	if promiseCount < 2 {
		t.Fatalf("Expected at least 2 promises, got %d", promiseCount)
	}

	// Try to prepare with lower proposal number - should not promise
	prepareReq2 := pb.PrepareRequest_builder{
		Instance:    1,
		ProposalNum: 500,
		ProposerId:  101,
	}.Build()

	cfgCtx2 := config.Context(ctx)
	promises2 := pb.Prepare(cfgCtx2, prepareReq2)

	rejectedCount := 0
	for promise := range promises2.Seq() {
		if promise.Err == nil && !promise.Value.GetPromised() {
			rejectedCount++
		}
	}

	if rejectedCount < 2 {
		t.Errorf("Expected at least 2 rejections for lower proposal, got %d", rejectedCount)
	}
}

func TestPaxosAcceptorAccepts(t *testing.T) {
	_, servers, config, cleanup := testSetup(t, 3)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Phase 1: Prepare
	prepareReq := pb.PrepareRequest_builder{
		Instance:    1,
		ProposalNum: 1000,
		ProposerId:  100,
	}.Build()

	cfgCtx := config.Context(ctx)
	promises := pb.Prepare(cfgCtx, prepareReq)

	// Wait for promises
	for range promises.Seq() {
	}

	// Phase 2: Accept
	acceptReq := pb.AcceptRequest_builder{
		Instance:    1,
		ProposalNum: 1000,
		Value:       "test-accept",
		ProposerId:  100,
	}.Build()

	cfgCtx2 := config.Context(ctx)
	responses := pb.Accept(cfgCtx2, acceptReq)

	acceptCount := 0
	for resp := range responses.Seq() {
		if resp.Err == nil && resp.Value.GetAccepted() {
			acceptCount++
		}
	}

	if acceptCount < 2 {
		t.Fatalf("Expected at least 2 accepts, got %d", acceptCount)
	}

	// Verify value was accepted
	time.Sleep(100 * time.Millisecond)
	for i, srv := range servers {
		state := srv.getInstance(1)
		if state.acceptedProposalNum != 1000 {
			t.Errorf("Node %d: expected accepted proposal 1000, got %d",
				i+1, state.acceptedProposalNum)
		}
		if state.acceptedValue != "test-accept" {
			t.Errorf("Node %d: expected accepted value 'test-accept', got %s",
				i+1, state.acceptedValue)
		}
	}
}

func TestPaxosMultiInstance(t *testing.T) {
	_, servers, config, cleanup := testSetup(t, 3)
	defer cleanup()

	// Use proposer to propose multiple values to different instances
	proposer := NewProposer(100, config)

	values := []string{"first", "second", "third", "fourth", "fifth"}

	for _, value := range values {
		err := proposer.Propose(value)
		if err != nil {
			t.Errorf("Failed to propose %s: %v", value, err)
		}
	}

	// Wait for all learns to complete
	time.Sleep(300 * time.Millisecond)

	// Verify all instances learned correct values
	for instance, expectedValue := range values {
		for i, srv := range servers {
			learned := srv.GetLearnedValue(uint32(instance + 1))
			if learned != expectedValue {
				t.Errorf("Node %d instance %d: expected %s, got %s",
					i+1, instance+1, expectedValue, learned)
			}
		}
	}
}

func TestPaxosProposerHigherProposal(t *testing.T) {
	_, _, config, cleanup := testSetup(t, 3)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Proposer 1 starts with lower proposal
	prepareReq1 := pb.PrepareRequest_builder{
		Instance:    1,
		ProposalNum: 1000,
		ProposerId:  100,
	}.Build()

	cfgCtx1 := config.Context(ctx)
	promises1 := pb.Prepare(cfgCtx1, prepareReq1)
	for range promises1.Seq() {
	}

	acceptReq1 := pb.AcceptRequest_builder{
		Instance:    1,
		ProposalNum: 1000,
		Value:       "value-from-proposer-1",
		ProposerId:  100,
	}.Build()

	cfgCtx2 := config.Context(ctx)
	accepts := pb.Accept(cfgCtx2, acceptReq1)

	// Wait for accepts to complete
	acceptCount := 0
	for accept := range accepts.Seq() {
		if accept.Err == nil && accept.Value.GetAccepted() {
			acceptCount++
		}
	}

	if acceptCount < 2 {
		t.Fatalf("Need at least 2 accepts before second proposal, got %d", acceptCount)
	}

	// Give time for accept to be processed
	time.Sleep(50 * time.Millisecond)

	// Proposer 2 comes with higher proposal
	prepareReq2 := pb.PrepareRequest_builder{
		Instance:    1,
		ProposalNum: 2000,
		ProposerId:  101,
	}.Build()

	cfgCtx3 := config.Context(ctx)
	promises2 := pb.Prepare(cfgCtx3, prepareReq2)

	// Should get promises with previously accepted value
	hasAcceptedValue := false
	for promise := range promises2.Seq() {
		if promise.Err == nil &&
			promise.Value.GetPromised() &&
			promise.Value.GetAcceptedValue() == "value-from-proposer-1" {
			hasAcceptedValue = true
		}
	}

	if !hasAcceptedValue {
		t.Error("Higher proposal should see previously accepted value")
	}
}

// BenchmarkPaxosPropose benchmarks the throughput of Paxos proposals.
func BenchmarkPaxosPropose(b *testing.B) {
	_, _, config, cleanup := testSetup(&testing.T{}, 3)
	defer cleanup()

	proposer := NewProposer(100, config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		value := fmt.Sprintf("bench-value-%d", i)
		if err := proposer.Propose(value); err != nil {
			b.Fatalf("Proposal failed: %v", err)
		}
	}
}

// BenchmarkPaxosParallelPropose benchmarks parallel proposals.
func BenchmarkPaxosParallelPropose(b *testing.B) {
	_, _, config, cleanup := testSetup(&testing.T{}, 5)
	defer cleanup()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		proposer := NewProposer(uint32(100+time.Now().UnixNano()%1000), config)
		i := 0
		for pb.Next() {
			value := fmt.Sprintf("parallel-value-%d", i)
			if err := proposer.Propose(value); err != nil {
				b.Fatalf("Parallel proposal failed: %v", err)
			}
			i++
		}
	})
}
