package l1

import (
	"bytes"
	"context"
	"fmt"
	"github.com/cockroachdb/errors"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1/beacon"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	lctypes "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/log"
	"math/big"
)

type InitialState struct {
	Genesis              beacon.Genesis
	Slot                 uint64
	Period               uint64
	CurrentSyncCommittee lctypes.SyncCommittee
	NextSyncCommittee    lctypes.SyncCommittee
}

type L1Client struct {
	beaconClient    beacon.Client
	executionClient *ethclient.Client
	config          *ProverConfig
}

func (pr *L1Client) BuildL1Config(state *InitialState) (*types.L1Config, error) {
	return &types.L1Config{
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

func (pr *L1Client) GetLatestFinalizedL1Header(ctx context.Context) (*types.L1Header, error) {
	res, err := pr.beaconClient.GetLightClientFinalityUpdate(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
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
	return &types.L1Header{
		ConsensusUpdate: lcUpdate,
		ExecutionUpdate: executionUpdate,
	}, nil
}

func (pr *L1Client) BuildInitialState(ctx context.Context, blockNumber uint64) (*InitialState, error) {

	timestamp, err := pr.TimestampAt(ctx, blockNumber)
	if err != nil {
		return nil, err
	}
	slot, err := pr.getSlotAtTimestamp(ctx, timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to compute slot at timestamp: %v", err)
	}
	period := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))

	currentSyncCommittee, err := pr.getBootstrapInPeriod(ctx, period)
	if err != nil {
		return nil, fmt.Errorf("failed to get bootstrap in period %v: %v", period, err)
	}
	res2, err := pr.beaconClient.GetLightClientUpdate(ctx, period)
	if err != nil {
		return nil, fmt.Errorf("failed to get LightClientUpdate: period=%v %v", period, err)
	}
	nextSyncCommittee := res2.Data.ToProto().NextSyncCommittee

	genesis, err := pr.beaconClient.GetGenesis(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis: %v", err)
	}

	return &InitialState{
		Genesis:              *genesis,
		Slot:                 slot,
		Period:               period,
		CurrentSyncCommittee: *currentSyncCommittee,
		NextSyncCommittee:    *nextSyncCommittee,
	}, nil
}

func (pr *L1Client) GetConsensusHeaderByBlockNumber(ctx context.Context, blockNumber uint64) (*types.L1Header, error) {
	timestamp, err := pr.TimestampAt(ctx, blockNumber)
	if err != nil {
		return nil, err
	}
	slot, err := pr.getSlotAtTimestamp(ctx, timestamp)
	if err != nil {
		return nil, err
	}
	period := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))
	res, err := pr.BuildNextSyncCommitteeUpdate(ctx, period, nil)
	return res, err
}

