package module

import (
	"context"
	"github.com/cockroachdb/errors"
	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	log2 "github.com/ethereum/go-ethereum/log"
	"github.com/hyperledger-labs/yui-relayer/log"
)

type L2Client struct {
	rollupClient dial.RollupClientInterface
}

func NewL2Client(ctx context.Context, config *ProverConfig) (*L2Client, error) {
	logger := log2.NewLogger(log.GetLogger().Logger.Handler())
	rpc, err := client.NewRPC(ctx, logger, config.OpNodeEndpoint)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &L2Client{
		rollupClient: sources.NewRollupClient(rpc),
	}, nil
}

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
