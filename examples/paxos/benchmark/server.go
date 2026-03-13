package main

import (
	"context"
	"sync"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/paxos/proto"
)

// instanceState holds the state for a single Paxos instance.
type instanceState struct {
	promisedProposalNum uint32
	acceptedProposalNum uint32
	acceptedValue       string
	learnedValue        string
	learnedFromNum      uint32
}

// PaxosServer implements acceptor + learner without verbose logging.
type PaxosServer struct {
	id             uint32
	proposerConfig pb.Configuration
	mu             sync.RWMutex
	instances      map[uint32]*instanceState
}

func NewPaxosServer(id uint32) *PaxosServer {
	return &PaxosServer{
		id:        id,
		instances: make(map[uint32]*instanceState),
	}
}

// Reset clears all Paxos instance state so the server is ready for a fresh benchmark run.
func (p *PaxosServer) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.instances = make(map[uint32]*instanceState)
}

func (p *PaxosServer) setProposerConfig(cfg pb.Configuration) {
	p.proposerConfig = cfg
}

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

func (p *PaxosServer) Prepare(ctx gorums.ServerCtx, req *pb.PrepareRequest) (*pb.PromiseResponse, error) {
	state := p.getInstance(req.GetInstance())
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.GetProposalNum() > state.promisedProposalNum {
		state.promisedProposalNum = req.GetProposalNum()
		return pb.PromiseResponse_builder{
			AcceptorId:          p.id,
			Instance:            req.GetInstance(),
			Promised:            true,
			AcceptedProposalNum: state.acceptedProposalNum,
			AcceptedValue:       state.acceptedValue,
		}.Build(), nil
	}
	return pb.PromiseResponse_builder{
		AcceptorId: p.id,
		Instance:   req.GetInstance(),
		Promised:   false,
	}.Build(), nil
}

func (p *PaxosServer) Accept(ctx gorums.ServerCtx, req *pb.AcceptRequest) (*pb.AcceptedResponse, error) {
	state := p.getInstance(req.GetInstance())
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.GetProposalNum() >= state.promisedProposalNum {
		state.promisedProposalNum = req.GetProposalNum()
		state.acceptedProposalNum = req.GetProposalNum()
		state.acceptedValue = req.GetValue()
		return pb.AcceptedResponse_builder{
			AcceptorId:  p.id,
			Instance:    req.GetInstance(),
			Accepted:    true,
			ProposalNum: req.GetProposalNum(),
		}.Build(), nil
	}
	return pb.AcceptedResponse_builder{
		AcceptorId:  p.id,
		Instance:    req.GetInstance(),
		Accepted:    false,
		ProposalNum: state.promisedProposalNum,
	}.Build(), nil
}

func (p *PaxosServer) Learn(ctx gorums.ServerCtx, req *pb.LearnRequest) (*pb.LearnResponse, error) {
	state := p.getInstance(req.GetInstance())
	p.mu.Lock()
	if state.learnedValue == "" || req.GetProposalNum() > state.learnedFromNum {
		state.learnedValue = req.GetValue()
		state.learnedFromNum = req.GetProposalNum()
		p.mu.Unlock()
		if p.proposerConfig != nil {
			go p.broadcastLearn(p.proposerConfig, req)
		}
		return pb.LearnResponse_builder{
			LearnerId: p.id,
			Instance:  req.GetInstance(),
			Learned:   true,
		}.Build(), nil
	}
	p.mu.Unlock()
	return pb.LearnResponse_builder{
		LearnerId: p.id,
		Instance:  req.GetInstance(),
		Learned:   false,
	}.Build(), nil
}

func (p *PaxosServer) broadcastLearn(cfg gorums.Configuration, req *pb.LearnRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfgCtx := cfg.Context(ctx)
	learned := pb.Learn(cfgCtx, req)
	for range learned.Seq() {
		// drain responses
	}
}

// nodeAddr implements the NodeAddress interface required by gorums.WithNodes.
type nodeAddr struct {
	addr string
}

func (n nodeAddr) Addr() string {
	return n.addr
}
