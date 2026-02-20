package main

import (
	"context"
	"log"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/broadcast/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func runClient(message string, senderID uint32) {
	if senderID == 0 {
		senderID = 99
	}

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

	log.Printf("Client sending to %d nodes", cfg.Size())

	// Create broadcast message
	msg := pb.BroadcastMsg_builder{
		Data:   message,
		Sender: senderID,
	}.Build()

	// Use quorum call to collect responses
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfgCtx := cfg.Context(ctx)
	responses := pb.BroadcastQC(cfgCtx, msg)

	log.Printf("Sending broadcast: '%s'", message)

	// Collect all responses
	var delivered, duplicates int
	var totalForwarded uint32

	for resp := range responses.Seq() {
		if resp.Err != nil {
			log.Printf("Error from node %d: %v", resp.NodeID, resp.Err)
			continue
		}

		ack := resp.Value
		log.Printf("Response from node %d: delivered=%v, forwarded_to=%d",
			ack.GetNodeId(), ack.GetDelivered(), ack.GetForwardedTo())

		if ack.GetDelivered() {
			delivered++
		} else {
			duplicates++
		}
		totalForwarded += ack.GetForwardedTo()
	}

	log.Printf("\n=== Broadcast Summary ===")
	log.Printf("Nodes delivered: %d", delivered)
	log.Printf("Duplicates: %d", duplicates)
	log.Printf("Total forwards: %d", totalForwarded)
	log.Printf("========================")
}
