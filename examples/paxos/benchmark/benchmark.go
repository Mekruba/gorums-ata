package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/relab/gorums"
	pb "github.com/relab/gorums/examples/paxos/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// RunOptions holds configuration for a benchmark run.
type RunOptions struct {
	NumClients          int
	NumRequests         int // per client (sync/async) or target reqs/s (throughput)
	ThroughputMax       int // max target reqs/s for sweep
	ThroughputIncrement int // step size for throughput sweep
	Dur                 int // seconds per throughput step
	Runs                int
}

// requestResult captures the timing of a single Paxos proposal.
type requestResult struct {
	err   error
	start time.Time
	end   time.Time
}

// clientResult holds aggregated metrics for one benchmark run.
type clientResult struct {
	Total         int
	Successes     int
	Failures      int
	LatencyAvg    time.Duration
	LatencyMedian time.Duration
	LatencyMin    time.Duration
	LatencyMax    time.Duration
	LatencyStdev  float64
	Throughput    float64 // reqs/s
	Durations     []time.Duration
}

// PaxosBenchmark manages server lifecycle and client connections for benchmarking.
type PaxosBenchmark struct {
	srvAddrs []string
	csvDir   string
	opts     RunOptions

	systems  []*gorums.System
	servers  []*PaxosServer
	managers []*pb.Manager

	// Clients (proposers) — each has its own manager + config
	clientCfgs []*proposerClient
}

// proposerClient is one benchmark client: a Proposer with its own Manager.
type proposerClient struct {
	id  uint32
	mgr *pb.Manager
	cfg pb.Configuration
	p   *Proposer
}

func NewPaxosBenchmark(srvAddrs []string, csvDir string) *PaxosBenchmark {
	return &PaxosBenchmark{
		srvAddrs: srvAddrs,
		csvDir:   csvDir,
	}
}

// Init starts the local servers and connects all clients.
func (b *PaxosBenchmark) Init(opts RunOptions) {
	b.opts = opts
	b.startServers()
	b.connectClients()
}

// startServers starts one in-process Paxos server per address.
func (b *PaxosBenchmark) startServers() {
	b.systems = make([]*gorums.System, len(b.srvAddrs))
	b.servers = make([]*PaxosServer, len(b.srvAddrs))
	b.managers = make([]*pb.Manager, len(b.srvAddrs))

	// Build node map for all servers
	nodeMap := make(map[uint32]nodeAddr)
	for i, addr := range b.srvAddrs {
		nodeMap[uint32(i+1)] = nodeAddr{addr: addr}
	}

	fmt.Printf("Starting %d Paxos servers locally...\n", len(b.srvAddrs))
	for i, addr := range b.srvAddrs {
		id := uint32(i + 1)
		sys, err := gorums.NewSystem(addr,
			gorums.WithConfig(id, gorums.WithNodeList(b.srvAddrs)),
			gorums.WithGRPCServerOptions(
				grpc.Creds(insecure.NewCredentials()),
			),
		)
		if err != nil {
			panic(fmt.Sprintf("Failed to start server %d at %s: %v", id, addr, err))
		}

		paxosSrv := NewPaxosServer(id)
		mgr := pb.NewManager(
			gorums.WithDialOptions(
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			),
		)

		sys.RegisterService(mgr, func(srv *gorums.Server) {
			pb.RegisterPaxosServer(srv, paxosSrv)
		})

		go func(sys *gorums.System) {
			if err := sys.Serve(); err != nil {
				// Server stopped — expected on shutdown
			}
		}(sys)

		b.systems[i] = sys
		b.servers[i] = paxosSrv
		b.managers[i] = mgr
	}

	// Wait for all servers to be ready
	time.Sleep(300 * time.Millisecond)

	// Wire each server's outbound proposer config
	for i, sys := range b.systems {
		id := uint32(i + 1)
		proposerCfg, err := sys.NewOutboundConfig(
			gorums.WithNodes(nodeMap),
			gorums.WithDialOptions(
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			),
		)
		if err != nil {
			panic(fmt.Sprintf("Server %d: failed to create outbound config: %v", id, err))
		}
		b.servers[i].setProposerConfig(proposerCfg)
	}

	// Allow bidirectional connections to settle
	time.Sleep(500 * time.Millisecond)
	fmt.Printf("Servers ready. Quorum size: %d\n", len(b.srvAddrs)/2+1)
}

