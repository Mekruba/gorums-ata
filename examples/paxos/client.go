package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/paxos/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Proposer implements the Paxos proposer role.
type Proposer struct {
	id          uint32
	cfg         pb.Configuration
	proposalNum uint32
}

func NewProposer(id uint32, cfg pb.Configuration) *Proposer {
	return &Proposer{
		id:          id,
		cfg:         cfg,
		proposalNum: id * 1000, // Start with proposer_id * 1000 to avoid conflicts
	}
}

// Propose attempts to get consensus on a value using the Paxos protocol.
func (p *Proposer) Propose(value string) error {
	fmt.Printf("\n=== Proposer %d: Proposing value '%s' ===\n", p.id, value)

	// Increment proposal number for new proposal
	p.proposalNum++
	currentProposal := p.proposalNum

	// Phase 1: Prepare
	fmt.Printf("\n--- Phase 1: PREPARE(%d) ---\n", currentProposal)
	prepareReq := pb.PrepareRequest_builder{
		ProposalNum: currentProposal,
		ProposerId:  p.id,
	}.Build()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfgCtx := p.cfg.Context(ctx)
	promises := pb.Prepare(cfgCtx, prepareReq)

	// Collect promises from a quorum
	var promiseCount int
	var highestAcceptedNum uint32
	var highestAcceptedValue string

	for promise := range promises.Seq() {
		if promise.Err != nil {
			log.Printf("Proposer %d: Error from node %d: %v", p.id, promise.NodeID, promise.Err)
			continue
		}

		resp := promise.Value
		if resp.GetPromised() {
			promiseCount++
			fmt.Printf("Proposer %d: ✓ PROMISE from acceptor %d (accepted_num=%d, accepted_val='%s')\n",
				p.id, resp.GetAcceptorId(), resp.GetAcceptedProposalNum(), resp.GetAcceptedValue())

			// Track highest accepted proposal
			if resp.GetAcceptedProposalNum() > highestAcceptedNum {
				highestAcceptedNum = resp.GetAcceptedProposalNum()
				highestAcceptedValue = resp.GetAcceptedValue()
			}
		} else {
			fmt.Printf("Proposer %d: ✗ REJECTED by acceptor %d\n", p.id, resp.GetAcceptorId())
		}
	}

	quorumSize := int(p.cfg.Size())/2 + 1
	fmt.Printf("Proposer %d: Received %d promises (quorum: %d)\n", p.id, promiseCount, quorumSize)

	if promiseCount < quorumSize {
		return fmt.Errorf("failed to get quorum of promises (%d/%d)", promiseCount, quorumSize)
	}

	// Phase 2: Accept
	// If any acceptor had previously accepted a value, we must use that value
	valueToPropose := value
	if highestAcceptedValue != "" {
		valueToPropose = highestAcceptedValue
		fmt.Printf("Proposer %d: Using previously accepted value '%s' instead of '%s'\n",
			p.id, highestAcceptedValue, value)
	}

	fmt.Printf("\n--- Phase 2: ACCEPT(%d, '%s') ---\n", currentProposal, valueToPropose)
	acceptReq := pb.AcceptRequest_builder{
		ProposalNum: currentProposal,
		Value:       valueToPropose,
		ProposerId:  p.id,
	}.Build()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	cfgCtx2 := p.cfg.Context(ctx2)
	accepteds := pb.Accept(cfgCtx2, acceptReq)

	// Collect acceptances from a quorum
	var acceptCount int
	for accepted := range accepteds.Seq() {
		if accepted.Err != nil {
			log.Printf("Proposer %d: Error from node %d: %v", p.id, accepted.NodeID, accepted.Err)
			continue
		}

		resp := accepted.Value
		if resp.GetAccepted() {
			acceptCount++
			fmt.Printf("Proposer %d: ✓ ACCEPTED from acceptor %d\n", p.id, resp.GetAcceptorId())
		} else {
			fmt.Printf("Proposer %d: ✗ REJECTED by acceptor %d\n", p.id, resp.GetAcceptorId())
		}
	}

	fmt.Printf("Proposer %d: Received %d acceptances (quorum: %d)\n", p.id, acceptCount, quorumSize)

	if acceptCount < quorumSize {
		return fmt.Errorf("failed to get quorum of acceptances (%d/%d)", acceptCount, quorumSize)
	}

	// Phase 3: Learn - notify all nodes of the chosen value
	fmt.Printf("\n--- Phase 3: LEARN (broadcasting chosen value) ---\n")
	learnReq := pb.LearnRequest_builder{
		ProposalNum: currentProposal,
		Value:       valueToPropose,
		ProposerId:  p.id,
	}.Build()

	ctx3, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel3()

	cfgCtx3 := p.cfg.Context(ctx3)
	learned := pb.Learn(cfgCtx3, learnReq)

	var learnCount int
	for learn := range learned.Seq() {
		if learn.Err != nil {
			log.Printf("Proposer %d: Learn error from node %d: %v", p.id, learn.NodeID, learn.Err)
			continue
		}

		resp := learn.Value
		if resp.GetLearned() {
			learnCount++
			fmt.Printf("Proposer %d: ✓ Node %d learned the value\n", p.id, resp.GetLearnerId())
		}
	}

	fmt.Printf("\n🎉 Proposer %d: SUCCESS! Value '%s' was chosen (proposal %d)\n",
		p.id, valueToPropose, currentProposal)
	fmt.Printf("   %d nodes learned the value\n\n", learnCount)

	return nil
}

func runProposer(proposerID uint32, value string) {
	// Get all node addresses
	var addrs []string
	for _, node := range nodes {
		addrs = append(addrs, node.addr)
	}

	// Create manager
	mgr := pb.NewManager(
		gorums.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		),
	)
	defer mgr.Close()

	// Create configuration with all servers
	cfg, err := pb.NewConfiguration(mgr, gorums.WithNodeList(addrs))
	if err != nil {
		log.Fatalf("Failed to create configuration: %v", err)
	}

	log.Printf("Proposer %d connected to %d acceptors", proposerID, cfg.Size())

	// Create proposer and propose value
	proposer := NewProposer(proposerID, cfg)
	if err := proposer.Propose(value); err != nil {
		log.Fatalf("Proposal failed: %v", err)
	}
}
