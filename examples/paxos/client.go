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

// Proposer implements the Multi-Paxos proposer role.
type Proposer struct {
	id                  uint32
	cfg                 pb.Configuration
	proposalNum         uint32
	preparedInstances   map[uint32]bool // Track instances where Phase 1 completed
	freshInstances      map[uint32]bool // Track instances WE created (not discovered)
	nextInstance        uint32          // Next instance number to propose
	highestSeenInstance uint32          // Highest instance number we've prepared
}

func NewProposer(id uint32, cfg pb.Configuration) *Proposer {
	return &Proposer{
		id:                  id,
		cfg:                 cfg,
		proposalNum:         id * 1000, // Start with proposer_id * 1000 to avoid conflicts
		preparedInstances:   make(map[uint32]bool),
		freshInstances:      make(map[uint32]bool),
		nextInstance:        1, // Start from instance 1
		highestSeenInstance: 0,
	}
}

// Propose attempts to get consensus on a value using Multi-Paxos.
// Phase 1 is only skipped for instances that this proposer has already prepared
// and are consecutive (no gaps in instance numbers).
func (p *Proposer) Propose(value string) error {
	instance := p.nextInstance
	p.nextInstance++

	fmt.Printf("\n=== Proposer %d: Proposing value '%s' for instance %d ===\n", p.id, value, instance)

	// Increment proposal number for new proposal
	p.proposalNum++
	currentProposal := p.proposalNum

	var valueToPropose string

	// Multi-Paxos optimization: Skip Phase 1 if:
	// 1. We've already explicitly prepared this exact instance, OR
	// 2. This is the next consecutive instance (N+1) after our last successful one
	//    AND the previous instance was FRESH (we created it, not discovered it)
	skipPhase1 := p.preparedInstances[instance] ||
		(p.highestSeenInstance > 0 &&
			instance == p.highestSeenInstance+1 &&
			p.preparedInstances[p.highestSeenInstance] &&
			p.freshInstances[p.highestSeenInstance])

	// Phase 1: Prepare (skip if conditions met)
	if !skipPhase1 {
		fmt.Printf("\n--- Phase 1: PREPARE(inst=%d, num=%d) ---\n", instance, currentProposal)
		prepareReq := pb.PrepareRequest_builder{
			Instance:    instance,
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

		// Mark this instance as prepared
		p.preparedInstances[instance] = true
		if instance > p.highestSeenInstance {
			p.highestSeenInstance = instance
		}

		if len(p.preparedInstances) == 1 {
			fmt.Printf("Proposer %d: 🎖️  Became LEADER for subsequent proposals\n", p.id)
		}

		// If any acceptor had previously accepted a value, we must use that value
		valueToPropose = value
		if highestAcceptedValue != "" {
			valueToPropose = highestAcceptedValue
			fmt.Printf("Proposer %d: Using previously accepted value '%s' instead of '%s'\n",
				p.id, highestAcceptedValue, value)
			// This instance was NOT fresh - it was already decided
		} else {
			// This instance was fresh - no previous value
			p.freshInstances[instance] = true
		}
	} else {
		// We can skip Phase 1 because we've already prepared this or the previous consecutive instance
		if p.preparedInstances[instance] {
			fmt.Printf("\n--- Phase 1: SKIPPED (instance already prepared) ---\n")
		} else {
			fmt.Printf("\n--- Phase 1: SKIPPED (consecutive fresh instance) ---\n")
			// Mark this new consecutive instance as fresh
			p.freshInstances[instance] = true
		}
		valueToPropose = value

		// Mark this instance as prepared
		p.preparedInstances[instance] = true
		if instance > p.highestSeenInstance {
			p.highestSeenInstance = instance
		}
	}

	// Phase 2: Accept
	fmt.Printf("\n--- Phase 2: ACCEPT(inst=%d, num=%d, val='%s') ---\n", instance, currentProposal, valueToPropose)
	acceptReq := pb.AcceptRequest_builder{
		Instance:    instance,
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

	quorumSize := int(p.cfg.Size())/2 + 1
	fmt.Printf("Proposer %d: Received %d acceptances (quorum: %d)\n", p.id, acceptCount, quorumSize)

	if acceptCount < quorumSize {
		// Lost leadership for this instance, need to retry Phase 1
		delete(p.preparedInstances, instance)
		return fmt.Errorf("failed to get quorum of acceptances (%d/%d)", acceptCount, quorumSize)
	}

	// Phase 3: Learn - notify all nodes of the chosen value
	fmt.Printf("\n--- Phase 3: LEARN (broadcasting chosen value) ---\n")
	learnReq := pb.LearnRequest_builder{
		Instance:    instance,
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

	fmt.Printf("\n🎉 Proposer %d: SUCCESS! Value '%s' was chosen (inst=%d, proposal=%d)\n",
		p.id, valueToPropose, instance, currentProposal)
	fmt.Printf("   %d nodes learned the value\n\n", learnCount)

	return nil
}

func runProposer(proposerID uint32, values []string) {
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

	// Create proposer
	proposer := NewProposer(proposerID, cfg)

	// Propose multiple values to demonstrate Multi-Paxos leader optimization
	for i, value := range values {
		log.Printf("\n========== Proposing value %d/%d ==========", i+1, len(values))
		if err := proposer.Propose(value); err != nil {
			log.Fatalf("Proposal %d failed: %v", i+1, err)
		}
		// Small delay between proposals to make output readable
		time.Sleep(500 * time.Millisecond)
	}

	log.Printf("\n✅ All %d values proposed successfully!", len(values))
}