// connectClients creates NumClients proposers, each with its own connection pool.
func (b *PaxosBenchmark) connectClients() {
	n := b.opts.NumClients
	b.clientCfgs = make([]*proposerClient, n)

	fmt.Printf("Connecting %d proposer client(s)...\n", n)
	for i := 0; i < n; i++ {
		id := uint32(1000 + i)
		mgr := pb.NewManager(
			gorums.WithDialOptions(
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			),
		)
		cfg, err := pb.NewConfiguration(mgr, gorums.WithNodeList(b.srvAddrs))
		if err != nil {
			panic(fmt.Sprintf("Client %d: failed to create configuration: %v", id, err))
		}
		b.clientCfgs[i] = &proposerClient{
			id:  id,
			mgr: mgr,
			cfg: cfg,
			p:   NewProposer(id, cfg),
		}
	}

	// Warmup: one proposal per client to establish leadership
	fmt.Println("Warming up...")
	var wg sync.WaitGroup
	for _, c := range b.clientCfgs {
		wg.Add(1)
		go func(c *proposerClient) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = ctx
			_ = c.p.Propose("warmup")
		}(c)
	}
	wg.Wait()
	fmt.Println("Warmup complete.")

	// Reset proposer state after warmup so instance numbering starts fresh
	for _, c := range b.clientCfgs {
		c.p = NewProposer(c.id, c.cfg)
	}
}

// stopClients closes all client managers.
func (b *PaxosBenchmark) stopClients() {
	for _, c := range b.clientCfgs {
		c.mgr.Close()
	}
}

// stopServers stops all local servers.
func (b *PaxosBenchmark) stopServers() {
	for _, sys := range b.systems {
		sys.Stop()
	}
}

// resetProposers resets all proposers and server state before a fresh run.
// Each proposer gets a disjoint set of instance numbers (stride = numClients)
// so concurrent proposers never compete for the same Paxos instance.
func (b *PaxosBenchmark) resetProposers() {
	// Clear server state so proposal numbers from previous runs don't block new ones.
	for _, srv := range b.servers {
		srv.Reset()
	}
	n := uint32(len(b.clientCfgs))
	for i, c := range b.clientCfgs {
		c.p = NewProposerWithOffset(c.id, c.cfg, uint32(i)+1, n)
	}
}

// ─────────────────────────── Run modes ───────────────────────────

// RunSync sends numRequests proposals sequentially per client (one at a time).
func (b *PaxosBenchmark) RunSync(runNum int) error {
	b.resetProposers()
	name := fmt.Sprintf("Paxos.C%d.R%d.Sync", b.opts.NumClients, runNum)
	total := b.opts.NumClients * b.opts.NumRequests
	resChan := make(chan requestResult, total)

	var wg sync.WaitGroup
	for _, c := range b.clientCfgs {
		wg.Add(1)
		go func(c *proposerClient) {
			defer wg.Done()
			for i := 0; i < b.opts.NumRequests; i++ {
				start := time.Now()
				err := c.p.Propose(fmt.Sprintf("v%d", i))
				end := time.Now()
				resChan <- requestResult{err: err, start: start, end: end}
			}
		}(c)
	}

	go func() { wg.Wait(); close(resChan) }()
	return b.collectAndWrite(name, resChan, total)
}

// RunAsync fires all requests simultaneously from all clients.
func (b *PaxosBenchmark) RunAsync(runNum int) error {
	b.resetProposers()
	name := fmt.Sprintf("Paxos.C%d.R%d.Async", b.opts.NumClients, runNum)
	total := b.opts.NumClients * b.opts.NumRequests
	resChan := make(chan requestResult, total)

	var wg sync.WaitGroup
	for _, c := range b.clientCfgs {
		for i := 0; i < b.opts.NumRequests; i++ {
			wg.Add(1)
			go func(c *proposerClient, val int) {
				defer wg.Done()
				start := time.Now()
				err := c.p.Propose(fmt.Sprintf("v%d", val))
				end := time.Now()
				resChan <- requestResult{err: err, start: start, end: end}
			}(c, i)
		}
	}

	go func() { wg.Wait(); close(resChan) }()
	return b.collectAndWrite(name, resChan, total)
}

