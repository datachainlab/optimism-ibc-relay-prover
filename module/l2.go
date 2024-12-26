package module

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	log2 "github.com/ethereum/go-ethereum/log"
	"github.com/hyperledger-labs/yui-relayer/log"
	"io"
	"net/http"
)

type L2Client struct {
	rollupClient dial.RollupClientInterface
	config       *ProverConfig
}

func NewL2Client(ctx context.Context, config *ProverConfig) (*L2Client, error) {
	logger := log2.NewLogger(log.GetLogger().Logger.Handler())
	rpc, err := client.NewRPC(ctx, logger, config.OpNodeEndpoint)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &L2Client{
		rollupClient: sources.NewRollupClient(rpc),
		config:       config,
	}, nil
}

// LatestDerivation retrieves the latest derivation information from the rollup client.
// It fetches the sync status, claimed output, and agreed output for the latest blocks.
func (c *L2Client) LatestDerivation(ctx context.Context) (*eth.L1BlockRef, *Derivation, error) {
	syncStatus, err := c.rollupClient.SyncStatus(ctx)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	claimed := syncStatus.FinalizedL2
	claimedNumber := claimed.Number
	agreedNumber := claimedNumber - 1

	claimedOutput, err := c.rollupClient.OutputAtBlock(ctx, claimedNumber)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	agreedOutput, err := c.rollupClient.OutputAtBlock(ctx, agreedNumber)
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
		agreedOutput, err := c.rollupClient.OutputAtBlock(ctx, agreedNumber)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		claimedNumber := agreedNumber + 1
		claimedOutput, err := c.rollupClient.OutputAtBlock(ctx, claimedNumber)
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
