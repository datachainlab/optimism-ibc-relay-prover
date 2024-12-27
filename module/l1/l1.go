package l1

import (
	"bytes"
	"context"
	"fmt"
	"github.com/cockroachdb/errors"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/ethereum-ibc-relay-prover/beacon"
	lctypes "github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module"
	"github.com/ethereum/go-ethereum/ethclient"
	"math/big"
)

type InitialState struct {
	Genesis              beacon.Genesis
	Slot                 uint64
	BlockNumber          uint64
	CurrentSyncCommittee lctypes.SyncCommittee
	NextSyncCommittee    lctypes.SyncCommittee
}

type L1Client struct {
	beaconClient    beacon.Client
	executionClient *ethclient.Client
	config          *ProverConfig
}

func (pr *L1Client) BuildL1Config(state *InitialState) (*module.L1Config, error) {
	return &module.L1Config{
		GenesisValidatorsRoot:        state.Genesis.GenesisValidatorsRoot[:],
		MinSyncCommitteeParticipants: 1,
		GenesisTime:                  state.Genesis.GenesisTimeSeconds,
		ForkParameters:               pr.config.getForkParameters(),
		SecondsPerSlot:               pr.secondsPerSlot(),
		SlotsPerEpoch:                pr.slotsPerEpoch(),
		EpochsPerSyncCommitteePeriod: pr.epochsPerSyncCommitteePeriod(),
		TrustLevel: &lctypes.Fraction{
			Numerator:   2,
			Denominator: 3,
		},
	}, nil
}

func (pr *L1Client) BuildInitialState(blockNumber uint64) (*InitialState, error) {

	header, err := pr.executionClient.HeaderByNumber(context.Background(), big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	timestamp := header.Time

	slot, err := pr.getSlotAtTimestamp(timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to compute slot at timestamp: %v", err)
	}

	period := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))

	currentSyncCommittee, err := pr.getBootstrapInPeriod(period)
	if err != nil {
		return nil, fmt.Errorf("failed to get bootstrap in period %v: %v", period, err)
	}
	genesis, err := pr.beaconClient.GetGenesis()
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis: %v", err)
	}
	res2, err := pr.beaconClient.GetLightClientUpdate(period)
	if err != nil {
		return nil, fmt.Errorf("failed to get LightClientUpdate: period=%v %v", period, err)
	}
	nextSyncCommittee := res2.Data.ToProto().NextSyncCommittee
	return &InitialState{
		Genesis:              *genesis,
		Slot:                 slot,
		BlockNumber:          blockNumber,
		CurrentSyncCommittee: *currentSyncCommittee,
		NextSyncCommittee:    *nextSyncCommittee,
	}, nil
}

func (pr *L1Client) getBootstrapInPeriod(period uint64) (*lctypes.SyncCommittee, error) {
	slotsPerEpoch := pr.slotsPerEpoch()
	startSlot := pr.getPeriodBoundarySlot(period)
	lastSlotInPeriod := pr.getPeriodBoundarySlot(period+1) - 1
	var errs []error
	for i := startSlot + slotsPerEpoch; i <= lastSlotInPeriod; i += slotsPerEpoch {
		res, err := pr.beaconClient.GetBlockRoot(i, false)
		if err != nil {
			errs = append(errs, err)
			return nil, fmt.Errorf("there is no available bootstrap in period: period=%v err=%v", period, errors.Join(errs...))
		}
		bootstrap, err := pr.beaconClient.GetBootstrap(res.Data.Root[:])
		if err != nil {
			errs = append(errs, err)
			continue
		} else {
			return bootstrap.Data.CurrentSyncCommittee.ToProto(), nil
		}
	}
	return nil, fmt.Errorf("failed to get bootstrap in period: period=%v err=%v", period, errors.Join(errs...))
}

func (pr *L1Client) buildNextSyncCommitteeUpdate(period uint64, trustedHeight clienttypes.Height, trustedNextSyncCommittee *lctypes.SyncCommittee) (*module.L1Header, error) {
	res, err := pr.beaconClient.GetLightClientUpdate(period)
	if err != nil {
		return nil, err
	}
	lcUpdate := res.Data.ToProto()
	executionHeader := &res.Data.FinalizedHeader.Execution
	executionUpdate, err := pr.buildExecutionUpdate(executionHeader)
	if err != nil {
		return nil, fmt.Errorf("failed to build execution update: %v", err)
	}
	executionRoot, err := executionHeader.HashTreeRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate execution root: %v", err)
	}
	if !bytes.Equal(executionRoot[:], lcUpdate.FinalizedExecutionRoot) {
		return nil, fmt.Errorf("execution root mismatch: %X != %X", executionRoot, lcUpdate.FinalizedExecutionRoot)
	}

	return &module.L1Header{
		TrustedSyncCommittee: &lctypes.TrustedSyncCommittee{
			TrustedHeight: &trustedHeight,
			SyncCommittee: trustedNextSyncCommittee,
			IsNext:        true,
		},
		ConsensusUpdate: lcUpdate,
		ExecutionUpdate: executionUpdate,
	}, nil
}

func (pr *L1Client) newHeight(blockNumber uint64) clienttypes.Height {
	return clienttypes.NewHeight(0, blockNumber)
}

func NewL1Client(ctx context.Context, config *module.ProverConfig) (*L1Client, error) {
	beaconClient := beacon.NewClient(config.L1BeaconEndpoint)
	executionClient, err := ethclient.DialContext(ctx, config.L1ExecutionEndpoint)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	chainID, err := executionClient.ChainID(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	network := Minimal
	if chainID.Uint64() == 1 {
		network = Mainnet
	} else if chainID.Uint64() == 5 {
		network = Goerli
	} else if chainID.Uint64() == 11155111 {
		network = Sepolia
	}

	return &L1Client{
		beaconClient:    beaconClient,
		executionClient: executionClient,
		config: &ProverConfig{
			Network: network,
		},
	}, nil
}
