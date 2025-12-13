package config

import (
	"context"
	"fmt"
	"time"

	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/coreutil"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/hyperledger-labs/yui-relayer/otelcore"
	"go.opentelemetry.io/otel"

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
	l1Client, err := l1.NewL1Client(
		context.Background(),
		c.L1BeaconEndpoint,
		c.L1ExecutionEndpoint,
		c.PreimageMakerTimeout,
		c.PreimageMakerEndpoint,
		c.MinimalForkSched,
		logger,
	)
	if err != nil {
		return nil, err
	}
	l2Client := l2.NewL2Client(
		l2Chain,
		c.OpNodeTimeout,
		c.PreimageMakerTimeout,
		c.PreimageMakerEndpoint,
		c.OpNodeEndpoint,
		logger,
	)
	inner := prover.NewProver(l2Chain, l1Client, l2Client,
		c.TrustingPeriod, c.RefreshThresholdRate, c.MaxClockDrift, c.MaxHeaderConcurrency,
		common.HexToAddress(c.DisputeGameFactoryAddress), logger)
	tracer := otel.Tracer("github.com/datachainlab/optimism-ibc-relay-prover/module")
	return otelcore.NewProver(inner, l2Chain.ChainID(), tracer), nil
}

func (c *ProverConfig) Validate() error {
	if c.L1BeaconEndpoint == "" {
		return fmt.Errorf("L1BeaconEndpoint is required")
	}
	if c.L1ExecutionEndpoint == "" {
		return fmt.Errorf("L1ExecutionEndpoint is required")
	}
	if c.OpNodeEndpoint == "" {
		return fmt.Errorf("OpNodeEndpoint is required")
	}
	if c.OpNodeTimeout <= time.Duration(0) {
		return fmt.Errorf("OpNodeTimeout must be greater than 0")
	}
	if c.TrustingPeriod <= time.Duration(0) {
		return fmt.Errorf("TrustingPeriod must be greater than 0")
	}
	if c.PreimageMakerTimeout <= time.Duration(0) {
		return fmt.Errorf("PreimageMakerTimeout must be greater than 0")
	}
	if c.DisputeGameFactoryAddress == "" {
		return fmt.Errorf("DisputeGameFactoryAddress is required")
	}
	if c.RefreshThresholdRate == nil {
		return fmt.Errorf("config attribute \"refresh_threshold_rate\" is required")
	}
	if c.RefreshThresholdRate.Denominator == 0 {
		return fmt.Errorf("config attribute \"refresh_threshold_rate.denominator\" must not be zero")
	}
	if c.RefreshThresholdRate.Numerator == 0 {
		return fmt.Errorf("config attribute \"refresh_threshold_rate.numerator\" must not be zero")
	}
	if c.RefreshThresholdRate.Numerator > c.RefreshThresholdRate.Denominator {
		return fmt.Errorf("config attribute \"refresh_threshold_rate\" must be less than or equal to 1.0: actual=%v/%v", c.RefreshThresholdRate.Numerator, c.RefreshThresholdRate.Denominator)
	}
	return nil
}
