package module

import (
	"fmt"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/hyperledger-labs/yui-relayer/core"
)

var _ core.ProverConfig = (*ProverConfig)(nil)

func (c *ProverConfig) Build(chain core.Chain) (core.Prover, error) {
	l2Chain, ok := chain.(*ethereum.Chain)
	if !ok {
		return nil, fmt.Errorf("chain type must be %T, not %T", &ethereum.Chain{}, chain)
	}
	return NewProver(l2Chain, *c), nil
}

func (c *ProverConfig) Validate() error {
	return nil
}
