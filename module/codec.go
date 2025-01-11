package module

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/config"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/hyperledger-labs/yui-relayer/core"
)

// RegisterInterfaces register the module interfaces to protobuf Any.
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*core.ProverConfig)(nil),
		&config.ProverConfig{},
	)
	registry.RegisterImplementations(
		(*exported.ClientState)(nil),
		&types.ClientState{},
	)
	registry.RegisterImplementations(
		(*exported.ConsensusState)(nil),
		&types.ConsensusState{},
	)
	codec.RegisterInterfaces(registry)
}
