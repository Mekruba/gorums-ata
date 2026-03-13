// Package main provides a benchmark for the Paxos implementation.
// It starts N server processes locally and measures latency and throughput
// of the Multi-Paxos consensus protocol, similar to the Master/src benchmark.
//
// Usage:
//
//	# Throughput vs latency sweep (default)
//	go run . -mode throughput -clients 10 -max 5000 -steps 10 -dur 10
//
//	# Single performance run (1000 requests, sequential per client)
//	go run . -mode performance -clients 5
//
//	# Quick sync run
//	go run . -mode sync -clients 1 -reqs 200
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

var serverAddrs = []string{
	"127.0.0.1:9091",
	"127.0.0.1:9092",
	"127.0.0.1:9093",
}

func main() {
	var (
		mode         = flag.String("mode", "throughput", "Benchmark mode: throughput | performance | sync | async")
		numClients   = flag.Int("clients", 4, "Number of concurrent proposer clients")
		numRequests  = flag.Int("reqs", 1000, "Requests per client (sync/async modes)")
		maxThrough   = flag.Int("max", 5000, "Max target throughput reqs/s (throughput mode)")
		steps        = flag.Int("steps", 10, "Number of throughput steps (throughput mode)")
		dur          = flag.Int("dur", 10, "Duration in seconds per throughput step")
		runs         = flag.Int("runs", 1, "Number of benchmark repetitions")
		csvDir       = flag.String("csv", "./csv", "Directory for CSV output files")
		srvAddrsFlag = flag.String("servers", "", "Comma-separated server addresses (overrides default)")
	)
	flag.Parse()

	if *srvAddrsFlag != "" {
		parts := strings.Split(*srvAddrsFlag, ",")
		serverAddrs = make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				serverAddrs = append(serverAddrs, p)
			}
		}
	}

	if err := os.MkdirAll(*csvDir, 0755); err != nil {
		log.Fatalf("Failed to create csv directory: %v", err)
	}

	bench := NewPaxosBenchmark(serverAddrs, *csvDir)

	opts := RunOptions{
		NumClients:          *numClients,
		NumRequests:         *numRequests,
		ThroughputMax:       *maxThrough,
		ThroughputIncrement: *maxThrough / *steps,
		Dur:                 *dur,
		Runs:                *runs,
	}

	bench.Init(opts)

	for run := 0; run < *runs; run++ {
		fmt.Printf("\n========== Run %d/%d ==========\n", run+1, *runs)
		switch *mode {
		case "throughput":
			if err := bench.RunThroughputVsLatency(run); err != nil {
				log.Fatalf("Benchmark failed: %v", err)
			}
		case "performance":
			if err := bench.RunPerformance(run); err != nil {
				log.Fatalf("Benchmark failed: %v", err)
			}
		case "sync":
			if err := bench.RunSync(run); err != nil {
				log.Fatalf("Benchmark failed: %v", err)
			}
		case "async":
			if err := bench.RunAsync(run); err != nil {
				log.Fatalf("Benchmark failed: %v", err)
			}
		default:
			log.Fatalf("Unknown mode: %s (use: throughput | performance | sync | async)", *mode)
		}
	}

	fmt.Println("\nBenchmark complete. Results written to:", *csvDir)
}
