package module

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	lctypes "github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	"github.com/ethereum/go-ethereum/crypto"
	"io"
	"math/big"
	"net/http"
)

type L2Client struct {
	config *ProverConfig
	*ethereum.Chain
}

func NewL2Client(config *ProverConfig, chain *ethereum.Chain) *L2Client {
	return &L2Client{
		config: config,
		Chain:  chain,
	}
}

func (c *L2Client) LatestFinalizedHeight() (ibcexported.Height, error) {
	syncStatus, err := c.SyncStatus()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	finalizedHeight := syncStatus.FinalizedL2.Number
	return clienttypes.NewHeight(0, finalizedHeight), nil
}

// LatestDerivation retrieves the latest derivation information from the rollup client.
// It fetches the sync status, claimed output, and agreed output for the latest blocks.
func (c *L2Client) LatestDerivation(ctx context.Context) (*L1BlockRef, *Derivation, error) {
	syncStatus, err := c.SyncStatus()
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	claimed := syncStatus.FinalizedL2
	claimedNumber := claimed.Number
	agreedNumber := claimedNumber - 1

	claimedOutput, err := c.OutputAtBlock(claimedNumber)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	agreedOutput, err := c.OutputAtBlock(agreedNumber)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	return &syncStatus.FinalizedL1, &Derivation{
		AgreedL2HeadHash:   agreedOutput.BlockRef.Hash.Bytes(),
		AgreedL2OutputRoot: agreedOutput.OutputRoot[:],
		L2HeadHash:         claimed.Hash.Bytes(),
		L2OutputRoot:       claimedOutput.OutputRoot[:],
		L2BlockNumber:      claimedNumber,
	}, nil

}

// SetupDerivations sets up a list of derivations between the trusted height and the latest agreed number.
// It iterates from the trusted height to the latest agreed number, fetching the agreed and claimed outputs
// for each block and appending them to the derivations list.
func (c *L2Client) SetupDerivations(ctx context.Context, trustedHeight uint64, latestAgreedNumber uint64) ([]*Derivation, error) {
	derivations := make([]*Derivation, 0)
	for i := trustedHeight; i < latestAgreedNumber; i++ {
		agreedNumber := trustedHeight
		agreedOutput, err := c.OutputAtBlock(agreedNumber)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		claimedNumber := agreedNumber + 1
		claimedOutput, err := c.OutputAtBlock(claimedNumber)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		derivations = append(derivations, &Derivation{
			AgreedL2HeadHash:   agreedOutput.BlockRef.Hash.Bytes(),
			AgreedL2OutputRoot: agreedOutput.OutputRoot[:],
			L2HeadHash:         claimedOutput.BlockRef.Hash.Bytes(),
			L2OutputRoot:       claimedOutput.OutputRoot[:],
			L2BlockNumber:      claimedNumber,
		})
	}
	return derivations, nil
}

// CreatePreimages sends a list of derivations to the preimage maker service and returns the preimage data.
// It marshals the derivations into JSON, sends a POST request to the preimage maker endpoint, and reads the response.
func (c *L2Client) CreatePreimages(ctx context.Context, derivations []*Derivation) ([]byte, error) {
	httpClient := http.Client{
		Timeout: c.config.PreimageMakerTimeout,
	}
	body, err := json.Marshal(derivations)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	buffer := bytes.NewBuffer(body)
	response, err := httpClient.Post(fmt.Sprintf("%s/derivation", c.config.PreimageMakerEndpoint), "application/json", buffer)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	preimageData, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return preimageData, nil
}

func (c *L2Client) TimestampAt(ctx context.Context, number uint64) (uint64, error) {
	header, err := c.Chain.Client().HeaderByNumber(ctx, big.NewInt(0).SetUint64(number))
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return header.Time, nil
}

func (c *L2Client) BuildAccountUpdate(blockNumber uint64) (*lctypes.AccountUpdate, error) {
	proof, err := c.Chain.Client().GetProof(
		c.Chain.Config().IBCAddress(),
		nil,
		big.NewInt(int64(blockNumber)),
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &lctypes.AccountUpdate{
		AccountProof:       proof.AccountProofRLP,
		AccountStorageRoot: proof.StorageHash[:],
	}, nil
}

func (c *L2Client) BuildStateProof(path []byte, height uint64) ([]byte, error) {
	// calculate slot for commitment
	storageKey := crypto.Keccak256Hash(append(
		crypto.Keccak256Hash(path).Bytes(),
		IBCCommitmentsSlot.Bytes()...,
	))
	storageKeyHex, err := storageKey.MarshalText()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// call eth_getProof
	stateProof, err := c.Chain.Client().GetProof(
		c.Chain.Config().IBCAddress(),
		[][]byte{storageKeyHex},
		big.NewInt(0).SetUint64(height),
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return stateProof.StorageProofRLP[0], nil
}
