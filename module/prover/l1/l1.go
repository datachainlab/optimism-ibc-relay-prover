package l1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1/beacon"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	lctypes "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/log"
)

type InitialState struct {
	Genesis              beacon.Genesis
	Slot                 uint64
	Period               uint64
	Timestamp            uint64
	CurrentSyncCommittee lctypes.SyncCommittee
	NextSyncCommittee    lctypes.SyncCommittee
}

type L1Client struct {
	beaconClient            beacon.Client
	executionClient         *ethclient.Client
	preimageMakerHttpClient *util.HTTPClient
	preimageMakerEndpoint   *util.Selector[string]
	config                  *ProverConfig
	logger                  *log.RelayLogger
}

func (pr *L1Client) BuildL1Config(state *InitialState, maxClockDrift, trustingPeriod time.Duration) (*types.L1Config, error) {
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
		MaxClockDrift:  maxClockDrift,
		TrustingPeriod: trustingPeriod,
	}, nil
}

func (pr *L1Client) GetLatestETHHeader(ctx context.Context) (*ethtypes.Header, error) {
	return pr.executionClient.HeaderByNumber(ctx, nil)
}

func (pr *L1Client) GetFinalizedL1Header(ctx context.Context, l1HeadHash common.Hash) (*types.L1Header, *beacon.LightClientUpdateData, uint64, error) {

	// Get finalized L1 data from optimism-preimage-maker
	type Request struct {
		L1HeadHash common.Hash `json:"l1_head_hash"`
	}
	request := &Request{L1HeadHash: l1HeadHash}
	rawResponse, err := pr.preimageMakerHttpClient.POST(ctx, fmt.Sprintf("%s/get_finalized_l1", pr.preimageMakerEndpoint.Get()), request)
	if err != nil {
		return nil, nil, 0, errors.Wrap(err, "failed to get finalized L1 data from preimage maker")
	}

	// Parse FinalizedL1DataResponse
	var finalizedL1Data beacon.FinalizedL1DataResponse
	if err = json.Unmarshal(rawResponse, &finalizedL1Data); err != nil {
		return nil, nil, 0, errors.Wrap(err, "failed to unmarshal FinalizedL1DataResponse")
	}

	// Parse finality_update
	var finalityUpdateRes beacon.LightClientFinalityUpdateResponse
	if err = json.Unmarshal(finalizedL1Data.RawFinalityUpdate, &finalityUpdateRes); err != nil {
		return nil, nil, 0, errors.Wrap(err, "failed to unmarshal finality update")
	}

	// Parse light_client_update (single object extracted by preimage-maker from Beacon API array)
	var lcUpdateSnapshotResponse beacon.LightClientUpdateResponse
	if err = json.Unmarshal(finalizedL1Data.RawLightClientUpdate, &lcUpdateSnapshotResponse); err != nil {
		return nil, nil, 0, errors.Wrap(err, "failed to unmarshal light client update")
	}
	lcUpdateSnapshot := &lcUpdateSnapshotResponse.Data

	// Build L1Header from finality_update
	lcUpdate := finalityUpdateRes.Data.ToProto()
	executionHeader := &finalityUpdateRes.Data.FinalizedHeader.Execution
	executionUpdate, err := pr.buildExecutionUpdate(executionHeader)
	if err != nil {
		return nil, nil, 0, errors.Wrap(err, "failed to build execution update")
	}
	executionRoot, err := executionHeader.HashTreeRoot()
	if err != nil {
		return nil, nil, 0, errors.Wrap(err, "failed to calculate execution root")
	}
	if !bytes.Equal(executionRoot[:], lcUpdate.FinalizedExecutionRoot) {
		return nil, nil, 0, fmt.Errorf("execution root mismatch: %X != %X", executionRoot, lcUpdate.FinalizedExecutionRoot)
	}
	return &types.L1Header{
		ConsensusUpdate: lcUpdate,
		ExecutionUpdate: executionUpdate,
		Timestamp:       executionHeader.Timestamp,
	}, lcUpdateSnapshot, finalizedL1Data.Period, nil
}

func (pr *L1Client) BuildInitialState(ctx context.Context, blockNumber uint64) (*InitialState, error) {

	timestamp, err := pr.TimestampAt(ctx, blockNumber)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get timestamp at blockNumber=%d", blockNumber)
	}
	slot, err := pr.getSlotAtTimestamp(ctx, timestamp)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get slot at timestamp=%d", timestamp)
	}
	period := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))

	currentSyncCommittee, err := pr.getBootstrapInPeriod(ctx, period)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get bootstrap in period %v", period)
	}
	res2, err := pr.beaconClient.GetLightClientUpdate(ctx, period)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get LightClientUpdate: period=%v", period)
	}
	nextSyncCommittee := res2.Data.ToProto().NextSyncCommittee

	genesis, err := pr.beaconClient.GetGenesis(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get genesis")
	}

	return &InitialState{
		Genesis:              *genesis,
		Slot:                 slot,
		Period:               period,
		CurrentSyncCommittee: *currentSyncCommittee,
		NextSyncCommittee:    *nextSyncCommittee,
		Timestamp:            timestamp,
	}, nil
}

