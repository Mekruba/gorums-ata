package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/paxos/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func runServer(id uint32) {
	// Find this node's address
	var addr string
	for _, node := range nodes {
		if node.id == id {
			addr = node.addr
			break
		}
	}

	// Build node address list for symmetric communication
	var nodeAddrs []string
	for _, node := range nodes {
		nodeAddrs = append(nodeAddrs, node.addr)
	}

	// Create System with dynamic peer tracking (symmetric communication)
	sys, err := gorums.NewSystem(addr,
		gorums.WithConfig(id, gorums.WithNodeList(nodeAddrs)),
		gorums.WithGRPCServerOptions(
			grpc.Creds(insecure.NewCredentials()),
		),
	)
	if err != nil {
		log.Fatalf("Failed to create system: %v", err)
	}

	// Create Paxos server without static config
	paxosSrv := NewPaxosServer(id)

	// Create manager for proposer role
	mgr := pb.NewManager(
		gorums.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		),
	)

	// Register service with System (before starting to serve)
	sys.RegisterService(mgr, func(srv *gorums.Server) {
		pb.RegisterPaxosServer(srv, paxosSrv)
	})

	// Start server in background so it's listening for connections
	go func() {
		if err := sys.Serve(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Give server time to start listening
	time.Sleep(200 * time.Millisecond)

	// Build map of all nodes
	nodeMap := make(map[uint32]nodeAddr)
	for _, node := range nodes {
		nodeMap[node.id] = nodeAddr{addr: node.addr}
	}

	// Use System.NewOutboundConfig to establish bidirectional channels
	// This automatically includes NodeID metadata for symmetric communication
	proposerConfig, err := sys.NewOutboundConfig(
		gorums.WithNodes(nodeMap),
		gorums.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		),
	)
	if err != nil {
		log.Fatalf("Failed to create proposer config: %v", err)
	}

	// Set proposer config for client-initiated calls
	paxosSrv.setProposerConfig(proposerConfig)

	// Wait for bidirectional connections to establish
	time.Sleep(500 * time.Millisecond)

	if cfg := sys.Config(); cfg != nil {
		log.Printf("Node %d starting on %s", id, addr)
		log.Printf("Connected to %d peers (including self) via symmetric channels", cfg.Size())
		log.Printf("Quorum size: %d nodes", cfg.Size()/2+1)
	} else {
		log.Printf("Node %d starting on %s", id, addr)
	}

	// Handle shutdown
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		log.Println("Shutting down...")
		sys.Stop() // System handles both server and manager cleanup
		os.Exit(0)
	}()

	// Keep running (server is already serving in goroutine)
	select {}
}

// nodeAddr implements NodeAddress interface
type nodeAddr struct {
	addr string
}

func (n nodeAddr) Addr() string {
	return n.addr
}

// instanceState holds the state for a single Paxos instance.
type instanceState struct {
	// Acceptor state (persistent in real implementation)
	promisedProposalNum uint32 // Highest proposal number promised
	acceptedProposalNum uint32 // Highest proposal number accepted
	acceptedValue       string // Value of highest accepted proposal

	// Learner state
	learnedValue   string // The chosen value
	learnedFromNum uint32 // Proposal number of learned value
}

// PaxosServer implements a Multi-Paxos acceptor and learner.
type PaxosServer struct {
	id             uint32
	proposerConfig pb.Configuration // For client-initiated proposals
	// Server uses dynamic config via ctx.Config() for server-initiated calls

	mu        sync.RWMutex
	instances map[uint32]*instanceState // Per-instance state
}

func NewPaxosServer(id uint32) *PaxosServer {
	return &PaxosServer{
		id:        id,
		instances: make(map[uint32]*instanceState),
	}
}

// setProposerConfig sets the configuration for client-initiated proposals.
func (p *PaxosServer) setProposerConfig(cfg pb.Configuration) {
	p.proposerConfig = cfg
}

// getInstance returns the state for the given instance, creating it if needed.
func (p *PaxosServer) getInstance(instance uint32) *instanceState {
	p.mu.Lock()
	defer p.mu.Unlock()

	if state, exists := p.instances[instance]; exists {
		return state
	}

	state := &instanceState{}
	p.instances[instance] = state
	return state
}

// Prepare handles Phase 1a: Prepare request from proposer.
// Acceptor promises not to accept proposals with lower numbers.
func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *pb.PrepareRequest) (*pb.PromiseResponse, error) {
	instance := req.GetInstance()
	proposalNum := req.GetProposalNum()
	proposerID := req.GetProposerId()

	fmt.Printf("Node %d: Received PREPARE(inst=%d, num=%d) from proposer %d\n",
		p.id, instance, proposalNum, proposerID)

	state := p.getInstance(instance)

	p.mu.Lock()
	defer p.mu.Unlock()

	// If this proposal number is higher than any we've seen, promise it
	if proposalNum > state.promisedProposalNum {
		state.promisedProposalNum = proposalNum

		fmt.Printf("Node %d: PROMISE(inst=%d, num=%d) - accepted_num=%d, accepted_val='%s'\n",
			p.id, instance, proposalNum, state.acceptedProposalNum, state.acceptedValue)

		return pb.PromiseResponse_builder{
			AcceptorId:          p.id,
			Instance:            instance,
			Promised:            true,
			AcceptedProposalNum: state.acceptedProposalNum,
			AcceptedValue:       state.acceptedValue,
		}.Build(), nil
	}

	// Reject: we've already promised a higher proposal number
	fmt.Printf("Node %d: REJECT PREPARE(inst=%d, num=%d) - already promised %d\n",
		p.id, instance, proposalNum, state.promisedProposalNum)

	return pb.PromiseResponse_builder{
		AcceptorId: p.id,
		Instance:   instance,
		Promised:   false,
	}.Build(), nil
}

