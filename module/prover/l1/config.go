package l1

import (
	lcrelay "github.com/datachainlab/ethereum-light-client-types/relayer/relay"
	lctypes "github.com/datachainlab/ethereum-light-client-types/relayer/types"
)

type ProverConfig struct {
	Network          string            `protobuf:"bytes,2,opt,name=network,proto3" json:"network,omitempty"`
	MinimalForkSched map[string]uint64 `protobuf:"bytes,6,rep,name=minimal_fork_sched,json=minimalForkSched,proto3" json:"minimal_fork_sched,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"varint,2,opt,name=value,proto3"`
}

func (prc *ProverConfig) getForkParameters() *lctypes.ForkParameters {
	params := lcrelay.GetForkParameters(prc.Network, prc.MinimalForkSched)
	if prc.Network == lcrelay.Minimal {
		// Optimism uses ethpandaops/ethereum-package fork versions
		// which differ from standard minimal preset
		// https://github.com/ethpandaops/ethereum-package/blob/8f8830fd1992db4e5678c125bc400e310d5b6006/src/package_io/constants.star#L110
		params = convertToEthpandaopsForkVersions(params)
	}
	return params
}

// convertToEthpandaopsForkVersions converts standard minimal fork versions to ethpandaops format
// Standard minimal: GenesisForkVersion={0,0,0,1}, Fork.Version={n,0,0,1}
// Ethpandaops:      GenesisForkVersion={0x10,0,0,0x38}, Fork.Version={n*0x10,0,0,0x38}
func convertToEthpandaopsForkVersions(params *lctypes.ForkParameters) *lctypes.ForkParameters {
	// Convert genesis fork version: {0,0,0,1} -> {0x10,0,0,0x38}
	newGenesis := make([]byte, 4)
	copy(newGenesis, params.GenesisForkVersion)
	newGenesis[0] = 0x10
	newGenesis[3] = 0x38

	// Convert each fork version: {n,0,0,1} -> {n*0x10,0,0,0x38}
	newForks := make([]*lctypes.Fork, len(params.Forks))
	for i, fork := range params.Forks {
		newVersion := make([]byte, 4)
		copy(newVersion, fork.Version)
		newVersion[0] = fork.Version[0] * 0x10
		newVersion[3] = 0x38

		newForks[i] = &lctypes.Fork{
			Version: newVersion,
			Epoch:   fork.Epoch,
			Spec:    fork.Spec,
		}
	}

	return &lctypes.ForkParameters{
		GenesisForkVersion: newGenesis,
		Forks:              newForks,
	}
}