func (pr *L1Client) GetSyncCommitteeByBlockNumber(
	ctx context.Context,
	blockNumber uint64,
	latestL1 *types.L1Header,
	latestLcUpdateSnapshot *beacon.LightClientUpdateData,
	latestPeriod uint64,
) (*types.L1Header, error) {
	timestamp, err := pr.TimestampAt(ctx, blockNumber)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get timestamp at blockNumber=%d", blockNumber)
	}
	slot, err := pr.getSlotAtTimestamp(ctx, timestamp)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get slot at timestamp=%d", timestamp)
	}
	period := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))

	// If latestL1 is provided and same period with slot exceeding, use latest's LightClientUpdate
	if latestL1 != nil && latestLcUpdateSnapshot != nil {
		latestSlot := latestL1.ConsensusUpdate.FinalizedHeader.Slot
		if period == latestPeriod && slot > latestSlot {
			pr.logger.InfoContext(ctx, "using latest LightClientUpdate due to same period",
				"period", period, "slot", slot, "latestSlot", latestSlot)
			return pr.buildNextSyncCommitteeUpdateFromData(latestLcUpdateSnapshot, nil)
		}
	}

	return pr.buildNextSyncCommitteeUpdate(ctx, period, nil)
}

func (pr *L1Client) GetSyncCommitteesFromTrustedToLatest(ctx context.Context, trusted *types.L1Header, lfh *types.L1Header) ([]*types.L1Header, error) {
	statePeriod := pr.computeSyncCommitteePeriod(pr.computeEpoch(trusted.ConsensusUpdate.SignatureSlot))
	latestPeriod := pr.computeSyncCommitteePeriod(pr.computeEpoch(lfh.ConsensusUpdate.SignatureSlot))
	pr.logger.DebugContext(ctx, "GetSyncCommitteesFromTrustedToLatest", "statePeriod", statePeriod, "latestPeriod", latestPeriod)
	res, err := pr.beaconClient.GetLightClientUpdate(ctx, statePeriod)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get LightClientUpdate: state_period=%v", statePeriod)
	}
	if statePeriod == latestPeriod {
		root, err := res.Data.FinalizedHeader.Beacon.HashTreeRoot()
		if err != nil {
			return nil, errors.Wrap(err, "failed to calculate root")
		}
		bootstrapRes, err := pr.beaconClient.GetBootstrap(ctx, root[:])
		if err != nil {
			return nil, errors.Wrap(err, "failed to get bootstrap")
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
		return nil, errors.Wrapf(err, "failed to get LightClientUpdate: state_period=%v", statePeriod)
	}
	trustedNextSyncCommittee = res.Data.ToProto().NextSyncCommittee
	for p := statePeriod + 1; p <= latestPeriod; p++ {
		header, err := pr.buildNextSyncCommitteeUpdate(ctx, p, trustedNextSyncCommittee)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to build next sync committee update: period=%v", p)
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
	header, err := pr.executionClient.HeaderByNumber(ctx, util.NewBigInt(number))
	if err != nil {
		return 0, errors.Wrapf(err, "failed to get block from number: number=%d", number)
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

func (pr *L1Client) buildNextSyncCommitteeUpdate(ctx context.Context, period uint64, trustedNextSyncCommittee *lctypes.SyncCommittee) (*types.L1Header, error) {
	res, err := pr.beaconClient.GetLightClientUpdate(ctx, period)
	if err != nil {
		return nil, err
	}
	return pr.buildNextSyncCommitteeUpdateFromData(&res.Data, trustedNextSyncCommittee)
}

func (pr *L1Client) buildNextSyncCommitteeUpdateFromData(latestLcUpdateSnapshot *beacon.LightClientUpdateData, trustedNextSyncCommittee *lctypes.SyncCommittee) (*types.L1Header, error) {
	lcUpdate := latestLcUpdateSnapshot.ToProto()
	executionHeader := &latestLcUpdateSnapshot.FinalizedHeader.Execution
	executionUpdate, err := pr.buildExecutionUpdate(executionHeader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build execution update")
	}
	executionRoot, err := executionHeader.HashTreeRoot()
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate execution root")
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
		Timestamp:       executionHeader.Timestamp,
	}, nil
}

func NewL1Client(ctx context.Context, l1BeaconEndpoint, l1ExecutionEndpoint string,
	preimageMakerTimeout time.Duration,
	preimageMakerEndpoint string,
	minimalForkSched map[string]uint64, logger *log.RelayLogger) (*L1Client, error) {
	beaconClient := beacon.NewClient(l1BeaconEndpoint)
	executionClient, err := ethclient.DialContext(ctx, l1ExecutionEndpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create L1Client")
	}
	chainID, err := executionClient.ChainID(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get chainId on L1Client")
	}

	network := Minimal
	if chainID.Uint64() == 1 {
		network = Mainnet
	} else if chainID.Uint64() == 11155111 {
		network = Sepolia
	}

	return &L1Client{
		beaconClient:            beaconClient,
		executionClient:         executionClient,
		preimageMakerHttpClient: util.NewHTTPClient(preimageMakerTimeout),
		preimageMakerEndpoint:   util.NewSelector(strings.Split(preimageMakerEndpoint, ",")),
		config: &ProverConfig{
			Network:          network,
			MinimalForkSched: minimalForkSched,
		},
		logger: logger,
	}, nil
}
