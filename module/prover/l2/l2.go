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
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/log"
	"io"
	"math/big"
	"net/http"
	"strings"
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
	opNodeTimeout         time.Duration
	preimageMakerTimeout  time.Duration
	preimageMakerEndpoint *util.Selector[string]
	opNodeEndpoint        string
	logger                *log.RelayLogger
}

func NewL2Client(chain *ethereum.Chain,
	l1ExecutionEndpoint string,
	opNodeTimeout time.Duration,
	preimageMakerTimeout time.Duration,
	preimageMakerEndpoint string,
	opNodeEndpoint string,
	logger *log.RelayLogger,
) *L2Client {
	l1ExecutionClient, err := ethclient.Dial(l1ExecutionEndpoint)
	if err != nil {
		panic(err)
	}
	return &L2Client{
		Chain:                 chain,
		l1ExecutionClient:     l1ExecutionClient,
		opNodeTimeout:         opNodeTimeout,
		preimageMakerTimeout:  preimageMakerTimeout,
		preimageMakerEndpoint: util.NewSelector(strings.Split(preimageMakerEndpoint, ",")),
		opNodeEndpoint:        opNodeEndpoint,
		logger:                logger,
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
		return nil, errors.Wrap(err, "failed to marshal preimage request")
	}
	buffer := bytes.NewBuffer(body)

	httpRequest, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/derivation", c.preimageMakerEndpoint.Get()), buffer)
	if err != nil {
		return nil, errors.Wrap(err, "failed to call preimage request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return nil, errors.Wrap(err, "failed to make preimage request")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to create preimages: status=%d", response.StatusCode)
	}
	preimageData, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read preimage data")
	}
	return preimageData, nil
}

func (c *L2Client) TimestampAt(ctx context.Context, number uint64) (uint64, error) {
	header, err := c.Chain.Client().HeaderByNumber(ctx, big.NewInt(0).SetUint64(number))
	if err != nil {
		return 0, errors.Wrapf(err, "failed to get block from number: number=%d", number)
	}
	return header.Time, nil
}

func (c *L2Client) BuildAccountUpdate(ctx context.Context, blockNumber uint64) (*lctypes.AccountUpdate, error) {
	proof, err := c.Chain.Client().GetProof(
		ctx,
		c.Chain.Config().IBCAddress(),
		nil,
		new(big.Int).SetUint64(blockNumber),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get account proof: number=%d", blockNumber)
	}
	c.logger.InfoContext(ctx, "buildAccountUpdate: get proof", "block_number", blockNumber, "ibc_address", c.Chain.Config().IBCAddress().String(), "account_proof", hex.EncodeToString(proof.AccountProofRLP), "storage_hash", hex.EncodeToString(proof.StorageHash[:]))
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
		return nil, errors.Wrapf(err, "failed to marshal storage key: key=%s, proof=%d", storageKey.Hex(), height)
	}

	// call eth_getProof
	stateProof, err := c.Chain.Client().GetProof(
		ctx,
		c.Chain.Config().IBCAddress(),
		[][]byte{storageKeyHex},
		big.NewInt(height),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get storage proof : key=%s, proof=%d", storageKey.Hex(), height)
	}
	return stateProof.StorageProofRLP[0], nil
}
