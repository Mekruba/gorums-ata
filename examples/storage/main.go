package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/relab/gorums"
)

func main() {
	server := flag.String("server", "", "Start as a server on given address.")
	remotes := flag.String("connect", "", "Comma-separated list of servers to connect to.")
	broadcast := flag.Bool("broadcast", false, "Enable broadcast interceptor (replicates writes to all nodes).")
	flag.Parse()

	if *server != "" {
		runServer(*server)
		return
	}

	addrs := strings.Split(*remotes, ",")
	// start local servers if no remote servers were specified
	if len(addrs) == 1 && addrs[0] == "" {
		addrs = nil
		srvs := make([]*gorums.Server, 0, 4)

		if *broadcast {
			// Broadcast mode requires a multi-phase startup:
			// 1. Allocate ports for all servers
			// 2. Start all servers with their broadcast configurations
			// 3. All servers are up before any broadcasting happens

			log.Println("Broadcast mode: allocating server addresses...")
			listeners := make([]string, 4)
			for i := range 4 {
				// Use fixed ports for broadcast mode to ensure consistent addressing
				listeners[i] = fmt.Sprintf("127.0.0.1:%d", 50000+i)
			}

			log.Println("Starting servers with broadcast configuration...")
			for i, addr := range listeners {
				// Each server broadcasts to all OTHER servers
				otherNodes := make([]string, 0, len(listeners)-1)
				for j, otherAddr := range listeners {
					if i != j {
						otherNodes = append(otherNodes, otherAddr)
					}
				}
				srv, realAddr := startServerWithBroadcast(addr, otherNodes)
				srvs = append(srvs, srv)
				addrs = append(addrs, realAddr)
				log.Printf("Started server %d on %s with broadcast to %d nodes\n", i, realAddr, len(otherNodes))
			}
			log.Println("All servers running - broadcasts will succeed")
		} else {
			// Normal mode: just start servers
			for range 4 {
				srv, addr := startServer("127.0.0.1:0")
				srvs = append(srvs, srv)
				addrs = append(addrs, addr)
				log.Printf("Started storage server on %s\n", addr)
			}
		}

		defer func() {
			for _, srv := range srvs {
				srv.Stop()
			}
		}()
	}

	if runClient(addrs) != nil {
		os.Exit(1)
	}
}
