package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/relab/gorums/examples/paxos/proto"
)

// Proposer implements Multi-Paxos proposer role without verbose logging.
type Proposer struct {
	mu                  sync.Mutex
	id                  uint32
	cfg                 pb.Configuration
	proposalNum         uint32
	preparedInstances   map[uint32]bool
	freshInstances      map[uint32]bool
	nextInstance        uint32
	stride              uint32 // instance stride to avoid collisions between concurrent proposers
	highestSeenInstance uint32
}

// NewProposer creates a proposer that owns every instance (stride=1).
func NewProposer(id uint32, cfg pb.Configuration) *Proposer {
	return NewProposerWithOffset(id, cfg, 1, 1)
}

// NewProposerWithOffset creates a proposer whose instances start at startInstance
// and advance by stride, ensuring non-overlapping instance sets across concurrent proposers.
func NewProposerWithOffset(id uint32, cfg pb.Configuration, startInstance, stride uint32) *Proposer {
	if stride < 1 {
		stride = 1
	}
	return &Proposer{
		id:                id,
		cfg:               cfg,
		proposalNum:       id * 100000,
		preparedInstances: make(map[uint32]bool),
		freshInstances:    make(map[uint32]bool),
		nextInstance:      startInstance,
		stride:            stride,
	}
}

// Propose runs Multi-Paxos for the given value with automatic retry on rejection.
func (p *Proposer) Propose(value string) error {
	const maxRetries = 8

	p.mu.Lock()
	instance := p.nextInstance
	p.nextInstance += p.stride
	p.mu.Unlock()

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Back off and use a much higher proposal number to leap-frog competing proposers.
			time.Sleep(time.Duration(attempt*attempt) * 2 * time.Millisecond)
			p.mu.Lock()
			p.proposalNum += uint32(attempt * 50000)
			p.mu.Unlock()
			// Must redo Phase 1 after being rejected.
			p.mu.Lock()
			delete(p.preparedInstances, instance)
			p.mu.Unlock()
		}

		if err := p.tryPropose(instance, value); err == nil {
			return nil
		}
	}
	return fmt.Errorf("proposer %d: gave up after %d attempts on instance %d", p.id, maxRetries, instance)
}

func (p *Proposer) tryPropose(instance uint32, value string) error {
	p.mu.Lock()
	p.proposalNum++
	currentProposal := p.proposalNum

	skipPhase1 := p.preparedInstances[instance] ||
		(p.highestSeenInstance > 0 &&
			instance == p.highestSeenInstance+p.stride &&
			p.preparedInstances[p.highestSeenInstance] &&
			p.freshInstances[p.highestSeenInstance])
	p.mu.Unlock()

	var valueToPropose string

	if !skipPhase1 {
		prepareReq := pb.PrepareRequest_builder{
			Instance:    instance,
			ProposalNum: currentProposal,
			ProposerId:  p.id,
		}.Build()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cfgCtx := p.cfg.Context(ctx)
		promises := pb.Prepare(cfgCtx, prepareReq)

		var promiseCount int
		var highestAcceptedNum uint32
		var highestAcceptedValue string

		for promise := range promises.Seq() {
			if promise.Err != nil {
				continue
			}
			resp := promise.Value
			if resp.GetPromised() {
				promiseCount++
				if resp.GetAcceptedProposalNum() > highestAcceptedNum {
					highestAcceptedNum = resp.GetAcceptedProposalNum()
					highestAcceptedValue = resp.GetAcceptedValue()
				}
			}
		}

		quorumSize := int(p.cfg.Size())/2 + 1
		if promiseCount < quorumSize {
			return fmt.Errorf("phase1: got %d promises, need %d", promiseCount, quorumSize)
		}
		p.mu.Lock()
		p.preparedInstances[instance] = true
		if instance > p.highestSeenInstance {
			p.highestSeenInstance = instance
		}
		if highestAcceptedValue != "" {
			valueToPropose = highestAcceptedValue
		} else {
			valueToPropose = value
			p.freshInstances[instance] = true
		}
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		p.freshInstances[instance] = true
		p.preparedInstances[instance] = true
		if instance > p.highestSeenInstance {
			p.highestSeenInstance = instance
		}
		p.mu.Unlock()
		valueToPropose = value
	}

	// Phase 2: Accept
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
		p.mu.Lock()
		delete(p.preparedInstances, instance)
		p.mu.Unlock()
		return fmt.Errorf("phase2: got %d acceptances, need %d", acceptCount, quorumSize)
	}

	// Phase 3: Learn
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
	for range learned.Seq() {
		// drain
	}

	return nil
}
