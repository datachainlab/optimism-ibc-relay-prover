package l2

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	lctypes "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/protolambda/zrnt/eth2/util/math"
	"golang.org/x/sync/errgroup"
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
	l1ExecutionClient             *ethclient.Client
	preimageMakerTimeout          time.Duration
	preimageMakerEndpoint         string
	opNodeEndpoint                string
	maxL2NumsForPreimage          uint64
	maxConcurrentPreimageRequests uint64
}

func NewL2Client(chain *ethereum.Chain,
	l1ExecutionEndpoint string,
	preimageMakerTimeout time.Duration,
	preimageMakerEndpoint string,
	opNodeEndpoint string,
	maxL2NumsForPreimage uint64,
	maxConcurrentPreimageRequests uint64,
) *L2Client {
	l1ExecutionClient, err := ethclient.Dial(l1ExecutionEndpoint)
	if err != nil {
		panic(err)
	}
	return &L2Client{
		Chain:                         chain,
		l1ExecutionClient:             l1ExecutionClient,
		preimageMakerTimeout:          preimageMakerTimeout,
		preimageMakerEndpoint:         preimageMakerEndpoint,
		opNodeEndpoint:                opNodeEndpoint,
		maxL2NumsForPreimage:          math.MaxU64(maxL2NumsForPreimage, 100),
		maxConcurrentPreimageRequests: math.MaxU64(maxConcurrentPreimageRequests, 1),
	}
}

