package l1

import (
	"fmt"
	lctypes "github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
)

const (
	Mainnet = "mainnet"
	Minimal = "minimal"
	Goerli  = "goerli"
	Sepolia = "sepolia"
)

const (
	MAINNET_PRESET_SYNC_COMMITTEE_SIZE = 512
	MINIMAL_PRESET_SYNC_COMMITTEE_SIZE = 32
)

var (
	AltairSpec = lctypes.ForkSpec{
		FinalizedRootGindex:        105,
		CurrentSyncCommitteeGindex: 54,
		NextSyncCommitteeGindex:    55,
	}
	BellatrixSpec = lctypes.ForkSpec{
		FinalizedRootGindex:               AltairSpec.FinalizedRootGindex,
		CurrentSyncCommitteeGindex:        AltairSpec.CurrentSyncCommitteeGindex,
		NextSyncCommitteeGindex:           AltairSpec.NextSyncCommitteeGindex,
		ExecutionPayloadGindex:            25,
		ExecutionPayloadStateRootGindex:   18,
		ExecutionPayloadBlockNumberGindex: 22,
	}
	CapellaSpec = BellatrixSpec
	DenebSpec   = lctypes.ForkSpec{
		FinalizedRootGindex:               CapellaSpec.FinalizedRootGindex,
		CurrentSyncCommitteeGindex:        CapellaSpec.CurrentSyncCommitteeGindex,
		NextSyncCommitteeGindex:           CapellaSpec.NextSyncCommitteeGindex,
		ExecutionPayloadGindex:            CapellaSpec.ExecutionPayloadGindex,
		ExecutionPayloadStateRootGindex:   34,
		ExecutionPayloadBlockNumberGindex: 38,
	}
)

type ProverConfig struct {
	Network string
}

func (prc *ProverConfig) IsMainnetPreset() bool {
	switch prc.Network {
	case Mainnet, Goerli, Sepolia:
		return true
	case Minimal:
		return false
	default:
		panic(fmt.Sprintf("unknown network: %v", prc.Network))
	}
}

func (prc *ProverConfig) getForkParameters() *lctypes.ForkParameters {
	switch prc.Network {
	case Mainnet:
		return &lctypes.ForkParameters{
			GenesisForkVersion: []byte{0, 0, 0, 0},
			Forks: []*lctypes.Fork{
				{
					Version: []byte{1, 0, 0, 0},
					Epoch:   74240,
					Spec:    &AltairSpec,
				},
				{
					Version: []byte{2, 0, 0, 0},
					Epoch:   144896,
					Spec:    &BellatrixSpec,
				},
				{
					Version: []byte{3, 0, 0, 0},
					Epoch:   194048,
					Spec:    &CapellaSpec,
				},
				{
					Version: []byte{4, 0, 0, 0},
					Epoch:   269568,
					Spec:    &DenebSpec,
				},
			},
		}
	case Minimal:
		return &lctypes.ForkParameters{
			GenesisForkVersion: []byte{0, 0, 0, 1},
			Forks: []*lctypes.Fork{
				{
					Version: []byte{1, 0, 0, 1},
					Epoch:   0,
					Spec:    &AltairSpec,
				},
				{
					Version: []byte{2, 0, 0, 1},
					Epoch:   0,
					Spec:    &BellatrixSpec,
				},
				{
					Version: []byte{3, 0, 0, 1},
					Epoch:   0,
					Spec:    &CapellaSpec,
				},
				{
					Version: []byte{4, 0, 0, 1},
					Epoch:   0,
					Spec:    &DenebSpec,
				},
			},
		}
	case Goerli:
		return &lctypes.ForkParameters{
			GenesisForkVersion: []byte{0, 0, 16, 32},
			Forks: []*lctypes.Fork{
				{
					Version: []byte{1, 0, 16, 32},
					Epoch:   36660,
					Spec:    &AltairSpec,
				},
				{
					Version: []byte{2, 0, 16, 32},
					Epoch:   112260,
					Spec:    &BellatrixSpec,
				},
				{
					Version: []byte{3, 0, 16, 32},
					Epoch:   162304,
					Spec:    &CapellaSpec,
				},
				{
					Version: []byte{4, 0, 16, 32},
					Epoch:   231680,
					Spec:    &DenebSpec,
				},
			},
		}
	case Sepolia:
		return &lctypes.ForkParameters{
			GenesisForkVersion: []byte{144, 0, 0, 105},
			Forks: []*lctypes.Fork{
				{
					Version: []byte{144, 0, 0, 112},
					Epoch:   50,
					Spec:    &AltairSpec,
				},
				{
					Version: []byte{144, 0, 0, 113},
					Epoch:   100,
					Spec:    &BellatrixSpec,
				},
				{
					Version: []byte{144, 0, 0, 114},
					Epoch:   56832,
					Spec:    &CapellaSpec,
				},
				{
					Version: []byte{144, 0, 0, 115},
					Epoch:   132608,
					Spec:    &DenebSpec,
				},
			},
		}
	default:
		panic(fmt.Sprintf("unknown network: %v", prc.Network))
	}
}
