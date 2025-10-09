package l1

import (
	"fmt"
	lctypes "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
)

type ProverConfig struct {
	Network          string            `protobuf:"bytes,2,opt,name=network,proto3" json:"network,omitempty"`
	MinimalForkSched map[string]uint64 `protobuf:"bytes,6,rep,name=minimal_fork_sched,json=minimalForkSched,proto3" json:"minimal_fork_sched,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"varint,2,opt,name=value,proto3"`
}

const (
	Mainnet = "mainnet"
	Minimal = "minimal"
	Sepolia = "sepolia"
)

const (
	MAINNET_PRESET_SYNC_COMMITTEE_SIZE = 512
	MINIMAL_PRESET_SYNC_COMMITTEE_SIZE = 32
)

const (
	Altair    = "altair"
	Bellatrix = "bellatrix"
	Capella   = "capella"
	Deneb     = "deneb"
	Electra   = "electra"
	Fulu      = "fulu"
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
	ElectraSpec = lctypes.ForkSpec{
		FinalizedRootGindex:               169,
		CurrentSyncCommitteeGindex:        86,
		NextSyncCommitteeGindex:           87,
		ExecutionPayloadGindex:            DenebSpec.ExecutionPayloadGindex,
		ExecutionPayloadStateRootGindex:   DenebSpec.ExecutionPayloadStateRootGindex,
		ExecutionPayloadBlockNumberGindex: DenebSpec.ExecutionPayloadBlockNumberGindex,
	}
	FuluSpec = ElectraSpec
)

// NOTE the prover supports only the mainnet and minimal preset for now
func (prc *ProverConfig) IsMainnetPreset() bool {
	switch prc.Network {
	case Mainnet, Sepolia:
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
				{
					Version: []byte{5, 0, 0, 0},
					Epoch:   364032,
					Spec:    &ElectraSpec,
				},
				{
					Version: []byte{6, 0, 0, 0},
					Epoch:   411392,
					Spec:    &FuluSpec,
				},
			},
		}
	case Minimal:
		// https://github.com/ethpandaops/ethereum-package/blob/8f8830fd1992db4e5678c125bc400e310d5b6006/src/package_io/constants.star#L110
		return &lctypes.ForkParameters{
			GenesisForkVersion: common.Hex2Bytes("10000038"),
			Forks: []*lctypes.Fork{
				{
					Version: common.Hex2Bytes("20000038"),
					Epoch:   prc.MinimalForkSched[Altair],
					Spec:    &AltairSpec,
				},
				{
					Version: common.Hex2Bytes("30000038"),
					Epoch:   prc.MinimalForkSched[Bellatrix],
					Spec:    &BellatrixSpec,
				},
				{
					Version: common.Hex2Bytes("40000038"),
					Epoch:   prc.MinimalForkSched[Capella],
					Spec:    &CapellaSpec,
				},
				{
					Version: common.Hex2Bytes("50000038"),
					Epoch:   prc.MinimalForkSched[Deneb],
					Spec:    &DenebSpec,
				},
				{
					Version: common.Hex2Bytes("60000038"),
					Epoch:   prc.MinimalForkSched[Electra],
					Spec:    &ElectraSpec,
				},
				{
					Version: common.Hex2Bytes("70000038"),
					Epoch:   prc.MinimalForkSched[Fulu],
					Spec:    &FuluSpec,
				},
			},
		}
	case Sepolia:
		// https://github.com/ChainSafe/lodestar/tree/unstable/packages/config/src/chainConfig/networks
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
				{
					Version: []byte{144, 0, 0, 116},
					Epoch:   222464,
					Spec:    &ElectraSpec,
				},
				{
					Version: []byte{144, 0, 0, 117},
					Epoch:   272640,
					Spec:    &FuluSpec,
				},
			},
		}
	default:
		panic(fmt.Sprintf("unknown network: %v", prc.Network))
	}
}
