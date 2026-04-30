package gorums

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Signable is implemented by proto messages that carry an Ed25519 signature
// and a signer ID. SetSignature must modify the receiver in place.
//
// It is intended for use with [NewSigningInterceptor] and [NewVerifyingInterceptor].
// Both the signer ID and signature fields are expected to be protobuf fields on the
// concrete message type, with accessor methods generated or hand-written to match
// this interface.
type Signable interface {
	proto.Message
	// GetSignerID returns the node ID of the signing node.
	GetSignerID() uint32
	// GetSignature returns the raw Ed25519 signature bytes.
	GetSignature() []byte
	// SetSignature sets the signature field in place.
	SetSignature([]byte)
}

// WithMutualTLS configures the server with mutual TLS (mTLS) authentication.
// Both the server and connecting clients must present valid certificates signed
// by a CA in clientCAs. serverCert is this server's TLS key pair.
//
// When combined with [WithPeerPublicKeys] and [WithConfig], the TLS certificate
// presented by a connecting peer is additionally checked against the registered
// public key for that peer's gorums node ID, preventing a valid-cert peer from
// claiming a foreign node ID.
func WithMutualTLS(serverCert tls.Certificate, clientCAs *x509.CertPool) ServerOption {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}
	return WithGRPCServerOptions(grpc.Creds(credentials.NewTLS(cfg)))
}

// WithPeerPublicKeys registers the expected public key for each known peer node.
// When a peer connects and presents a gorums-node-id in its metadata, the server
// extracts the TLS peer certificate and verifies that its public key matches the
// key registered for that node ID. Peers that fail verification are demoted to
// anonymous connections and excluded from [ServerCtx.Config].
//
// This option has no effect unless peers connect over TLS with client certificates
// (see [WithMutualTLS]).
func WithPeerPublicKeys(keys map[uint32]crypto.PublicKey) ServerOption {
	return func(o *serverOptions) {
		o.peerPubKeys = keys
	}
}

// NewSigningInterceptor returns a [QuorumInterceptor] that signs the outgoing
// request using the Ed25519 private key priv. canonicalBytes must return a
// deterministic byte slice representing the request content to sign; it must
// not include the signature field itself.
//
// The signature is computed once on the first node of the configuration and
// cached, so all recipients receive the same signed message. This is correct
// for broadcast calls where the signature covers message content, not recipient
// identity.
//
// Req must implement [Signable]; SetSignature is called in place on the shared
// request value so all nodes receive the same signed payload. This interceptor
// should be applied last so that all content fields are set before signing.
func NewSigningInterceptor[Req Signable, Resp msg](
	priv ed25519.PrivateKey,
	canonicalBytes func(Req) []byte,
) QuorumInterceptor[Req, Resp] {
	return func(c *ClientCtx[Req, Resp], next ResponseSeq[Resp]) ResponseSeq[Resp] {
		var once sync.Once
		c.reqTransforms = append(c.reqTransforms, func(req Req, _ *Node) Req {
			// Sign exactly once; subsequent nodes reuse the signature already set
			// on the shared request since all nodes receive the same signed message.
			once.Do(func() {
				req.SetSignature(ed25519.Sign(priv, canonicalBytes(req)))
			})
			return req
		})
		return next
	}
}

// NewVerifyingInterceptor returns a server-side [Interceptor] that verifies
// Ed25519 signatures on incoming requests implementing [Signable]. Requests
// from unknown signer IDs or with an invalid or absent signature are rejected
// with an UNAUTHENTICATED status error and never reach the application handler.
// Requests for message types that do not implement [Signable] pass through
// unchanged, allowing the interceptor to be installed globally.
//
// pubKeys maps signer node IDs to their Ed25519 public keys.
// canonicalBytes must produce the same bytes as those passed to [NewSigningInterceptor].
func NewVerifyingInterceptor(
	pubKeys map[uint32]ed25519.PublicKey,
	canonicalBytes func(proto.Message) []byte,
) Interceptor {
	return func(ctx ServerCtx, req *Message, next Handler) (*Message, error) {
		s, ok := req.Msg.(Signable)
		if !ok {
			// Non-signable message type: pass through without modification.
			return next(ctx, req)
		}
		signerID := s.GetSignerID()
		pub, ok := pubKeys[signerID]
		if !ok {
			return nil, status.Errorf(codes.Unauthenticated, "gorums: unknown signer ID %d", signerID)
		}
		sig := s.GetSignature()
		if len(sig) == 0 {
			return nil, status.Errorf(codes.Unauthenticated, "gorums: missing signature from signer %d", signerID)
		}
		if !ed25519.Verify(pub, canonicalBytes(req.Msg), sig) {
			return nil, status.Errorf(codes.Unauthenticated, "gorums: invalid signature from signer %d", signerID)
		}
		return next(ctx, req)
	}
}

