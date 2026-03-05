package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/broadcast/proto"
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

	// Create manager to connect to other nodes
	mgr := pb.NewManager(
		gorums.WithDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		),
	)

	// Build map of peers (all nodes except self)
	peerMap := make(map[uint32]nodeAddr)
	for _, node := range nodes {
		if node.id != id {
			peerMap[node.id] = nodeAddr{addr: node.addr}
		}
	}

	// Create configuration with peers
	var config gorums.Configuration
	var err error
	if len(peerMap) > 0 {
		config, err = gorums.NewConfiguration(mgr, gorums.WithNodes(peerMap))
		if err != nil {
			log.Fatalf("Failed to create configuration: %v", err)
		}
	}

	// Create broadcast server
	broadcastSrv := NewBroadcastServer(id, config)

	// Create Gorums server with configuration
	srv := gorums.NewServer(gorums.WithConfiguration(&config), gorums.WithInterceptors())

	// Register service
	pb.RegisterBroadcastServer(srv, broadcastSrv)

	// Start listening
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	log.Printf("Node %d starting on %s", id, addr)
	log.Printf("Connected to %d peers", len(peerMap))

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

// BroadcastServer implements broadcast with response collection.
type BroadcastServer struct {
	id       uint32
	messages map[string]bool
	mu       sync.Mutex
	config   gorums.Configuration
}

func NewBroadcastServer(id uint32, config gorums.Configuration) *BroadcastServer {
	return &BroadcastServer{
		id:       id,
		messages: make(map[string]bool),
		config:   config,
	}
}

// Broadcast handles one-way broadcast (no response).
func (b *BroadcastServer) Broadcast(ctx gorums.ServerCtx, msg *pb.BroadcastMsg) {
	b.mu.Lock()
	msgKey := fmt.Sprintf("%d:%s", msg.GetSender(), msg.GetData())

	if b.messages[msgKey] {
		b.mu.Unlock()
		fmt.Printf("Node %d: Already delivered message from %d\n", b.id, msg.GetSender())
		return
	}

	b.messages[msgKey] = true
	b.mu.Unlock()

	fmt.Printf("Node %d: ✓ Delivered message from %d: '%s'\n", b.id, msg.GetSender(), msg.GetData())

	// Forward to all other nodes
	config := ctx.Config()
	if config != nil {
		for _, node := range config {
			if node.ID() == b.id {
				continue
			}
			go func(n *gorums.Node) {
				nodeCtx := n.Context(context.Background())
				_ = pb.Broadcast(nodeCtx, msg, gorums.IgnoreErrors())
			}(node)
		}
		fmt.Printf("Node %d: → Forwarded to all other nodes\n", b.id)
	}
}

// BroadcastQC handles broadcast with acknowledgment (quorum call).
func (b *BroadcastServer) BroadcastQC(ctx gorums.ServerCtx, msg *pb.BroadcastMsg) (*pb.BroadcastAck, error) {
	b.mu.Lock()
	msgKey := fmt.Sprintf("%d:%s", msg.GetSender(), msg.GetData())

	// Check if already delivered
	alreadyDelivered := b.messages[msgKey]
	if !alreadyDelivered {
		b.messages[msgKey] = true
	}
	b.mu.Unlock()

	if alreadyDelivered {
		fmt.Printf("Node %d: Already delivered message from %d\n", b.id, msg.GetSender())
		// Return ack but mark as not newly delivered
		return pb.BroadcastAck_builder{
			NodeId:      b.id,
			Delivered:   false,
			ForwardedTo: 0,
		}.Build(), nil
	}

	fmt.Printf("Node %d: ✓ Delivered message from %d: '%s'\n", b.id, msg.GetSender(), msg.GetData())

	// Forward to all other nodes using the server-side configuration
	var forwardedCount uint32
	config := ctx.Config()
	if config != nil {
		for _, node := range config {
			if node.ID() == b.id {
				continue
			}
			go func(n *gorums.Node) {
				nodeCtx := n.Context(context.Background())
				_ = pb.Broadcast(nodeCtx, msg, gorums.IgnoreErrors())
			}(node)
			forwardedCount++
		}
		fmt.Printf("Node %d: → Forwarded to %d other nodes\n", b.id, forwardedCount)
	}

	// Return acknowledgment
	return pb.BroadcastAck_builder{
		NodeId:      b.id,
		Delivered:   true,
		ForwardedTo: forwardedCount,
	}.Build(), nil
}

func (b *BroadcastServer) GetMessageCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.messages)
}
