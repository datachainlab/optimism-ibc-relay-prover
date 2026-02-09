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

func (pr *L1Client) GetFinalizedL1Header(ctx context.Context, l1HeadHash common.Hash) (*types.L1Header, uint64, *beacon.LightClientUpdateData, error) {

	// Get finalized L1 data from optimism-preimage-maker
	type Request struct {
		L1HeadHash common.Hash `json:"l1_head_hash"`
	}
	request := &Request{L1HeadHash: l1HeadHash}
	rawResponse, err := pr.preimageMakerHttpClient.POST(ctx, fmt.Sprintf("%s/get_finalized_l1", pr.preimageMakerEndpoint.Get()), request)
	if err != nil {
		return nil, 0, nil, errors.Wrap(err, "failed to get finalized L1 data from preimage maker")
	}

	// Parse FinalizedL1DataResponse
	var finalizedL1Data beacon.FinalizedL1DataResponse
	if err = json.Unmarshal(rawResponse, &finalizedL1Data); err != nil {
		return nil, 0, nil, errors.Wrap(err, "failed to unmarshal FinalizedL1DataResponse")
	}

	lcUpdateSnapshot := &finalizedL1Data.LightClientUpdate.Data

	// Build L1Header from finality_update
	lcUpdate := finalizedL1Data.FinalityUpdate.Data.ToProto()
	executionHeader := &finalizedL1Data.FinalityUpdate.Data.FinalizedHeader.Execution
	executionUpdate, err := pr.buildExecutionUpdate(executionHeader)
	if err != nil {
		return nil, 0, nil, errors.Wrap(err, "failed to build execution update")
	}
	executionRoot, err := executionHeader.HashTreeRoot()
	if err != nil {
		return nil, 0, nil, errors.Wrap(err, "failed to calculate execution root")
	}
	if !bytes.Equal(executionRoot[:], lcUpdate.FinalizedExecutionRoot) {
		return nil, 0, nil, fmt.Errorf("execution root mismatch: %X != %X", executionRoot, lcUpdate.FinalizedExecutionRoot)
	}
	return &types.L1Header{
		ConsensusUpdate: lcUpdate,
		ExecutionUpdate: executionUpdate,
		Timestamp:       executionHeader.Timestamp,
	}, finalizedL1Data.Period, lcUpdateSnapshot, nil
}

func (pr *L1Client) BuildInitialState(ctx context.Context, blockNumber uint64) (*InitialState, error) {
	period, err := pr.GetPreviousPeriodByBlockNumber(ctx, blockNumber)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get period by blockNumber=%d", blockNumber)
	}
	currentSyncCommittee, err := pr.getBootstrapInPeriod(ctx, period)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get bootstrap in period %v", period)
	}
	res, err := pr.beaconClient.GetLightClientUpdate(ctx, period)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get LightClientUpdate: period=%v", period)
	}
	nextSyncCommittee := res.Data.ToProto().NextSyncCommittee

	genesis, err := pr.beaconClient.GetGenesis(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get genesis")
	}

	return &InitialState{
		Genesis:              *genesis,
		Slot:                 uint64(res.Data.FinalizedHeader.Beacon.Slot),
		Period:               period,
		CurrentSyncCommittee: *currentSyncCommittee,
		NextSyncCommittee:    *nextSyncCommittee,
		Timestamp:            res.Data.FinalizedHeader.Execution.Timestamp,
	}, nil
}

func (pr *L1Client) GetPreviousPeriodByBlockNumber(ctx context.Context, blockNumber uint64) (uint64, error) {
	timestamp, err := pr.TimestampAt(ctx, blockNumber)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to get timestamp at blockNumber=%d", blockNumber)
	}
	slot, err := pr.getSlotAtTimestamp(ctx, timestamp)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to get slot at timestamp=%d", timestamp)
	}
	period := pr.computeSyncCommitteePeriod(pr.computeEpoch(slot))
	if period > 0 {
		period--
	}
	return period, nil
}

