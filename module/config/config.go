package config

import (
	"context"
	"fmt"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
)

var _ core.ProverConfig = (*ProverConfig)(nil)

func (c *ProverConfig) Build(chain core.Chain) (core.Prover, error) {
	l2Chain, ok := chain.(*ethereum.Chain)
	if !ok {
		return nil, fmt.Errorf("chain type must be %T, not %T", &ethereum.Chain{}, chain)
	}
	logger := log.GetLogger().WithChain(l2Chain.ChainID()).WithModule(prover.ModuleName)
	l1Client, err := l1.NewL1Client(context.Background(), c.L1BeaconEndpoint, c.L1ExecutionEndpoint, logger)
	if err != nil {
		return nil, err
	}
	l2Client := l2.NewL2Client(l2Chain, c.L1ExecutionEndpoint,
		c.PreimageMakerTimeout,
		c.PreimageMakerEndpoint,
		c.OpNodeEndpoint,
		logger,
	)
	return prover.NewProver(l2Chain, l1Client, l2Client, c.TrustingPeriod, c.RefreshThresholdRate, c.MaxClockDrift, c.MaxHeaderConcurrency, c.MaxL2NumsForPreimage, logger), nil
}

func (c *ProverConfig) Validate() error {
	return nil
}
