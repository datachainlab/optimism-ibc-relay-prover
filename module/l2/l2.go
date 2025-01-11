package l2

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
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"io"
	"math/big"
	"net/http"
	"time"
)

var IBCCommitmentsSlot = common.HexToHash("1ee222554989dda120e26ecacf756fe1235cd8d726706b57517715dde4f0c900")

type L2Derivation struct {
	L1 L1BlockRef
	L2 types.Derivation
}

type L2Client struct {
	*ethereum.Chain
	l1ExecutionClient     *ethclient.Client
	preimageMakerTimeout  time.Duration
	preimageMakerEndpoint string
	opNodeEndpoint        string
}

func NewL2Client(chain *ethereum.Chain,
	l1ExecutionEndpoint string,
	preimageMakerTimeout time.Duration,
	preimageMakerEndpoint string,
	opNodeEndpoint string,
) *L2Client {
	l1ExecutionClient, err := ethclient.Dial(l1ExecutionEndpoint)
	if err != nil {
		panic(err)
	}
	return &L2Client{
		Chain:                 chain,
		l1ExecutionClient:     l1ExecutionClient,
		preimageMakerTimeout:  preimageMakerTimeout,
		preimageMakerEndpoint: preimageMakerEndpoint,
		opNodeEndpoint:        opNodeEndpoint,
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
func (c *L2Client) LatestDerivation(ctx context.Context) (*L2Derivation, error) {
	syncStatus, err := c.SyncStatus()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	finalized := syncStatus.FinalizedL2
	targetNumber := finalized.Number
	if targetNumber == 0 {
		return nil, errors.New("no finalized block")
	}
	agreedNumber := targetNumber - 1

	targetOutput, err := c.OutputAtBlock(targetNumber)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	agreedOutput, err := c.OutputAtBlock(agreedNumber)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &L2Derivation{
		L1: syncStatus.FinalizedL1,
		L2: types.Derivation{
			AgreedL2HeadHash:   agreedOutput.BlockRef.Hash.Bytes(),
			AgreedL2OutputRoot: agreedOutput.OutputRoot[:],
			L2HeadHash:         finalized.Hash.Bytes(),
			L2OutputRoot:       targetOutput.OutputRoot[:],
			L2BlockNumber:      targetNumber,
		}}, nil

}

// SetupDerivations sets up a list of derivations between the trusted height and the latest agreed number.
// It iterates from the trusted height to the latest agreed number, fetching the agreed and claimed outputs
// for each block and appending them to the derivations list.
func (c *L2Client) SetupDerivations(ctx context.Context, trustedHeight uint64, latestAgreedNumber uint64, latestFinalizedL1Number uint64) ([]*L2Derivation, error) {
	derivations := make([]*L2Derivation, 0)
	for i := trustedHeight; i < latestAgreedNumber; i++ {
		agreedNumber := i
		agreedOutput, err := c.OutputAtBlock(agreedNumber)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		claimedNumber := agreedNumber + 1
		claimedOutput, err := c.OutputAtBlock(claimedNumber)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		// use threshold number inorder to reduce derivation latency
		l1Number := latestFinalizedL1Number
		header, err := c.l1ExecutionClient.HeaderByNumber(ctx, big.NewInt(0).SetUint64(l1Number))
		if err != nil {
			return nil, errors.WithStack(err)
		}

		derivations = append(derivations, &L2Derivation{
			L1: L1BlockRef{
				Hash:   header.Hash(),
				Number: l1Number,
			},
			L2: types.Derivation{
				AgreedL2HeadHash:   agreedOutput.BlockRef.Hash.Bytes(),
				AgreedL2OutputRoot: agreedOutput.OutputRoot[:],
				L2HeadHash:         claimedOutput.BlockRef.Hash.Bytes(),
				L2OutputRoot:       claimedOutput.OutputRoot[:],
				L2BlockNumber:      claimedNumber,
			},
		})
	}
	return derivations, nil
}

// CreatePreimages sends a list of derivations to the preimage maker service and returns the preimage data.
// It marshals the derivations into JSON, sends a POST request to the preimage maker endpoint, and reads the response.
func (c *L2Client) CreatePreimages(ctx context.Context, derivations []*L2Derivation) ([]byte, error) {
	httpClient := http.Client{
		Timeout: c.preimageMakerTimeout,
	}
	type rawType struct {
		L1HeadHash         common.Hash `json:"l1_head_hash"`
		AgreedL2HeadHash   common.Hash `json:"agreed_l2_head_hash"`
		AgreedL2OutputRoot common.Hash `json:"agreed_l2_output_root"`
		L2HeadHash         common.Hash `json:"l2_head_hash"`
		L2OutputRoot       common.Hash `json:"l2_output_root"`
		L2BlockNumber      uint64      `json:"l2_block_number"`
	}
	rawDerivations := make([]rawType, 0)
	for _, derivation := range derivations {
		rawDerivations = append(rawDerivations, rawType{
			L1HeadHash:         derivation.L1.Hash,
			AgreedL2HeadHash:   common.BytesToHash(derivation.L2.AgreedL2HeadHash),
			AgreedL2OutputRoot: common.BytesToHash(derivation.L2.AgreedL2OutputRoot),
			L2HeadHash:         common.BytesToHash(derivation.L2.L2HeadHash),
			L2OutputRoot:       common.BytesToHash(derivation.L2.L2OutputRoot),
			L2BlockNumber:      derivation.L2.L2BlockNumber,
		})
	}
	body, err := json.Marshal(rawDerivations)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	buffer := bytes.NewBuffer(body)
	response, err := httpClient.Post(fmt.Sprintf("%s/derivation", c.preimageMakerEndpoint), "application/json", buffer)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to create preimages: status=%d", response.StatusCode)
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
