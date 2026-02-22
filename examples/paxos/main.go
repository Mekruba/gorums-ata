package main

import (
	"flag"
	"log"
)

// Fixed list of nodes (acceptors/learners)
var nodes = []struct {
	id   uint32
	addr string
}{
	{1, "localhost:9091"},
	{2, "localhost:9092"},
	{3, "localhost:9093"},
}

func main() {
	var (
		nodeID     = flag.Uint("id", 0, "Node ID (1, 2, or 3 for server/acceptor)")
		isProposer = flag.Bool("propose", false, "Run as proposer instead of server")
		value      = flag.String("value", "default-value", "Value to propose (proposer mode)")
	)
	flag.Parse()

	if *isProposer {
		if *nodeID == 0 {
			*nodeID = 100 // Default proposer ID
		}
		runProposer(uint32(*nodeID), *value)
	} else {
		if *nodeID == 0 || *nodeID > 3 {
			log.Fatal("Server mode requires -id flag (1, 2, or 3)")
		}
		runServer(uint32(*nodeID))
	}
}
