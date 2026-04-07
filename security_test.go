package gorums

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	pb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/relab/gorums/internal/stream"
)

// -----------------------------------------------------------------------------
// Test helpers
// -----------------------------------------------------------------------------

// testSignable wraps a *pb.StringValue to implement the Signable interface.
// The extra signerID and sig fields are not marshaled by proto (they are stored
// outside the proto message), which is intentional: these tests exercise the
// interceptor logic independently of the marshaling layer.
type testSignable struct {
	*pb.StringValue
	signerID uint32
	sig      []byte
}

func newTestSignable(value string, signerID uint32) *testSignable {
	return &testSignable{StringValue: pb.String(value), signerID: signerID}
}

func (t *testSignable) GetSignerID() uint32  { return t.signerID }
func (t *testSignable) GetSignature() []byte  { return t.sig }
func (t *testSignable) SetSignature(s []byte) { t.sig = s }

// generateTestKeyPair creates a fresh Ed25519 key pair for testing.
func generateTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

// generateTestCert creates a self-signed x509 certificate for the given Ed25519 key pair.
func generateTestCert(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey) *x509.Certificate {
	t.Helper()
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}
	return cert
}

// tlsPeerCtx returns a context carrying TLS peer info for the given certificate.
func tlsPeerCtx(parent context.Context, cert *x509.Certificate) context.Context {
	tlsInfo := credentials.TLSInfo{
		State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{cert},
		},
	}
	return peer.NewContext(parent, &peer.Peer{AuthInfo: tlsInfo})
}

// noPeerCtx returns a context carrying TLS peer info with no client certificates.
func noPeerCtx(parent context.Context) context.Context {
	tlsInfo := credentials.TLSInfo{State: tls.ConnectionState{}}
	return peer.NewContext(parent, &peer.Peer{AuthInfo: tlsInfo})
}

// nonTLSPeerCtx returns a context with a peer that has no auth info.
func nonTLSPeerCtx(parent context.Context) context.Context {
	return peer.NewContext(parent, &peer.Peer{})
}

// inboundCtxWithTLS returns a context carrying both the gorums-node-id metadata
// and TLS peer info for the given certificate.
func inboundCtxWithTLS(parent context.Context, id uint32, cert *x509.Certificate) context.Context {
	return tlsPeerCtx(inboundCtx(parent, id), cert)
}

// -----------------------------------------------------------------------------
// TestEqualPublicKeys
// -----------------------------------------------------------------------------

