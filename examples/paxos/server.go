package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

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

	// Create manager to connect to other nodes (for proposing)
	mgr := pb.NewManager(
		gorums.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		),
	)

	// Build map of all nodes (for proposer role)
	nodeMap := make(map[uint32]nodeAddr)
	for _, node := range nodes {
		nodeMap[node.id] = nodeAddr{addr: node.addr}
	}

	// Create configuration with all nodes
	var config gorums.Configuration
	var err error
	if len(nodeMap) > 0 {
		config, err = gorums.NewConfiguration(mgr, gorums.WithNodes(nodeMap))
		if err != nil {
			log.Fatalf("Failed to create configuration: %v", err)
		}
	}

	// Create Paxos server (acceptor + learner)
	paxosSrv := NewPaxosServer(id, config)

	// Create Gorums server with configuration
	srv := gorums.NewServer(gorums.WithConfiguration(&config))

	// Register service
	pb.RegisterPaxosServer(srv, paxosSrv)

	// Start listening
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	log.Printf("Node %d starting on %s", id, addr)
	log.Printf("Quorum size: %d nodes", config.Size()/2+1)

	// Handle shutdown
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		log.Println("Shutting down...")
		srv.Stop()
		mgr.Close()
		os.Exit(0)
	}()

	// Start serving
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// nodeAddr implements NodeAddress interface
type nodeAddr struct {
	addr string
}

func (n nodeAddr) Addr() string {
	return n.addr
}

// PaxosServer implements a Paxos acceptor and learner.
type PaxosServer struct {
	id     uint32
	config gorums.Configuration

	// Acceptor state (persistent in real implementation)
	mu                  sync.Mutex
	promisedProposalNum uint32 // Highest proposal number promised
	acceptedProposalNum uint32 // Highest proposal number accepted
	acceptedValue       string // Value of highest accepted proposal

	// Learner state
	learnedValue   string // The chosen value
	learnedFromNum uint32 // Proposal number of learned value
	learnedMu      sync.RWMutex
}

func NewPaxosServer(id uint32, config gorums.Configuration) *PaxosServer {
	return &PaxosServer{
		id:     id,
		config: config,
	}
}

// Prepare handles Phase 1a: Prepare request from proposer.
// Acceptor promises not to accept proposals with lower numbers.
func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *pb.PrepareRequest) (*pb.PromiseResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	proposalNum := req.GetProposalNum()
	proposerID := req.GetProposerId()

	fmt.Printf("Node %d: Received PREPARE(%d) from proposer %d\n",
		p.id, proposalNum, proposerID)

	// If this proposal number is higher than any we've seen, promise it
	if proposalNum > p.promisedProposalNum {
		p.promisedProposalNum = proposalNum

		fmt.Printf("Node %d: PROMISE(%d) - accepted_num=%d, accepted_val='%s'\n",
			p.id, proposalNum, p.acceptedProposalNum, p.acceptedValue)

		return pb.PromiseResponse_builder{
			AcceptorId:          p.id,
			Promised:            true,
			AcceptedProposalNum: p.acceptedProposalNum,
			AcceptedValue:       p.acceptedValue,
		}.Build(), nil
	}

	// Reject: we've already promised a higher proposal number
	fmt.Printf("Node %d: REJECT PREPARE(%d) - already promised %d\n",
		p.id, proposalNum, p.promisedProposalNum)

	return pb.PromiseResponse_builder{
		AcceptorId: p.id,
		Promised:   false,
	}.Build(), nil
}

// Accept handles Phase 2a: Accept request from proposer.
// Acceptor accepts the value if it hasn't promised a higher proposal.
func (p *PaxosServer) Accept(ctx gorums.ServerCtx, req *pb.AcceptRequest) (*pb.AcceptedResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	proposalNum := req.GetProposalNum()
	value := req.GetValue()
	proposerID := req.GetProposerId()

	fmt.Printf("Node %d: Received ACCEPT(%d, '%s') from proposer %d\n",
		p.id, proposalNum, value, proposerID)

	// Accept if proposal number >= promised number
	if proposalNum >= p.promisedProposalNum {
		p.promisedProposalNum = proposalNum
		p.acceptedProposalNum = proposalNum
		p.acceptedValue = value

		fmt.Printf("Node %d: ACCEPTED(%d, '%s')\n", p.id, proposalNum, value)

		return pb.AcceptedResponse_builder{
			AcceptorId:  p.id,
			Accepted:    true,
			ProposalNum: proposalNum,
		}.Build(), nil
	}

	// Reject: we've promised a higher proposal number
	fmt.Printf("Node %d: REJECT ACCEPT(%d) - promised %d\n",
		p.id, proposalNum, p.promisedProposalNum)

	return pb.AcceptedResponse_builder{
		AcceptorId:  p.id,
		Accepted:    false,
		ProposalNum: p.promisedProposalNum,
	}.Build(), nil
}

// Learn handles learning of chosen value.
func (p *PaxosServer) Learn(ctx gorums.ServerCtx, req *pb.LearnRequest) (*pb.LearnResponse, error) {
	proposalNum := req.GetProposalNum()
	value := req.GetValue()
	proposerID := req.GetProposerId()

	p.learnedMu.Lock()
	defer p.learnedMu.Unlock()

	// Only learn if we haven't learned anything yet, or this is a higher proposal
	if p.learnedValue == "" || proposalNum > p.learnedFromNum {
		p.learnedValue = value
		p.learnedFromNum = proposalNum

		fmt.Printf("\n🎉 Node %d: LEARNED value '%s' (proposal %d from proposer %d)\n\n",
			p.id, value, proposalNum, proposerID)

		return pb.LearnResponse_builder{
			LearnerId: p.id,
			Learned:   true,
		}.Build(), nil
	}

	// Already learned this or a newer value
	return pb.LearnResponse_builder{
		LearnerId: p.id,
		Learned:   false,
	}.Build(), nil
}

// GetLearnedValue returns the learned value (for testing/querying).
func (p *PaxosServer) GetLearnedValue() string {
	p.learnedMu.RLock()
	defer p.learnedMu.RUnlock()
	return p.learnedValue
}
