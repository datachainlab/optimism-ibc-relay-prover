package util

import (
	"bytes"

	"github.com/cockroachdb/errors"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// EmptyTrieRoot is keccak256 of RLP encoded empty bytes (0x80)
// This represents the root hash of an empty Merkle Patricia Trie
var EmptyTrieRoot = crypto.Keccak256(mustRLPEncodeEmptyBytes())

func mustRLPEncodeEmptyBytes() []byte {
	encoded, err := rlp.EncodeToBytes([]byte{})
	if err != nil {
		panic(err)
	}
	return encoded
}

// VerifyStorageRootNotEmpty verifies that the account storage root is not empty or zero.
// Empty storage root (keccak256 of empty trie) indicates the account has no storage.
func VerifyStorageRootNotEmpty(storageRoot []byte) error {
	if len(storageRoot) == 0 {
		return errors.New("account storage root is empty")
	}

	// Check for zero hash
	zeroHash := make([]byte, len(storageRoot))
	if bytes.Equal(storageRoot, zeroHash) {
		return errors.New("account storage root is zero")
	}

	// Check for empty trie root (keccak256 of RLP encoded empty bytes)
	if bytes.Equal(storageRoot, EmptyTrieRoot) {
		return errors.New("account storage root is empty trie root (account has no storage)")
	}

	return nil
}
