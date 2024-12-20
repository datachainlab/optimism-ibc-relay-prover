package module

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover"
	"github.com/hyperledger-labs/yui-relayer/config"
	"github.com/spf13/cobra"
)

type Module struct{}

var _ config.ModuleI = (*Module)(nil)

// Name returns the name of the module
func (Module) Name() string {
	return prover.ModuleName
}

// RegisterInterfaces register the module interfaces to protobuf Any.
func (Module) RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	RegisterInterfaces(registry)
}

// GetCmd returns the command
func (Module) GetCmd(ctx *config.Context) *cobra.Command {
	return nil
}
