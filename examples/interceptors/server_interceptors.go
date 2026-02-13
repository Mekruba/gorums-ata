package interceptors

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/relab/gorums"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

func LoggingInterceptor(addr string) gorums.Interceptor {
	return func(ctx gorums.ServerCtx, in *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
		req := gorums.AsProto[proto.Message](in)
		log.Printf("[%s]: LoggingInterceptor(incoming): Method=%s, Message=%s", addr, in.GetMethod(), req)
		start := time.Now()
		out, err := next(ctx, in)

		duration := time.Since(start)
		resp := gorums.AsProto[proto.Message](out)
		log.Printf("[%s]: LoggingInterceptor(outgoing): Method=%s, Duration=%s, Err=%v, Message=%v, Type=%T", addr, in.GetMethod(), duration, err, resp, resp)
		return out, err
	}
}

func LoggingSimpleInterceptor(ctx gorums.ServerCtx, in *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
	req := gorums.AsProto[proto.Message](in)
	log.Printf("LoggingSimpleInterceptor(incoming): Method=%s, Message=%v)", in.GetMethod(), req)
	out, err := next(ctx, in)
	resp := gorums.AsProto[proto.Message](out)
	log.Printf("LoggingSimpleInterceptor(outgoing): Method=%s, Err=%v, Message=%v", in.GetMethod(), err, resp)
	return out, err
}

func DelayedInterceptor(ctx gorums.ServerCtx, in *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
	// delay based on sending node address
	delay := 0 * time.Millisecond
	peer, ok := peer.FromContext(ctx)
	if ok && peer.Addr != nil {
		node := peer.Addr.String()
		log.Printf("DelayedInterceptor: Received message from node %v", peer)
		// Example: delay based on node address length
		delay = time.Duration(len(node)) * 100 * time.Millisecond
		log.Printf("DelayedInterceptor: Delaying message processing for %s based on node address length", delay)
	} else {
		log.Printf("DelayedInterceptor: No peer address found in context, using default delay of 0ms")
	}

	time.Sleep(delay)
	// Call the next handler in the chain
	out, err := next(ctx, in)
	log.Printf("DelayedInterceptor: Finished processing message after %s", delay)
	return out, err
}

/** NoFooAllowedInterceptor rejects requests for messages with key "foo". */
func NoFooAllowedInterceptor[T interface{ GetKey() string }](ctx gorums.ServerCtx, in *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
	if req, ok := gorums.AsProto[proto.Message](in).(T); ok {
		log.Printf("NoFooAllowedInterceptor: Received request for key '%s'", req.GetKey())
		if req.GetKey() == "foo" {
			log.Printf("NoFooAllowedInterceptor: Rejecting request for key 'foo'")
			return nil, fmt.Errorf("requests for key 'foo' are not allowed")
		}
	}
	return next(ctx, in)
}

func MetadataInterceptor(ctx gorums.ServerCtx, in *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
	log.Printf("MetadataInterceptor: Adding custom metadata to message(customKey=customValue)")
	// Add a custom metadata field
	entry := gorums.MetadataEntry_builder{
		Key:   "customKey",
		Value: "customValue",
	}.Build()
	in.SetEntry([]*gorums.MetadataEntry{
		entry,
	})
	// Call the next handler in the chain
	out, err := next(ctx, in)
	log.Printf("MetadataInterceptor: Finished processing message with custom metadata")
	return out, err
}

