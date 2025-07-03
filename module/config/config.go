package config

import (
	"context"

	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/coreutil"
	"github.com/hyperledger-labs/yui-relayer/log"

	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
)

var _ core.ProverConfig = (*ProverConfig)(nil)

func (c *ProverConfig) Build(chain core.Chain) (core.Prover, error) {
	l2Chain, err := coreutil.UnwrapChain[*ethereum.Chain](chain)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}

	logger := log.GetLogger().WithChain(l2Chain.ChainID()).WithModule(prover.ModuleName)
	l1Client, err := l1.NewL1Client(context.Background(), c.L1BeaconEndpoint, c.L1ExecutionEndpoint, logger)
	if err != nil {
		return nil, err
	}
	l2Client := l2.NewL2Client(l2Chain, c.L1ExecutionEndpoint,
		c.PreimageMakerTimeout,
		c.OpNodeTimeout,
		c.OpNodeEndpoint,
		logger,
	)
	return prover.NewProver(l2Chain, l1Client, l2Client,
		c.TrustingPeriod, c.RefreshThresholdRate, c.MaxClockDrift, c.MaxHeaderConcurrency, c.MaxL2NumsForPreimage,
		common.HexToAddress(c.DisputeGameFactoryAddress),
		logger), nil
}

func (c *ProverConfig) Validate() error {
	return nil
}