// RunPerformance sends 1000 requests sequentially and reports latency statistics.
func (b *PaxosBenchmark) RunPerformance(runNum int) error {
	b.resetProposers()
	perClient := 1000 / b.opts.NumClients
	if perClient < 1 {
		perClient = 1
	}
	total := perClient * b.opts.NumClients
	name := fmt.Sprintf("Paxos.C%d.Reqs%d.R%d.Performance", b.opts.NumClients, total, runNum)
	resChan := make(chan requestResult, total)

	var wg sync.WaitGroup
	for _, c := range b.clientCfgs {
		wg.Add(1)
		go func(c *proposerClient) {
			defer wg.Done()
			for i := 0; i < perClient; i++ {
				start := time.Now()
				err := c.p.Propose(fmt.Sprintf("v%d", i))
				end := time.Now()
				resChan <- requestResult{err: err, start: start, end: end}
			}
		}(c)
	}

	go func() { wg.Wait(); close(resChan) }()

	result, err := b.collectResults(resChan, total)
	if err != nil {
		return err
	}

	printResult(name, result)
	return writePerformanceCSV(b.csvDir, name, result, total/b.opts.NumClients)
}

// RunThroughputVsLatency sweeps target throughput from increment to max,
// running each for dur seconds. Writes CSV files.
func (b *PaxosBenchmark) RunThroughputVsLatency(runNum int) error {
	incr := b.opts.ThroughputIncrement
	if incr <= 0 {
		incr = b.opts.ThroughputMax / 10
	}

	summaryRows := make([][]string, 0)

	for target := incr; target <= b.opts.ThroughputMax; target += incr {
		b.resetProposers()
		name := fmt.Sprintf("Paxos.C%d.T%d.R%d.Throughput", b.opts.NumClients, target, runNum)
		fmt.Printf("\n--- Target: %d reqs/s for %ds ---\n", target, b.opts.Dur)

		result, err := b.runThroughputStep(target, b.opts.Dur)
		if err != nil {
			fmt.Printf("Step %d failed: %v\n", target, err)
			continue
		}

		printResult(name, result)
		summaryRows = append(summaryRows, []string{
			strconv.Itoa(int(result.Throughput)),
			strconv.Itoa(int(result.LatencyAvg.Microseconds())),
			strconv.Itoa(int(result.LatencyMedian.Microseconds())),
		})

		if err := writeDurationsCSV(b.csvDir, name, result.Durations); err != nil {
			fmt.Printf("Warning: failed to write durations CSV: %v\n", err)
		}
		time.Sleep(1 * time.Second)
	}

	summaryName := fmt.Sprintf("Paxos.C%d.R%d", b.opts.NumClients, runNum)
	return writeThroughputVsLatencyCSV(b.csvDir, summaryName, summaryRows)
}

// runThroughputStep sends `target` reqs/s for `dur` seconds using all clients.
func (b *PaxosBenchmark) runThroughputStep(target, dur int) (clientResult, error) {
	numClients := b.opts.NumClients
	reqsPerClientPerSec := target / numClients
	if reqsPerClientPerSec < 1 {
		reqsPerClientPerSec = 1
	}
	totalReqs := reqsPerClientPerSec * numClients * dur
	resChan := make(chan requestResult, totalReqs+numClients*dur)

	var wg sync.WaitGroup

	for t := 0; t < dur; t++ {
		for _, c := range b.clientCfgs {
			wg.Add(1)
			go func(c *proposerClient, sec int) {
				defer wg.Done()
				for i := 0; i < reqsPerClientPerSec; i++ {
					start := time.Now()
					err := c.p.Propose(fmt.Sprintf("v%d-%d", sec, i))
					end := time.Now()
					resChan <- requestResult{err: err, start: start, end: end}
				}
			}(c, t)
		}
		time.Sleep(1 * time.Second)
	}

	go func() { wg.Wait(); close(resChan) }()
	return b.collectResults(resChan, totalReqs)
}

// ─────────────────────────── Result collection ───────────────────────────

// collectAndWrite collects results into a clientResult and writes CSVs.
func (b *PaxosBenchmark) collectAndWrite(name string, resChan <-chan requestResult, total int) error {
	result, err := b.collectResults(resChan, total)
	if err != nil {
		return err
	}
	printResult(name, result)
	if err := writeDurationsCSV(b.csvDir, name, result.Durations); err != nil {
		return err
	}
	return writePerformanceCSV(b.csvDir, name, result, b.opts.NumRequests)
}