func (pr *L1Client) GetSyncCommitteesFromTrustedToLatest(ctx context.Context, trustedBlockNumber uint64, lfh *types.L1Header) ([]*types.L1Header, error) {
	timestamp, err := pr.TimestampAt(ctx, trustedBlockNumber)
	if err != nil {
		return nil, err
	}
	slot, err := pr.getSlotAtTimestamp(ctx, timestamp)
	if err != nil {
		return nil, err
	}
	statePeriod := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))
	latestPeriod := pr.computeSyncCommitteePeriod(pr.computeEpoch(lfh.ConsensusUpdate.SignatureSlot))
	log.GetLogger().Debug("GetSyncCommitteesFromTrustedToLatest", "statePeriod", statePeriod, "latestPeriod", latestPeriod)
	res, err := pr.beaconClient.GetLightClientUpdate(ctx, statePeriod)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if statePeriod == latestPeriod {
		root, err := res.Data.FinalizedHeader.Beacon.HashTreeRoot()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		bootstrapRes, err := pr.beaconClient.GetBootstrap(ctx, root[:])
		if err != nil {
			return nil, errors.WithStack(err)
		}
		lfh.TrustedSyncCommittee = &lctypes.TrustedSyncCommittee{
			SyncCommittee: bootstrapRes.Data.CurrentSyncCommittee.ToProto(),
			IsNext:        false,
		}
		return []*types.L1Header{lfh}, nil
	} else if statePeriod > latestPeriod {
		return nil, fmt.Errorf("the light-client server's response is old: client_state_period=%v latest_finalized_period=%v", statePeriod, latestPeriod)
	}

	//--------- In case statePeriod < latestPeriod ---------//

	var (
		headers                     []*types.L1Header
		trustedNextSyncCommittee    *lctypes.SyncCommittee
		trustedCurrentSyncCommittee *lctypes.SyncCommittee
	)
	res, err = pr.beaconClient.GetLightClientUpdate(ctx, statePeriod)
	if err != nil {
		return nil, fmt.Errorf("failed to get LightClientUpdate: state_period=%v %v", statePeriod, err)
	}
	trustedNextSyncCommittee = res.Data.ToProto().NextSyncCommittee
	for p := statePeriod + 1; p <= latestPeriod; p++ {
		header, err := pr.BuildNextSyncCommitteeUpdate(ctx, p, trustedNextSyncCommittee)
		if err != nil {
			return nil, fmt.Errorf("failed to build next sync committee update for next: period=%v %v", p, err)
		}
		trustedCurrentSyncCommittee = trustedNextSyncCommittee
		trustedNextSyncCommittee = header.ConsensusUpdate.NextSyncCommittee
		headers = append(headers, header)
	}
	lfh.TrustedSyncCommittee = &lctypes.TrustedSyncCommittee{
		SyncCommittee: trustedCurrentSyncCommittee,
		IsNext:        false,
	}
	return append(headers, lfh), nil
}

func (pr *L1Client) TimestampAt(ctx context.Context, number uint64) (uint64, error) {
	header, err := pr.executionClient.HeaderByNumber(ctx, big.NewInt(0).SetUint64(number))
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return header.Time, nil
}

func (pr *L1Client) getBootstrapInPeriod(ctx context.Context, period uint64) (*lctypes.SyncCommittee, error) {
	slotsPerEpoch := pr.slotsPerEpoch()
	startSlot := pr.getPeriodBoundarySlot(period)
	lastSlotInPeriod := pr.getPeriodBoundarySlot(period+1) - 1
	var errs []error
	for i := startSlot + slotsPerEpoch; i <= lastSlotInPeriod; i += slotsPerEpoch {
		res, err := pr.beaconClient.GetBlockRoot(ctx, i, false)
		if err != nil {
			errs = append(errs, err)
			return nil, fmt.Errorf("there is no available bootstrap in period: period=%v err=%v", period, errors.Join(errs...))
		}
		bootstrap, err := pr.beaconClient.GetBootstrap(ctx, res.Data.Root[:])
		if err != nil {
			errs = append(errs, err)
			continue
		} else {
			return bootstrap.Data.CurrentSyncCommittee.ToProto(), nil
		}
	}
	return nil, fmt.Errorf("failed to get bootstrap in period: period=%v err=%v", period, errors.Join(errs...))
}

func (pr *L1Client) BuildNextSyncCommitteeUpdate(ctx context.Context, period uint64, trustedNextSyncCommittee *lctypes.SyncCommittee) (*types.L1Header, error) {
	res, err := pr.beaconClient.GetLightClientUpdate(ctx, period)
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

	return &types.L1Header{
		TrustedSyncCommittee: &lctypes.TrustedSyncCommittee{
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

func NewL1Client(ctx context.Context, l1BeaconEndpoint, l1ExecutionEndpoint string) (*L1Client, error) {
	beaconClient := beacon.NewClient(l1BeaconEndpoint)
	executionClient, err := ethclient.DialContext(ctx, l1ExecutionEndpoint)
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
	} else if chainID.Uint64() == 11155111 {
		network = Sepolia
	}

	return &L1Client{
		beaconClient:    beaconClient,
		executionClient: executionClient,
		config: &ProverConfig{
			Network:          network,
			MinimalForkSched: nil,
		},
	}, nil
}
