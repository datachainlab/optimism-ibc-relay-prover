package config

import (
	"context"
	"go.opentelemetry.io/otel"

	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/coreutil"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/hyperledger-labs/yui-relayer/otelcore"

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

	logger := log.GetLogger().WithChain(l2Chain.ChainID()).WithModule(prover.ModuleName)
	l1Client, err := l1.NewL1Client(context.Background(), c.L1BeaconEndpoint, c.L1ExecutionEndpoint, logger)
	if err != nil {
		return nil, err
	}
	l2Client := l2.NewL2Client(
		l2Chain,
		c.L1ExecutionEndpoint,
		c.OpNodeTimeout,
		c.PreimageMakerTimeout,
		c.PreimageMakerEndpoint,
		c.OpNodeEndpoint,
		logger,
	)
	inner := prover.NewProver(l2Chain, l1Client, l2Client,
		c.TrustingPeriod, c.RefreshThresholdRate, c.MaxClockDrift, c.MaxHeaderConcurrency, c.MaxL2NumsForPreimage,
		common.HexToAddress(c.DisputeGameFactoryAddress),
		logger)
	tracer := otel.Tracer("github.com/datachainlab/optimism-ibc-relay-prover/module")
	return otelcore.NewProver(inner, l2Chain.ChainID(), tracer), nil
}

func (c *ProverConfig) Validate() error {
	return nil
}