// collectResults drains resChan until closed or total reached, then computes stats.
// Wall-clock throughput = successes / (last_end - first_start).
func (b *PaxosBenchmark) collectResults(resChan <-chan requestResult, total int) (clientResult, error) {
	runtime.GC()
	durations := make([]time.Duration, 0, total)
	failures := 0
	graceTimeout := 30 * time.Second

	var wallStart, wallEnd time.Time

	for i := 0; i < total; i++ {
		var res requestResult
		var ok bool
		select {
		case res, ok = <-resChan:
			if !ok {
				failures += total - i
				goto done
			}
		case <-time.After(graceTimeout):
			failures += total - i
			goto done
		}
		if res.err != nil {
			failures++
			continue
		}
		dur := res.end.Sub(res.start)
		durations = append(durations, dur)
		if wallStart.IsZero() || res.start.Before(wallStart) {
			wallStart = res.start
		}
		if res.end.After(wallEnd) {
			wallEnd = res.end
		}
	}

done:
	successes := len(durations)
	if successes == 0 {
		return clientResult{Total: total, Failures: failures}, fmt.Errorf("all %d requests failed", total)
	}

	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.Sort(sorted)

	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	avg := sum / time.Duration(successes)

	var median time.Duration
	n := len(sorted)
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	} else {
		median = sorted[n/2]
	}

	var variance float64
	for _, d := range durations {
		diff := float64(d-avg) / float64(time.Microsecond)
		variance += diff * diff
	}
	stdev := math.Sqrt(variance / float64(successes))

	wallDur := wallEnd.Sub(wallStart)
	var throughput float64
	if wallDur > 0 {
		throughput = float64(successes) / wallDur.Seconds()
	}

	return clientResult{
		Total:         total,
		Successes:     successes,
		Failures:      failures,
		LatencyAvg:    avg,
		LatencyMedian: median,
		LatencyMin:    sorted[0],
		LatencyMax:    sorted[n-1],
		LatencyStdev:  stdev,
		Throughput:    throughput,
		Durations:     durations,
	}, nil
}

func printResult(name string, r clientResult) {
	fmt.Printf("\n[%s]\n", name)
	fmt.Printf("  Total:     %d  Successes: %d  Failures: %d\n", r.Total, r.Successes, r.Failures)
	fmt.Printf("  Latency:   avg=%-10s  median=%-10s  min=%-10s  max=%s\n",
		r.LatencyAvg.Round(time.Microsecond),
		r.LatencyMedian.Round(time.Microsecond),
		r.LatencyMin.Round(time.Microsecond),
		r.LatencyMax.Round(time.Microsecond))
	fmt.Printf("  Stdev:     %.2f µs\n", r.LatencyStdev)
	fmt.Printf("  Throughput:%.0f reqs/s\n", r.Throughput)
}

// ─────────────────────────── CSV helpers ───────────────────────────

func writeThroughputVsLatencyCSV(dir, name string, rows [][]string) error {
	path := fmt.Sprintf("%s/%s.csv", dir, name)
	fmt.Printf("Writing throughput-vs-latency CSV: %s\n", path)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	data := append([][]string{{"Throughput", "Latency avg (µs)", "Latency median (µs)"}}, rows...)
	return w.WriteAll(data)
}

func writeDurationsCSV(dir, name string, durations []time.Duration) error {
	path := fmt.Sprintf("%s/%s.durations.csv", dir, name)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	data := make([][]string, 1, len(durations)+1)
	data[0] = []string{"Number", "Latency (µs)"}
	for i, d := range durations {
		data = append(data, []string{strconv.Itoa(i), strconv.Itoa(int(d.Microseconds()))})
	}
	return w.WriteAll(data)
}

func writePerformanceCSV(dir, name string, r clientResult, reqsPerClient int) error {
	path := fmt.Sprintf("%s/%s.performance.csv", dir, name)
	fmt.Printf("Writing performance CSV: %s\n", path)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	data := [][]string{
		{"Reqs/client", "Mean (µs)", "Median (µs)", "Std. dev. (µs)", "Min (µs)", "Max (µs)"},
		{
			strconv.Itoa(reqsPerClient),
			strconv.Itoa(int(r.LatencyAvg.Microseconds())),
			strconv.Itoa(int(r.LatencyMedian.Microseconds())),
			strconv.Itoa(int(r.LatencyStdev)),
			strconv.Itoa(int(r.LatencyMin.Microseconds())),
			strconv.Itoa(int(r.LatencyMax.Microseconds())),
		},
	}
	return w.WriteAll(data)
}
