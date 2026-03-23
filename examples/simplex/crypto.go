package main

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"

	pb "github.com/relab/gorums/examples/simplex/proto"
)

// Domain separation prefixes prevent signature reuse across message types.
const (
	domainVote     = "simplex-vote\x00"
	domainPropose  = "simplex-propose\x00"
	domainFinalize = "simplex-finalize\x00"
)

// signable is a thin helper that builds the canonical byte string that is
// signed or verified for each message type.  Using a deterministic encoding
// prevents malleability and ensures the same bytes are produced on every node.
//
// Layout per type:
//
//	VoteMsg:     domain(13) + height(8) + blockID
//	ProposeMsg:  domain(16) + height(8) + blockID + "|" + parentHash
//	FinalizeMsg: domain(17) + height(8)
func voteBytes(height uint64, block *pb.Block) []byte {
	return appendUint64([]byte(domainVote), height, blockID(block))
}

func proposeBytes(height uint64, block *pb.Block, chainH string) []byte {
	msg := appendUint64([]byte(domainPropose), height, blockID(block))
	msg = append(msg, '|')
	msg = append(msg, chainH...)
	return msg
}

func finalizeBytes(height uint64) []byte {
	return appendUint64([]byte(domainFinalize), height, "")
}

// appendUint64 appends an 8-byte big-endian representation of v followed by
// the optional suffix to dst, returning the extended slice.
func appendUint64(dst []byte, v uint64, suffix string) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	dst = append(dst, buf[:]...)
	dst = append(dst, suffix...)
	return dst
}

// signVote signs msg and sets its Signature field.
func signVote(msg *pb.VoteMsg, priv ed25519.PrivateKey) {
	msg.SetSignature(ed25519.Sign(priv, voteBytes(msg.GetHeight(), msg.GetBlock())))
}

// signPropose signs msg and sets its Signature field.
func signPropose(msg *pb.ProposeMsg, priv ed25519.PrivateKey) {
	data := proposeBytes(msg.GetHeight(), msg.GetBlock(), chainHash(msg.GetChain()))
	msg.SetSignature(ed25519.Sign(priv, data))
}

// signFinalize signs msg and sets its Signature field.
func signFinalize(msg *pb.FinalizeMsg, priv ed25519.PrivateKey) {
	msg.SetSignature(ed25519.Sign(priv, finalizeBytes(msg.GetHeight())))
}

// verifyVote verifies the Ed25519 signature on msg against pub.
// Returns an error if the signature is missing or invalid.
func verifyVote(msg *pb.VoteMsg, pub ed25519.PublicKey) error {
	sig := msg.GetSignature()
	if len(sig) == 0 {
		return errors.New("missing signature")
	}
	if !ed25519.Verify(pub, voteBytes(msg.GetHeight(), msg.GetBlock()), sig) {
		return fmt.Errorf("invalid vote signature from node %d at height %d", msg.GetVoterId(), msg.GetHeight())
	}
	return nil
}

// verifyPropose verifies the Ed25519 signature on msg against pub.
func verifyPropose(msg *pb.ProposeMsg, pub ed25519.PublicKey) error {
	sig := msg.GetSignature()
	if len(sig) == 0 {
		return errors.New("missing signature")
	}
	data := proposeBytes(msg.GetHeight(), msg.GetBlock(), chainHash(msg.GetChain()))
	if !ed25519.Verify(pub, data, sig) {
		return fmt.Errorf("invalid propose signature from leader %d at height %d", msg.GetLeaderId(), msg.GetHeight())
	}
	return nil
}

// verifyFinalize verifies the Ed25519 signature on msg against pub.
func verifyFinalize(msg *pb.FinalizeMsg, pub ed25519.PublicKey) error {
	sig := msg.GetSignature()
	if len(sig) == 0 {
		return errors.New("missing signature")
	}
	if !ed25519.Verify(pub, finalizeBytes(msg.GetHeight()), sig) {
		return fmt.Errorf("invalid finalize signature from node %d at height %d", msg.GetNodeId(), msg.GetHeight())
	}
	return nil
}