// GetSyncCommitteesFromTrustedToDeterministic returns the sync committee updates needed to transition from
// the trusted state to the deterministic header.
// This function does not use snapshot since both trusted and deterministic use past periods (period - 1).
func (pr *L1Client) GetSyncCommitteesFromAgreedToClaimed(
	ctx context.Context,
	agreedPeriod uint64,
	claimedPeriod uint64,
	claimed *types.L1Header,
) ([]*types.L1Header, error) {
	pr.logger.InfoContext(ctx, "GetSyncCommitteesFromAgreedToClaimed", "agreedPeriod", agreedPeriod, "claimedPeriod", claimedPeriod)

	res, err := pr.beaconClient.GetLightClientUpdate(ctx, agreedPeriod)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get LightClientUpdate: agreedPeriod=%v", agreedPeriod)
	}
	if agreedPeriod == claimedPeriod {
		root, err := res.Data.FinalizedHeader.Beacon.HashTreeRoot()
		if err != nil {
			return nil, errors.Wrap(err, "failed to calculate root")
		}
		bootstrapRes, err := pr.beaconClient.GetBootstrap(ctx, root[:])
		if err != nil {
			return nil, errors.Wrap(err, "failed to get bootstrap")
		}
		claimed.TrustedSyncCommittee = &lctypes.TrustedSyncCommittee{
			SyncCommittee: bootstrapRes.Data.CurrentSyncCommittee.ToProto(),
			IsNext:        false,
		}
		return []*types.L1Header{claimed}, nil
	} else if agreedPeriod > claimedPeriod {
		return nil, fmt.Errorf("agreedPeriod must be greater less than or equals to claimedPeriod: agreedPeriod=%v claimedPeriod=%v", agreedPeriod, claimedPeriod)
	}

	var (
		headers                     []*types.L1Header
		trustedNextSyncCommittee    *lctypes.SyncCommittee
		trustedCurrentSyncCommittee *lctypes.SyncCommittee
	)
	trustedNextSyncCommittee = res.Data.ToProto().NextSyncCommittee
	for p := agreedPeriod + 1; p <= claimedPeriod; p++ {
		header, err := pr.BuildNextSyncCommitteeUpdate(ctx, p, trustedNextSyncCommittee)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to build next sync committee update: period=%v", p)
		}
		trustedCurrentSyncCommittee = trustedNextSyncCommittee
		trustedNextSyncCommittee = header.ConsensusUpdate.NextSyncCommittee
		headers = append(headers, header)
	}
	claimed.TrustedSyncCommittee = &lctypes.TrustedSyncCommittee{
		SyncCommittee: trustedCurrentSyncCommittee,
		IsNext:        false,
	}
	return append(headers, claimed), nil
}

// GetSyncCommitteesFromDeterministicToLatest returns the sync committee updates needed to transition from
// the deterministic header to the latest finalized header.
// lcUpdateSnapshot is used for the sync committee update at latestPeriod to ensure consistency with preimage-maker's cached data.
func (pr *L1Client) GetSyncCommitteesFromClaimedToLatest(
	ctx context.Context,
	claimedPeriod uint64,
	latestPeriod uint64,
	latest *types.L1Header,
	lcUpdateSnapshot *beacon.LightClientUpdateData,
) ([]*types.L1Header, error) {
	pr.logger.InfoContext(ctx, "GetSyncCommitteesFromClaimedToLatest", "claimedPeriod", claimedPeriod, "latestPeriod", latestPeriod)

	res, err := pr.beaconClient.GetLightClientUpdate(ctx, claimedPeriod)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get LightClientUpdate: claimedPeriod=%v", claimedPeriod)
	}
	if claimedPeriod >= latestPeriod {
		return nil, fmt.Errorf("claimedPeriod must be than less than latestPeriod: claimedPeriod=%v latestPeriod=%v", claimedPeriod, latestPeriod)
	}

	var (
		headers                     []*types.L1Header
		trustedNextSyncCommittee    *lctypes.SyncCommittee
		trustedCurrentSyncCommittee *lctypes.SyncCommittee
	)
	trustedNextSyncCommittee = res.Data.ToProto().NextSyncCommittee
	for p := claimedPeriod + 1; p <= latestPeriod; p++ {
		var header *types.L1Header
		// Use cached snapshot for latestPeriod to ensure consistency with preimage-maker
		if p == latestPeriod {
			pr.logger.InfoContext(ctx, "using cached snapshot for sync committee update", "period", p)
			header, err = pr.buildNextSyncCommitteeUpdateFromData(lcUpdateSnapshot, trustedNextSyncCommittee)
		} else {
			header, err = pr.BuildNextSyncCommitteeUpdate(ctx, p, trustedNextSyncCommittee)
		}
		if err != nil {
			return nil, errors.Wrapf(err, "failed to build next sync committee update: period=%v", p)
		}
		trustedCurrentSyncCommittee = trustedNextSyncCommittee
		trustedNextSyncCommittee = header.ConsensusUpdate.NextSyncCommittee
		headers = append(headers, header)
	}
	latest.TrustedSyncCommittee = &lctypes.TrustedSyncCommittee{
		SyncCommittee: trustedCurrentSyncCommittee,
		IsNext:        false,
	}
	return append(headers, latest), nil
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

func (pr *L1Client) BuildNextSyncCommitteeUpdate(ctx context.Context, period uint64, trustedNextSyncCommittee *lctypes.SyncCommittee) (*types.L1Header, error) {
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
