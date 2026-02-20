package main

import (
	"flag"
	"log"
)

// Fixed list of nodes
var nodes = []struct {
	id   uint32
	addr string
}{
	{1, "localhost:8081"},
	{2, "localhost:8082"},
	{3, "localhost:8083"},
}

func main() {
	var (
		nodeID   = flag.Uint("id", 0, "Node ID (1, 2, or 3 for server)")
		isClient = flag.Bool("client", false, "Run as client instead of server")
		message  = flag.String("msg", "Hello World", "Message to broadcast (client mode)")
	)
	flag.Parse()

	if *isClient {
		runClient(*message, uint32(*nodeID))
	} else {
		if *nodeID == 0 || *nodeID > 3 {
			log.Fatal("Server mode requires -id flag (1, 2, or 3)")
		}
		runServer(uint32(*nodeID))
	}
}
