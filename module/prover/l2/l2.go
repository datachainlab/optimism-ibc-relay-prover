package l2

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	lctypes "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/log"
	"io"
	"math/big"
	"net/http"
	"time"
)

var IBCCommitmentsSlot = common.HexToHash("1ee222554989dda120e26ecacf756fe1235cd8d726706b57517715dde4f0c900")

type DerivationAttribute struct {
	L1BlockNumber uint64
	L2            DerivationL2Attribute
}

type DerivationL2Attribute struct {
	OutputRoot  common.Hash
	BlockNumber uint64
	BlockHash   common.Hash
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

type PreimageRequest struct {
	L1HeadHash         common.Hash `json:"l1_head_hash"`
	AgreedL2HeadHash   common.Hash `json:"agreed_l2_head_hash"`
	AgreedL2OutputRoot common.Hash `json:"agreed_l2_output_root"`
	L2OutputRoot       common.Hash `json:"l2_output_root"`
	L2BlockNumber      uint64      `json:"l2_block_number"`
}

// CreatePreimages sends a list of derivations to the preimage maker service and returns the preimage data.
// It marshals the derivations into JSON, sends a POST request to the preimage maker endpoint, and reads the response.
func (c *L2Client) CreatePreimages(ctx context.Context, request *PreimageRequest) ([]byte, error) {
	httpClient := http.Client{
		Timeout: c.preimageMakerTimeout,
	}
	body, err := json.Marshal(request)
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

func (c *L2Client) BuildAccountUpdate(ctx context.Context, blockNumber uint64) (*lctypes.AccountUpdate, error) {
	proof, err := c.Chain.Client().GetProof(
		ctx,
		c.Chain.Config().IBCAddress(),
		nil,
		big.NewInt(int64(blockNumber)),
	)
	if err != nil {
		return nil, err
	}
	log.GetLogger().Info("buildAccountUpdate: get proof", "block_number", blockNumber, "ibc_address", c.Chain.Config().IBCAddress().String(), "account_proof", hex.EncodeToString(proof.AccountProofRLP), "storage_hash", hex.EncodeToString(proof.StorageHash[:]))
	return &lctypes.AccountUpdate{
		AccountProof:       proof.AccountProofRLP,
		AccountStorageRoot: proof.StorageHash[:],
	}, nil
}

func (c *L2Client) BuildStateProof(ctx context.Context, path []byte, height int64) ([]byte, error) {
	// calculate slot for commitment
	storageKey := crypto.Keccak256Hash(append(
		crypto.Keccak256Hash(path).Bytes(),
		IBCCommitmentsSlot.Bytes()...,
	))
	storageKeyHex, err := storageKey.MarshalText()
	if err != nil {
		return nil, err
	}

	// call eth_getProof
	stateProof, err := c.Chain.Client().GetProof(
		ctx,
		c.Chain.Config().IBCAddress(),
		[][]byte{storageKeyHex},
		big.NewInt(height),
	)
	if err != nil {
		return nil, err
	}
	return stateProof.StorageProofRLP[0], nil
}