// verifyCertForNode extracts the TLS public key from the peer certificate in ctx
// and verifies that it matches expected. Returns a non-nil error if the context
// carries no TLS peer info, the peer presented no certificate, or the public key
// does not match.
func verifyCertForNode(ctx context.Context, expected crypto.PublicKey) error {
	got, err := peerPublicKey(ctx)
	if err != nil {
		return fmt.Errorf("gorums: cert verification for peer: %w", err)
	}
	if !equalPublicKeys(got, expected) {
		return errors.New("gorums: peer TLS certificate does not match registered public key")
	}
	return nil
}

// peerPublicKey extracts the public key from the TLS client certificate presented
// by the connecting peer. Returns an error when the stream context carries no
// peer info, the transport is not TLS, or no client certificate was presented.
func peerPublicKey(ctx context.Context) (crypto.PublicKey, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, errors.New("no peer info in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, errors.New("peer connection is not TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, errors.New("peer presented no TLS certificate")
	}
	return tlsInfo.State.PeerCertificates[0].PublicKey, nil
}

// equalPublicKeys reports whether a and b represent the same public key.
// It relies on the Equal method implemented by all standard-library key types
// (ed25519.PublicKey, ecdsa.PublicKey, rsa.PublicKey, etc.).
func equalPublicKeys(a, b crypto.PublicKey) bool {
	type equaler interface {
		Equal(crypto.PublicKey) bool
	}
	if ae, ok := a.(equaler); ok {
		return ae.Equal(b)
	}
	// Fallback: compare as interface values (works for identical byte slices).
	return a == b
}

// gorumsURIScheme, gorumsURIHost, and gorumsCNPrefix define the two formats
// used to encode a gorums NodeID into an X.509 certificate for PKI-native peer
// identity. The URI SAN format "gorums://node/<id>" is preferred; the CN format
// "gorums-node-<id>" is supported as a fallback for environments where URI SANs
// cannot be added to certificates.
const (
	gorumsURIScheme = "gorums"
	gorumsURIHost   = "node"
	gorumsCNPrefix  = "gorums-node-"
)

// nodeIDFromCert extracts a gorums NodeID from a certificate's URI SAN or
// Subject Common Name. URI SANs of the form "gorums://node/<id>" are tried
// first; the CN "gorums-node-<id>" is tried as a fallback. Returns 0 if no
// valid gorums NodeID is found or if the encoded ID is 0 (reserved).
func nodeIDFromCert(cert *x509.Certificate) uint32 {
	// URI SANs are preferred (standards-compliant, does not pollute the CN).
	for _, u := range cert.URIs {
		if u.Scheme == gorumsURIScheme && u.Host == gorumsURIHost {
			id, err := strconv.ParseUint(strings.TrimPrefix(u.Path, "/"), 10, 32)
			if err == nil && id > 0 {
				return uint32(id)
			}
		}
	}
	// Fall back to Common Name.
	if strings.HasPrefix(cert.Subject.CommonName, gorumsCNPrefix) {
		id, err := strconv.ParseUint(cert.Subject.CommonName[len(gorumsCNPrefix):], 10, 32)
		if err == nil && id > 0 {
			return uint32(id)
		}
	}
	return 0
}

// identifyPeerFromCert extracts the gorums NodeID from the TLS client
// certificate in ctx. Returns 0 if the context carries no TLS peer info,
// no client certificate was presented, or the certificate carries no gorums
// identity (see [nodeIDFromCert]).
func identifyPeerFromCert(ctx context.Context) uint32 {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return 0
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return 0
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return 0
	}
	return nodeIDFromCert(tlsInfo.State.PeerCertificates[0])
}

// WithTLSPeerIdentity configures the server to derive each connecting peer's
// NodeID from its verified TLS certificate rather than from the gorums-node-id
// metadata key. The NodeID is read from a URI SAN ("gorums://node/<id>") or,
// as a fallback, from the Subject Common Name ("gorums-node-<id>"). This
// delegates identity entirely to the PKI: any peer whose certificate encodes a
// known NodeID and was signed by the trusted CA is accepted as that peer
// without an additional key map.
//
// This option requires mTLS (see [WithMutualTLS]). Peers whose certificate
// carries no gorums identity are accepted as anonymous connections and
// excluded from [ServerCtx.Config].
func WithTLSPeerIdentity() ServerOption {
	return func(o *serverOptions) {
		o.tlsPeerIdentity = true
	}
}
