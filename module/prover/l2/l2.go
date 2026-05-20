package l2

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	lcrelay "github.com/datachainlab/ethereum-light-client-types/relayer/relay"
	lctypes "github.com/datachainlab/ethereum-light-client-types/relayer/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/log"
)

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
	opNodeTimeout           time.Duration
	preimageMakerHttpClient *util.HTTPClient
	preimageMakerEndpoint   *util.Selector[string]
	opNodeEndpoint          string
	logger                  *log.RelayLogger
}

func NewL2Client(chain *ethereum.Chain,
	opNodeTimeout time.Duration,
	preimageMakerTimeout time.Duration,
	preimageMakerEndpoint string,
	opNodeEndpoint string,
	logger *log.RelayLogger,
) *L2Client {
	return &L2Client{
		Chain:                   chain,
		opNodeTimeout:           opNodeTimeout,
		preimageMakerHttpClient: util.NewHTTPClient(preimageMakerTimeout),
		preimageMakerEndpoint:   util.NewSelector(strings.Split(preimageMakerEndpoint, ",")),
		opNodeEndpoint:          opNodeEndpoint,
		logger:                  logger,
	}
}

func (c *L2Client) GetLatestPreimageMetadata(ctx context.Context) (*PreimageMetadata, error) {
	response, err := c.preimageMakerHttpClient.POST(ctx, fmt.Sprintf("%s/get_latest_metadata", c.preimageMakerEndpoint.Get()), nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get latest preimage metadata")
	}
	var metadata *PreimageMetadata
	if err = json.Unmarshal(response, &metadata); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal latest preimage metadata")
	}
	return metadata, nil
}

// ListPreimageMetadata returns preimage metadata list between trustedHeight and latestHeight.
// sorted ascending by claimed height.
func (c *L2Client) ListPreimageMetadata(ctx context.Context, trustedHeight uint64, latestHeight uint64) ([]*PreimageMetadata, error) {
	type Request struct {
		LtClaimed uint64 `json:"lt_claimed"`
		GtClaimed uint64 `json:"gt_claimed"`
	}
	request := &Request{
		LtClaimed: latestHeight + 1,
		GtClaimed: trustedHeight,
	}
	response, err := c.preimageMakerHttpClient.POST(ctx, fmt.Sprintf("%s/list_metadata", c.preimageMakerEndpoint.Get()), request)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list preimage data")
	}

	var preimageDataList []*PreimageMetadata
	if err = json.Unmarshal(response, &preimageDataList); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal preimage data")
	}
	return preimageDataList, nil
}

func (c *L2Client) GetPreimage(ctx context.Context, request *PreimageMetadata) ([]byte, error) {
	preimageData, err := c.preimageMakerHttpClient.POST(ctx, fmt.Sprintf("%s/get_preimage", c.preimageMakerEndpoint.Get()), request)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read preimage data")
	}
	return preimageData, nil
}

func (c *L2Client) TimestampAt(ctx context.Context, number uint64) (uint64, error) {
	header, err := c.Chain.Client().HeaderByNumber(ctx, util.NewBigInt(number))
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
		util.NewBigInt(blockNumber),
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
	storageKeyHex, err := lcrelay.IBCCommitmentStorageKey(path).MarshalText()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to marshal storage key: path=%x, height=%d", path, height)
	}

	// call eth_getProof
	stateProof, err := c.Chain.Client().GetProof(
		ctx,
		c.Chain.Config().IBCAddress(),
		[][]byte{storageKeyHex},
		big.NewInt(height),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get storage proof: path=%x, height=%d", path, height)
	}
	return stateProof.StorageProofRLP[0], nil
}
