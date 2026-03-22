// Simplex consensus demo.
//
// Starts a local cluster of n Simplex nodes and drives several consensus
// iterations, printing which transactions are finalized at each height.
//
// Usage:
//
//	simplex [-n 4] [-iterations 5]
package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/simplex/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	n := flag.Int("n", 4, "Number of consensus nodes (must be ≥ 4 for f=1 Byzantine tolerance).")
	iterations := flag.Int("iterations", 5, "Number of consensus iterations to run.")
	verbose := flag.Bool("v", false, "Enable verbose debug logging.")
	flag.Parse()

	if *n < 4 {
		log.Fatal("cluster size must be at least 4 (requires f < n/3 ≥ 1)")
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: level})))

	fmt.Printf("Starting Simplex consensus with %d nodes, running %d iterations.\n", *n, *iterations)
	fmt.Printf("Quorum threshold: %d votes (⌈2n/3⌉).\n\n", quorum(*n))

	fmt.Printf("Pre-loading %d transactions (one per iteration) into leader queues.\n\n", *iterations)
	for iter := 1; iter <= *iterations; iter++ {
		leader := leaderForHeight(uint64(iter), *n)
		fmt.Printf("  iteration %d: leader=node%d\n", iter, leader)
	}
	fmt.Println()

	// Create n local Gorums systems.  NewLocalSystems assigns node IDs 1..n,
	// allocates listeners on random ports, and sets up outbound configs.
	dialOpts := gorums.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))
	systems, stop, err := gorums.NewLocalSystems(*n, dialOpts)
	if err != nil {
		log.Fatalf("NewLocalSystems: %v", err)
	}
	defer stop()

	// Create one Simplex node per Gorums system and register the service.
	nodes := make([]*Node, *n)
	for i, sys := range systems {
		id := uint32(i + 1)
		outCfg := sys.OutboundConfig()
		onFinalize := makeOnFinalize(id)
		node := NewNode(id, *n, outCfg, onFinalize)
		nodes[i] = node
		sys.RegisterService(nil, func(srv *gorums.Server) {
			pb.RegisterSimplexNodeServer(srv, node)
		})
		go func() {
			if err := sys.Serve(); err != nil {
				slog.Debug("serve stopped", "node", id, "err", err)
			}
		}()
	}

	// Brief pause to let all gRPC connections be established.
	time.Sleep(200 * time.Millisecond)

	// Pre-load one transaction per planned iteration into the current leader's
	// queue.  We inject them before starting so no transactions are missed due
	// to timing between Start() and the first proposal.
	for iter := 1; iter <= *iterations; iter++ {
		leader := leaderForHeight(uint64(iter), *n)
		nodes[leader-1].AddTx(fmt.Sprintf("tx-iter%d-leader%d", iter, leader))
	}

	// Start the protocol on all nodes.
	for _, nd := range nodes {
		nd.Start()
	}

	// Run for enough time to finalize all iterations.
	// Each honest iteration takes at most 2Δ; the final sleep covers stragglers.
	runTime := time.Duration(*iterations+2) * 4 * delta
	time.Sleep(runTime)

	// Stop all nodes before printing results.
	for _, nd := range nodes {
		nd.Stop()
	}

	// Print the finalized transaction log from every node so results can be compared.
	fmt.Println("\n── Finalized transaction log ───────────────────────────────────────")
	for _, nd := range nodes {
		log_ := nd.FinalizedLog()
		fmt.Printf("node %d (%d tx): %v\n", nd.ID(), len(log_), log_)
	}
}

// makeOnFinalize returns a callback that logs when transactions are finalized.
func makeOnFinalize(id uint32) func(uint64, []string) {
	return func(height uint64, txs []string) {
		slog.Info("finalized",
			"node", id,
			"height", height,
			"txs", txs,
		)
	}
}