func (c *L2Client) LatestFinalizedHeight(ctx context.Context) (ibcexported.Height, error) {
	syncStatus, err := c.SyncStatus(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	finalizedHeight := syncStatus.FinalizedL2.Number
	return clienttypes.NewHeight(0, finalizedHeight), nil
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

type headerWithPreimageRequest struct {
	Header          *types.Header
	PreimageRequest *PreimageRequest
}

// SplitHeaders splits the headers between the trusted L2 block and the latest L2 block.
// It iterates through the blocks, creating account updates, generating preimages, and constructing headers.
func (c *L2Client) SplitHeaders(ctx context.Context, trustedL2 *OutputResponse, latestHeader *types.Header, trustedToDeterministic, deterministicToLatest []*types.L1Header) ([]core.Header, error) {
	logger := log.GetLogger()
	var targetHeaders []core.Header

	nextTrusted := trustedL2
	latestL1Header := deterministicToLatest[len(deterministicToLatest)-1]
	logger.Info("split headers for ", "trustedL2", trustedL2.BlockRef.Number, "latestL2Header", latestHeader.Derivation.L2BlockNumber)

	preimageRequests := make([]*headerWithPreimageRequest, 0)
	for start := trustedL2.BlockRef.Number + c.maxL2NumsForPreimage; start < latestHeader.Derivation.L2BlockNumber; start += c.maxL2NumsForPreimage {
		accountUpdate, err := c.BuildAccountUpdate(ctx, start)
		if err != nil {
			return nil, err
		}

		claimingOutput, err := c.OutputAtBlock(ctx, start)
		if err != nil {
			return nil, err
		}

		// set up header
		t := clienttypes.NewHeight(latestHeader.GetHeight().GetRevisionNumber(), nextTrusted.BlockRef.Number)
		header := &types.Header{
			TrustedHeight: &t,
			AccountUpdate: accountUpdate,
			Derivation: &types.Derivation{
				AgreedL2OutputRoot: nextTrusted.OutputRoot[:],
				L2OutputRoot:       claimingOutput.OutputRoot[:],
				L2BlockNumber:      claimingOutput.BlockRef.Number,
			},
			DeterministicToLatest: deterministicToLatest,
		}
		targetHeaders = append(targetHeaders, header)

		// Later concurrent preimage call
		preimageRequests = append(preimageRequests, &headerWithPreimageRequest{
			Header: header,
			PreimageRequest: &PreimageRequest{
				L1HeadHash:         common.BytesToHash(latestL1Header.ExecutionUpdate.BlockHash),
				AgreedL2HeadHash:   nextTrusted.BlockRef.Hash,
				AgreedL2OutputRoot: common.BytesToHash(nextTrusted.OutputRoot[:]),
				L2OutputRoot:       common.BytesToHash(claimingOutput.OutputRoot[:]),
				L2BlockNumber:      claimingOutput.BlockRef.Number,
			},
		})

		// set up next loop
		nextTrusted = claimingOutput
	}

	// set up latest header
	t := clienttypes.NewHeight(latestHeader.GetHeight().GetRevisionNumber(), nextTrusted.BlockRef.Number)
	latestHeader.TrustedHeight = &t
	latestHeader.DeterministicToLatest = deterministicToLatest
	latestHeader.Derivation.AgreedL2OutputRoot = nextTrusted.OutputRoot[:]
	targetHeaders = append(targetHeaders, latestHeader)

	// Later concurrent preimage call
	preimageRequests = append(preimageRequests, &headerWithPreimageRequest{
		Header: latestHeader,
		PreimageRequest: &PreimageRequest{
			L1HeadHash:         common.BytesToHash(latestL1Header.ExecutionUpdate.BlockHash),
			AgreedL2HeadHash:   nextTrusted.BlockRef.Hash,
			AgreedL2OutputRoot: common.BytesToHash(nextTrusted.OutputRoot[:]),
			L2OutputRoot:       common.BytesToHash(latestHeader.Derivation.L2OutputRoot[:]),
			L2BlockNumber:      latestHeader.Derivation.L2BlockNumber,
		},
	})

	// set preimage data
	if err := c.setPreimageDataIntoHeaders(ctx, preimageRequests); err != nil {
		return nil, errors.WithStack(err)
	}

	for i, h := range targetHeaders {
		ih := h.(*types.Header)

		// Only first header needs trustedToDeterministic
		if i == 0 {
			ih.TrustedToDeterministic = trustedToDeterministic
		} else {
			ih.TrustedToDeterministic = nil
		}

		logger.Info("targetHeaders", "l2", ih.Derivation.L2BlockNumber, "trusted_l2", ih.TrustedHeight.GetRevisionHeight(), "l1_t2d", len(ih.TrustedToDeterministic), "l1_d2l", len(ih.DeterministicToLatest), "preimages", len(ih.Preimages))
	}

	return targetHeaders, nil

}

func (c *L2Client) setPreimageDataIntoHeaders(ctx context.Context, headers []*headerWithPreimageRequest) error {
	logger := log.GetLogger()
	for _, header := range headers {
		logger.Info("target preimageRequest", "l2", header.PreimageRequest.L2BlockNumber)
	}
	return callAndWaitParallel(ctx, headers, c.maxConcurrentPreimageRequests, func(ctx context.Context, r *headerWithPreimageRequest) error {
		logger.Info("start preimageRequest", "l2", r.PreimageRequest.L2BlockNumber)
		preimageData, err := c.CreatePreimages(ctx, r.PreimageRequest)
		if err != nil {
			return errors.Wrapf(err, "failed to create preimages for L2 header %d", r.PreimageRequest.L2BlockNumber)
		}
		logger.Info("success preimageRequest", "l2", r.PreimageRequest.L2BlockNumber, "preimageSize", len(preimageData))
		r.Header.Preimages = preimageData
		return nil
	})
}

func callAndWaitParallel[T any](ctx context.Context, requests []*T, maxConcurrency uint64, fn func(ctx context.Context, r *T) error) error {
	sem := make(chan struct{}, maxConcurrency)
	g, ctx := errgroup.WithContext(ctx)

	for _, header := range requests {
		sem <- struct{}{}
		g.Go(func() error {
			defer func() { <-sem }()
			return fn(ctx, header)
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}
	return nil
}
