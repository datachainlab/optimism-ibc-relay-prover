package l2

import (
	"context"
	"math/big"

	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/client"
	lcrelay "github.com/datachainlab/ethereum-light-client-types/prover/relay"
	"github.com/ethereum/go-ethereum/common"
)

// proofClient adapts the ethereum-ibc-relay-chain ETHClient to lcrelay.ProofClient.
type proofClient struct {
	*client.ETHClient
}

var _ lcrelay.ProofClient = (*proofClient)(nil)

func (c proofClient) GetProof(ctx context.Context, address common.Address, storageKeys [][]byte, blockNumber *big.Int) (*lcrelay.StateProof, error) {
	proof, err := c.ETHClient.GetProof(ctx, address, storageKeys, blockNumber)
	if err != nil {
		return nil, err
	}
	return &lcrelay.StateProof{
		StorageHash:     proof.StorageHash,
		AccountProofRLP: proof.AccountProofRLP,
		StorageProofRLP: proof.StorageProofRLP,
	}, nil
}