// NewBroadcastInterceptor creates an interceptor that broadcasts incoming requests to all nodes
// in the provided configuration. It uses message ID tracking to prevent broadcast loops -
// each unique message (identified by its message ID) is only broadcasted once per server.
//
// Parameters:
//   - cfg: Configuration containing the nodes to broadcast to
//   - method: The RPC method name to invoke on other nodes (e.g., "proto.Storage.WriteRPC")
//
// The interceptor will:
//  1. Check if this message ID has already been broadcasted (loop prevention)
//  2. Process the request locally by calling next()
//  3. Broadcast the request to all nodes in the configuration (fire-and-forget)
//  4. Return the local response
//
// Loop Prevention:
// When a client sends a write request, it gets a unique message ID. When server 0 receives
// it and broadcasts to servers 1, 2, 3, they all receive the SAME message ID. Each server
// tracks which message IDs it has already broadcast, preventing infinite loops.
//
// Example usage:
//
//	// In server setup, create a client manager and configuration
//	clientMgr := proto.NewManager(gorums.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
//	clientCfg, _ := proto.NewConfiguration(clientMgr, gorums.WithNodeList(otherNodeAddresses))
//
//	// Create server with broadcast interceptor
//	srv := gorums.NewServer(gorums.WithInterceptors(
//	    interceptors.NewBroadcastInterceptor(clientCfg, "proto.Storage.WriteRPC"),
//	))
func NewBroadcastInterceptor(cfg gorums.Configuration, method string) gorums.Interceptor {
	// Cache of message content hashes we've already broadcast (to prevent loops)
	// We use content hash instead of message seq number because each RPCCall creates a new seq number
	var mu sync.Mutex
	broadcastedHashes := make(map[string]struct{})

	return func(ctx gorums.ServerCtx, msg *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
		// Only broadcast for the specified method
		if msg.GetMethod() != method {
			return next(ctx, msg)
		}

		// Create a unique hash from the message content
		// This ensures we detect duplicates even when sequence numbers differ
		msgBytes, err := proto.Marshal(msg.GetProtoMessage())
		if err != nil {
			log.Printf("BroadcastInterceptor: Failed to marshal message: %v", err)
			return next(ctx, msg)
		}
		hash := sha256.Sum256(msgBytes)
		hashStr := fmt.Sprintf("%x", hash[:8]) // Use first 8 bytes for efficiency

		// Check if we've already broadcast this exact message content
		mu.Lock()
		_, alreadyBroadcasted := broadcastedHashes[hashStr]
		if !alreadyBroadcasted {
			broadcastedHashes[hashStr] = struct{}{}
			// Limit cache size to prevent memory leak
			if len(broadcastedHashes) > 10000 {
				// Clear half the cache
				count := 0
				for h := range broadcastedHashes {
					delete(broadcastedHashes, h)
					count++
					if count >= 5000 {
						break
					}
				}
			}
		}
		mu.Unlock()

		if alreadyBroadcasted {
			// Silently skip re-broadcast
			return next(ctx, msg)
		}

		// Process locally first
		resp, err := next(ctx, msg)

		// Broadcast to all nodes asynchronously (fire-and-forget)
		go func() {
			broadcastCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Clone the message to avoid data races
			msgCopy := proto.Clone(msg.GetProtoMessage())

			// Broadcast to all nodes
			var wg sync.WaitGroup
			for _, node := range cfg.Nodes() {
				wg.Add(1)
				go func(n *gorums.Node) {
					defer wg.Done()
					nodeCtx := n.Context(broadcastCtx)

					// Send the message using gorums.RPCCall
					_, _ = gorums.RPCCall(nodeCtx, msgCopy, method)
					// Silently ignore errors
				}(node)
			}

			wg.Wait()
			// Broadcast complete (no logging)
		}()

		return resp, err
	}
}

// SelectiveBroadcastInterceptor creates an interceptor that conditionally broadcasts based
// on a predicate function. This allows fine-grained control over which requests get broadcasted.
//
// Parameters:
//   - cfg: Configuration containing the nodes to broadcast to
//   - method: The RPC method name to invoke on other nodes
//   - shouldBroadcast: Function that receives the message and returns true if it should be broadcasted
//
// Example:
//
//	// Only broadcast writes to keys starting with "replicate:"
//	interceptor := interceptors.NewSelectiveBroadcastInterceptor(
//	    clientCfg,
//	    "proto.Storage.WriteRPC",
//	    func(msg proto.Message) bool {
//	        if req, ok := msg.(*proto.WriteRequest); ok {
//	            return strings.HasPrefix(req.GetKey(), "replicate:")
//	        }
//	        return false
//	    },
//	)
func NewSelectiveBroadcastInterceptor(cfg gorums.Configuration, method string, shouldBroadcast func(proto.Message) bool) gorums.Interceptor {
	return func(ctx gorums.ServerCtx, msg *gorums.Message, next gorums.Handler) (*gorums.Message, error) {
		// Check if we should broadcast this message
		if msg.GetMethod() != method || !shouldBroadcast(msg.GetProtoMessage()) {
			return next(ctx, msg)
		}

		log.Printf("SelectiveBroadcastInterceptor: Broadcasting %s to %d nodes", method, cfg.Size())

		// Process locally first
		resp, err := next(ctx, msg)

		// Broadcast to all nodes asynchronously
		go func() {
			broadcastCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			msgCopy := proto.Clone(msg.GetProtoMessage())

			var wg sync.WaitGroup
			for _, node := range cfg.Nodes() {
				wg.Add(1)
				go func(n *gorums.Node) {
					defer wg.Done()
					nodeCtx := n.Context(broadcastCtx)
					_, callErr := gorums.RPCCall(nodeCtx, msgCopy, method)
					if callErr != nil {
						log.Printf("SelectiveBroadcastInterceptor: Failed to broadcast to %s: %v", n.Address(), callErr)
					} else {
						log.Printf("SelectiveBroadcastInterceptor: Broadcasted to %s", n.Address())
					}
				}(node)
			}
			wg.Wait()
		}()

		return resp, err
	}
}