func TestSecurityEqualPublicKeys(t *testing.T) {
	pub1, _ := generateTestKeyPair(t)
	pub2, _ := generateTestKeyPair(t)

	tests := []struct {
		name string
		a, b crypto.PublicKey
		want bool
	}{
		{"SameKey", pub1, pub1, true},
		{"DifferentKeys", pub1, pub2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalPublicKeys(tc.a, tc.b); got != tc.want {
				t.Errorf("equalPublicKeys() = %v; want %v", got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// TestPeerPublicKey
// -----------------------------------------------------------------------------

func TestSecurityPeerPublicKey(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	cert := generateTestCert(t, pub, priv)

	tests := []struct {
		name    string
		ctx     context.Context
		wantPub crypto.PublicKey
		wantErr bool
	}{
		{
			name:    "ValidTLSCert",
			ctx:     tlsPeerCtx(context.Background(), cert),
			wantPub: pub,
			wantErr: false,
		},
		{
			name:    "NoCertsInTLS",
			ctx:     noPeerCtx(context.Background()),
			wantErr: true,
		},
		{
			name:    "NonTLSPeer",
			ctx:     nonTLSPeerCtx(context.Background()),
			wantErr: true,
		},
		{
			name:    "NoPeerInfo",
			ctx:     context.Background(),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := peerPublicKey(tc.ctx)
			if tc.wantErr {
				if err == nil {
					t.Error("peerPublicKey() = nil error; want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("peerPublicKey() error = %v; want nil", err)
			}
			if !equalPublicKeys(got, tc.wantPub) {
				t.Error("peerPublicKey() returned unexpected public key")
			}
		})
	}
}

// -----------------------------------------------------------------------------
// TestVerifyCertForNode
// -----------------------------------------------------------------------------

func TestSecurityVerifyCertForNode(t *testing.T) {
	pub1, priv1 := generateTestKeyPair(t)
	pub2, priv2 := generateTestKeyPair(t)
	cert1 := generateTestCert(t, pub1, priv1)
	cert2 := generateTestCert(t, pub2, priv2)

	tests := []struct {
		name     string
		ctx      context.Context
		expected crypto.PublicKey
		wantErr  bool
	}{
		{
			name:     "MatchingCert",
			ctx:      tlsPeerCtx(context.Background(), cert1),
			expected: pub1,
			wantErr:  false,
		},
		{
			name:     "MismatchedCert",
			ctx:      tlsPeerCtx(context.Background(), cert2),
			expected: pub1,
			wantErr:  true,
		},
		{
			name:     "NoCert",
			ctx:      noPeerCtx(context.Background()),
			expected: pub1,
			wantErr:  true,
		},
		{
			name:     "NoTLS",
			ctx:      context.Background(),
			expected: pub1,
			wantErr:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyCertForNode(tc.ctx, tc.expected)
			if tc.wantErr && err == nil {
				t.Error("verifyCertForNode() = nil; want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("verifyCertForNode() = %v; want nil", err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// TestAcceptPeerWithCertVerification
// -----------------------------------------------------------------------------

// TestSecurityAcceptPeerCertVerification checks that AcceptPeer promotes or
// demotes a peer based on whether its TLS certificate matches the registered
// public key for its claimed node ID.
func TestSecurityAcceptPeerCertVerification(t *testing.T) {
	pub1, priv1 := generateTestKeyPair(t)
	pub2, priv2 := generateTestKeyPair(t)
	cert1 := generateTestCert(t, pub1, priv1)
	cert2 := generateTestCert(t, pub2, priv2)

	// Build an inboundManager with node IDs 1, 2, 3 in the peer list but with
	// myID=99 so nodes 1-3 only appear in Config when their channel is attached.
	// Node 1 has a registered public key; node 2 does not (cert check skipped).
	im := newInboundManager(99, WithNodes(map[uint32]testNode{
		1: {"127.0.0.1:9081"},
		2: {"127.0.0.1:9082"},
		3: {"127.0.0.1:9083"},
	}), 0, nil, nil, map[uint32]crypto.PublicKey{
		1: pub1,
	})

	s := newMockBidiStream()
	defer s.close()

	tests := []struct {
		name         string
		streamCtx    context.Context
		wantInConfig bool // whether the peer should appear in Config after connecting
	}{
		{
			name:         "KnownPeerMatchingCert",
			streamCtx:    inboundCtxWithTLS(t.Context(), 1, cert1),
			wantInConfig: true,
		},
		{
			name:         "KnownPeerMismatchedCert",
			streamCtx:    inboundCtxWithTLS(t.Context(), 1, cert2),
			wantInConfig: false,
		},
		{
			name: "KnownPeerNoCert",
			// TLS peer info present but no client certificate.
			streamCtx:    peer.NewContext(inboundCtx(t.Context(), 1), &peer.Peer{AuthInfo: credentials.TLSInfo{}}),
			wantInConfig: false,
		},
		{
			name: "KnownPeerNoPubKeyRegistered",
			// Node 2 has no registered pub key: cert verification is skipped and
			// the peer is accepted as a known peer unconditionally.
			streamCtx:    inboundCtx(t.Context(), 2),
			wantInConfig: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Use a fresh stream for each subtest.
			ms := newMockBidiStream()
			defer ms.close()

			peerNode, cleanup, err := im.AcceptPeer(tc.streamCtx, ms)
			if err != nil {
				t.Fatalf("AcceptPeer() error = %v; want nil", err)
			}
			if peerNode == nil {
				t.Fatal("AcceptPeer() returned nil peerNode")
			}
			defer cleanup()

			// Check whether the peer landed in Config.
			inCfg := false
			for _, n := range im.Config() {
				if n.ID() == nodeID(tc.streamCtx) {
					inCfg = true
					break
				}
			}

			if inCfg != tc.wantInConfig {
				t.Errorf("peer in Config = %v; want %v", inCfg, tc.wantInConfig)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// TestWithMutualTLS
// -----------------------------------------------------------------------------

// TestSecurityWithMutualTLS verifies that WithMutualTLS returns a valid
// ServerOption that sets up gRPC mTLS credentials without panic.
func TestSecurityWithMutualTLS(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	cert := generateTestCert(t, pub, priv)

	// Build a tls.Certificate from the key pair.
	tlsCert := tls.Certificate{
		Certificate: [][]byte{cert.Raw},
		PrivateKey:  priv,
		Leaf:        cert,
	}

	ca := x509.NewCertPool()
	ca.AddCert(cert)

	opt := WithMutualTLS(tlsCert, ca)
	if opt == nil {
		t.Fatal("WithMutualTLS() returned nil")
	}

	// Verify NewServer does not panic with this option.
	s := NewServer(opt)
	s.Stop()
}

// -----------------------------------------------------------------------------
// TestWithPeerPublicKeys
// -----------------------------------------------------------------------------

// TestSecurityWithPeerPublicKeys checks that WithPeerPublicKeys stores the keys
// and that AcceptPeer uses them for cert verification.
func TestSecurityWithPeerPublicKeys(t *testing.T) {
	pub1, priv1 := generateTestKeyPair(t)
	pub2, _ := generateTestKeyPair(t)
	cert1 := generateTestCert(t, pub1, priv1)

	// wrong cert: pub2 registered but cert1 presented.
	// myID=99 ensures no node is auto-included in Config (they need an active channel).
	peerKeys := map[uint32]crypto.PublicKey{1: pub2}

	im := newInboundManager(99, WithNodes(map[uint32]testNode{
		1: {"127.0.0.1:9081"},
		2: {"127.0.0.1:9082"},
	}), 0, nil, nil, peerKeys)

	ms := newMockBidiStream()
	defer ms.close()

	// Node 1 presents cert1 but pub2 is registered: should be demoted to nil peer.
	ctx := inboundCtxWithTLS(t.Context(), 1, cert1)
	_, cleanup, err := im.AcceptPeer(ctx, ms)
	if err != nil {
		t.Fatalf("AcceptPeer() error = %v", err)
	}
	defer cleanup()

	// Node 1 should not appear in Config because cert did not match.
	for _, n := range im.Config() {
		if n.ID() == 1 {
			t.Errorf("node 1 unexpectedly present in Config after cert mismatch")
		}
	}
}

// -----------------------------------------------------------------------------
// TestNewSigningInterceptor
// -----------------------------------------------------------------------------

// TestSecurityNewSigningInterceptor verifies that the interceptor registers a
// request transform that signs the message exactly once with the provided key.
func TestSecurityNewSigningInterceptor(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	content := []byte("canonical-test-content")

	req := newTestSignable("hello", 42)
	canonicalBytes := func(_ *testSignable) []byte { return content }

	interceptor := NewSigningInterceptor[*testSignable, *pb.StringValue](priv, canonicalBytes)

	// Construct a minimal ClientCtx with just the request field set.
	c := &ClientCtx[*testSignable, *pb.StringValue]{request: req}

	// Calling the interceptor should register the transform and return next (nil here).
	if got := interceptor(c, nil); got != nil {
		t.Error("interceptor returned non-nil ResponseSeq before any call")
	}

	if len(c.reqTransforms) != 1 {
		t.Fatalf("len(reqTransforms) = %d; want 1", len(c.reqTransforms))
	}

	// Execute the transform to trigger signing.
	signed := c.reqTransforms[0](req, nil)

	sig := signed.GetSignature()
	if len(sig) == 0 {
		t.Fatal("signature is empty after transform")
	}
	if !ed25519.Verify(pub, content, sig) {
		t.Error("signature verification failed")
	}
}

// TestSecuritySigningInterceptorSignsOnce verifies that calling the transform
// multiple times (simulating multiple nodes) only signs once.
func TestSecuritySigningInterceptorSignsOnce(t *testing.T) {
	_, priv := generateTestKeyPair(t)
	content := []byte("once-content")

	req := newTestSignable("value", 1)
	calls := 0
	canonicalBytes := func(_ *testSignable) []byte {
		calls++
		return content
	}

	interceptor := NewSigningInterceptor[*testSignable, *pb.StringValue](priv, canonicalBytes)
	c := &ClientCtx[*testSignable, *pb.StringValue]{request: req}
	interceptor(c, nil)

	// Invoke the transform three times (simulating three nodes).
	c.reqTransforms[0](req, nil)
	c.reqTransforms[0](req, nil)
	c.reqTransforms[0](req, nil)

	if calls != 1 {
		t.Errorf("canonicalBytes called %d times; want 1 (sign-once guarantee)", calls)
	}
}

// -----------------------------------------------------------------------------
// TestNewVerifyingInterceptor
// -----------------------------------------------------------------------------

// makeVerifyCtx creates a ServerCtx suitable for interceptor unit tests.
func makeVerifyCtx(t *testing.T) ServerCtx {
	t.Helper()
	return ServerCtx{Context: t.Context()}
}

// makeSignableMessage builds a *Message wrapping a *testSignable for use in
// verifying interceptor tests.
func makeSignableMessage(t *testing.T, priv ed25519.PrivateKey, signerID uint32, content []byte, tamper bool) *Message {
	t.Helper()
	sig := ed25519.Sign(priv, content)
	if tamper {
		sig[0] ^= 0xFF
	}
	msg := newTestSignable("payload", signerID)
	msg.SetSignature(sig)
	return &Message{Msg: msg, Message: stream.Message_builder{}.Build()}
}

func TestSecurityNewVerifyingInterceptor(t *testing.T) {
	pub1, priv1 := generateTestKeyPair(t)
	pub2, _ := generateTestKeyPair(t)
	content := []byte("interceptor-test-content")

	pubKeys := map[uint32]ed25519.PublicKey{1: pub1, 2: pub2}
	canonicalBytes := func(_ proto.Message) []byte { return content }
	interceptor := NewVerifyingInterceptor(pubKeys, canonicalBytes)

	// passThrough is a handler that always succeeds; used to verify the interceptor
	// calls it for valid messages.
	passThrough := func(ctx ServerCtx, req *Message) (*Message, error) {
		return req, nil
	}

	tests := []struct {
		name     string
		req      *Message
		wantCode codes.Code
	}{
		{
			name:     "ValidSignature",
			req:      makeSignableMessage(t, priv1, 1, content, false),
			wantCode: codes.OK,
		},
		{
			name:     "TamperedSignature",
			req:      makeSignableMessage(t, priv1, 1, content, true),
			wantCode: codes.Unauthenticated,
		},
		{
			name: "MissingSignature",
			req: &Message{
				Msg:     newTestSignable("payload", 1),
				Message: stream.Message_builder{}.Build(),
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "UnknownSignerID",
			req:      makeSignableMessage(t, priv1, 99, content, false),
			wantCode: codes.Unauthenticated,
		},
		{
			name: "NonSignableMessagePassesThrough",
			req: &Message{
				// Use a plain proto.Message that does not implement Signable.
				Msg:     pb.String("plain message"),
				Message: stream.Message_builder{}.Build(),
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interceptor(makeVerifyCtx(t), tc.req, passThrough)

			if tc.wantCode == codes.OK {
				if err != nil {
					t.Errorf("interceptor() error = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("interceptor() = nil error; want %v", tc.wantCode)
			}
			if got := status.Code(err); got != tc.wantCode {
				t.Errorf("interceptor() code = %v; want %v", got, tc.wantCode)
			}
		})
	}
}
