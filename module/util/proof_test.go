package util

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestEmptyTrieRoot(t *testing.T) {
	// Empty trie root should be keccak256(0x80)
	// 0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421
	expected := common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	require.Equal(t, expected[:], EmptyTrieRoot)
}

func TestVerifyStorageRootNotEmpty(t *testing.T) {
	tests := []struct {
		name        string
		storageRoot []byte
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty slice",
			storageRoot: []byte{},
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "nil",
			storageRoot: nil,
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "zero hash",
			storageRoot: make([]byte, 32),
			wantErr:     true,
			errContains: "zero",
		},
		{
			name:        "empty trie root",
			storageRoot: EmptyTrieRoot,
			wantErr:     true,
			errContains: "empty trie root",
		},
		{
			name:        "valid storage root",
			storageRoot: common.HexToHash("1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef").Bytes(),
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyStorageRootNotEmpty(tt.storageRoot)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