// Accept handles Phase 2a: Accept request from proposer.
// Acceptor accepts the value if it hasn't promised a higher proposal.
func (p *PaxosServer) Accept(ctx gorums.ServerCtx, req *pb.AcceptRequest) (*pb.AcceptedResponse, error) {
	instance := req.GetInstance()
	proposalNum := req.GetProposalNum()
	value := req.GetValue()
	proposerID := req.GetProposerId()

	fmt.Printf("Node %d: Received ACCEPT(inst=%d, num=%d, val='%s') from proposer %d\n",
		p.id, instance, proposalNum, value, proposerID)

	state := p.getInstance(instance)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Accept if proposal number >= promised number
	if proposalNum >= state.promisedProposalNum {
		state.promisedProposalNum = proposalNum
		state.acceptedProposalNum = proposalNum
		state.acceptedValue = value

		fmt.Printf("Node %d: ACCEPTED(inst=%d, num=%d, val='%s')\n", p.id, instance, proposalNum, value)

		return pb.AcceptedResponse_builder{
			AcceptorId:  p.id,
			Instance:    instance,
			Accepted:    true,
			ProposalNum: proposalNum,
		}.Build(), nil
	}

	// Reject: we've promised a higher proposal number
	fmt.Printf("Node %d: REJECT ACCEPT(inst=%d, num=%d) - promised %d\n",
		p.id, instance, proposalNum, state.promisedProposalNum)

	return pb.AcceptedResponse_builder{
		AcceptorId:  p.id,
		Instance:    instance,
		Accepted:    false,
		ProposalNum: state.promisedProposalNum,
	}.Build(), nil
}

// Learn handles learning of chosen value.
// When a node learns a value, it broadcasts to other nodes using ServerCtx.Config().
func (p *PaxosServer) Learn(ctx gorums.ServerCtx, req *pb.LearnRequest) (*pb.LearnResponse, error) {
	instance := req.GetInstance()
	proposalNum := req.GetProposalNum()
	value := req.GetValue()
	proposerID := req.GetProposerId()

	state := p.getInstance(instance)

	p.mu.Lock()
	// Only learn if we haven't learned anything yet, or this is a higher proposal
	if state.learnedValue == "" || proposalNum > state.learnedFromNum {
		state.learnedValue = value
		state.learnedFromNum = proposalNum
		p.mu.Unlock()

		fmt.Printf("\n🎉 Node %d: LEARNED value '%s' (inst=%d, proposal=%d from proposer %d)\n\n",
			p.id, value, instance, proposalNum, proposerID)

		// Broadcast to other nodes using proposerConfig
		// This demonstrates server-to-server communication in Multi-Paxos
		// We use proposerConfig (all nodes) instead of ctx.Config() (connected peers)
		// because ctx.Config() only knows about peers that have connected TO this node
		if p.proposerConfig != nil {
			fmt.Printf("Node %d: Broadcasting to other nodes (config size: %d)\n", p.id, p.proposerConfig.Size())
			go p.broadcastLearn(p.proposerConfig, req)
		}

		return pb.LearnResponse_builder{
			LearnerId: p.id,
			Instance:  instance,
			Learned:   true,
		}.Build(), nil
	}
	p.mu.Unlock()

	// Already learned this or a newer value
	return pb.LearnResponse_builder{
		LearnerId: p.id,
		Instance:  instance,
		Learned:   false,
	}.Build(), nil
}

// broadcastLearn broadcasts learned value to other nodes (excluding self).
func (p *PaxosServer) broadcastLearn(cfg gorums.Configuration, req *pb.LearnRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	fmt.Printf("Node %d: broadcastLearn called with config size %d\n", p.id, len(cfg))

	// Use the full configuration (including self) and filter responses afterward
	cfgCtx := cfg.Context(ctx)
	learned := pb.Learn(cfgCtx, req)

	// Check if there's an error getting responses
	numResponses := 0
	newlyLearned := 0
	alreadyLearned := 0
	errorCount := 0

	for learn := range learned.Seq() {
		numResponses++

		// Skip responses from ourselves
		if learn.NodeID == p.id {
			continue
		}

		if learn.Err != nil {
			errorCount++
			fmt.Printf("Node %d: ✗ Broadcast error to node %d: %v\n", p.id, learn.NodeID, learn.Err)
			continue
		}

		if learn.Value.GetLearned() {
			newlyLearned++
			fmt.Printf("Node %d: ✓ Node %d newly learned value (inst=%d)\n",
				p.id, learn.NodeID, req.GetInstance())
		} else {
			alreadyLearned++
			fmt.Printf("Node %d: ✓ Node %d already knew value (inst=%d)\n",
				p.id, learn.NodeID, req.GetInstance())
		}
	}
	fmt.Printf("Node %d: Broadcast complete - %d newly learned, %d already knew, %d errors\n",
		p.id, newlyLearned, alreadyLearned, errorCount)
}

// GetLearnedValue returns the learned value for a specific instance (for testing/querying).
func (p *PaxosServer) GetLearnedValue(instance uint32) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if state, exists := p.instances[instance]; exists {
		return state.learnedValue
	}
	return ""
}
