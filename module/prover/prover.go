package prover

import (
	"context"
	"fmt"
	"github.com/cosmos/cosmos-sdk/codec"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"time"
)

const ModuleName = "optimism-light-client"

const SyncWaitTTL = 12 * time.Second

type Prover struct {
	l2Client *l2.L2Client
	l1Client *l1.L1Client

	trustingPeriod       time.Duration
	refreshThresholdRate *types.Fraction
	maxClockDrift        time.Duration

	codec codec.ProtoCodecMarshaler
}

//--------- StateProver implementation ---------//

var _ core.StateProver = (*Prover)(nil)

func (pr *Prover) ProveState(ctx core.QueryContext, path string, value []byte) ([]byte, clienttypes.Height, error) {
	proofHeight := ctx.Height().GetRevisionHeight()
	height := util.NewHeight(proofHeight)
	proof, err := pr.l2Client.BuildStateProof(ctx.Context(), []byte(path), int64(proofHeight))
	return proof, *height, err
}

func (pr *Prover) ProveHostConsensusState(ctx core.QueryContext, height exported.Height, consensusState exported.ConsensusState) (proof []byte, err error) {
	return clienttypes.MarshalConsensusState(pr.codec, consensusState)
}

func (pr *Prover) GetLogger() *log.RelayLogger {
	return log.GetLogger().WithChain(pr.l2Client.ChainID()).WithModule(ModuleName)
}

var _ core.Prover = (*Prover)(nil)

// Init initializes the chain
func (pr *Prover) Init(homePath string, timeout time.Duration, codec codec.ProtoCodecMarshaler, debug bool) error {
	pr.codec = codec
	return nil
}

func (pr *Prover) GetLatestFinalizedHeader(ctx context.Context) (latestFinalizedHeader core.Header, err error) {
	syncStatus, err := pr.l2Client.SyncStatus(ctx)
	if err != nil {
		return nil, err
	}
	// Wait for finalizedL1 to exceed syncStatus
	var finalizedL1Header *types.L1Header
	for {
		finalizedL1Header, err = pr.l1Client.GetLatestFinalizedL1Header(ctx)
		if err != nil {
			return nil, err
		}
		if finalizedL1Header.ExecutionUpdate.BlockNumber >= syncStatus.FinalizedL1.Number {
			break
		}
		pr.GetLogger().Debug("seek next finalized l1", "syncStatus", syncStatus.FinalizedL1.Number, "finalized", finalizedL1Header.ExecutionUpdate.BlockNumber)
		time.Sleep(SyncWaitTTL)
	}

	// Find L2 where L1 is deterministic.
	var deterministicL1Header *types.L1Header
	var l2Output *l2.OutputResponse
	finalizedL2Number := syncStatus.FinalizedL2.Number
	for {
		l2Output, err = pr.l2Client.OutputAtBlock(ctx, finalizedL2Number)
		if err != nil {
			return nil, err
		}
		deterministicL1Header, err = pr.l1Client.GetConsensusHeaderByBlockNumber(ctx, l2Output.BlockRef.DeterministicFinalizedL1())
		if err != nil {
			return nil, err
		}

		if finalizedL1Header.ExecutionUpdate.BlockNumber >= deterministicL1Header.ExecutionUpdate.BlockNumber {
			break
		}
		if finalizedL2Number == 0 {
			return nil, fmt.Errorf("no finalized L2 block")
		}
		pr.GetLogger().Debug("seek next finalized l2", "candidate", finalizedL2Number)
		finalizedL2Number--
	}

	pr.GetLogger().Debug("deterministicL1Header", "number", deterministicL1Header.ExecutionUpdate.BlockNumber,
		"finalized-slot", deterministicL1Header.ConsensusUpdate.FinalizedHeader.Slot,
		"signature-slot", deterministicL1Header.ConsensusUpdate.SignatureSlot)
	pr.GetLogger().Debug("finalizedL1Header", "number", finalizedL1Header.ExecutionUpdate.BlockNumber,
		"finalized-slot", finalizedL1Header.ConsensusUpdate.FinalizedHeader.Slot,
		"signature-slot", finalizedL1Header.ConsensusUpdate.SignatureSlot)

	accountUpdate, err := pr.l2Client.BuildAccountUpdate(ctx, l2Output.BlockRef.Number)
	if err != nil {
		return nil, err
	}
	header := &types.Header{
		AccountUpdate:         accountUpdate,
		DeterministicToLatest: []*types.L1Header{deterministicL1Header, finalizedL1Header},
		Derivation: &types.Derivation{
			L2OutputRoot:  l2Output.OutputRoot[:],
			L2BlockNumber: l2Output.BlockRef.Number,
		},
	}
	return header, nil
}

func (pr *Prover) SetupHeadersForUpdate(ctx context.Context, counterparty core.FinalityAwareChain, latestFinalizedHeader core.Header) ([]core.Header, error) {
	latest := latestFinalizedHeader.(*types.Header)

	latestHeightOnDstChain, err := counterparty.LatestHeight(ctx)
	if err != nil {
		return nil, err
	}
	csRes, err := counterparty.QueryClientState(core.NewQueryContext(ctx, latestHeightOnDstChain))
	if err != nil {
		return nil, fmt.Errorf("no client state found : SetupHeadersForUpdate: height = %d, %+v", latestHeightOnDstChain.GetRevisionHeight(), err)
	}
	var cs exported.ClientState
	if err = pr.l2Client.Codec().UnpackAny(csRes.ClientState, &cs); err != nil {
		return nil, err
	}
	trustedHeight := clienttypes.NewHeight(cs.GetLatestHeight().GetRevisionNumber(), cs.GetLatestHeight().GetRevisionHeight())

	pr.GetLogger().Info("Setup Headers For Update", "trustedHeight", trustedHeight.GetRevisionHeight(), "latest", latest.Derivation.L2BlockNumber)

	// No need to update
	if trustedHeight.GetRevisionHeight() == latest.Derivation.L2BlockNumber {
		pr.GetLogger().Info("latest is trusted", "l2", latest.Derivation.L2BlockNumber)
		return nil, nil
	}
	if trustedHeight.GetRevisionHeight() > latest.Derivation.L2BlockNumber {
		pr.GetLogger().Info("past l2 header", "trustedL2", trustedHeight.GetRevisionHeight(), "targetL2", latest.Derivation.L2BlockNumber)
		return nil, nil
	}

	// Collect L1 headers from trusted to deterministic and deterministic to latest by trusted l2
	trustedOutput, err := pr.l2Client.OutputAtBlock(ctx, trustedHeight.RevisionHeight)
	if err != nil {
		return nil, err
	}
	trustedL1BlockNumber := trustedOutput.BlockRef.DeterministicFinalizedL1()
	latestL1 := latest.DeterministicToLatest[1]
	headers, err := pr.l2Client.SplitHeaders(ctx, trustedOutput, latest, latestL1)
	if err != nil {
		return nil, err
	}
	nextTrustedL1 := trustedL1BlockNumber
	for _, h := range headers {
		ih := h.(*types.Header)
		output, err := pr.l2Client.OutputAtBlock(ctx, ih.Derivation.L2BlockNumber)
		if err != nil {
			return nil, err
		}
		deterministicL1, err := pr.l1Client.GetConsensusHeaderByBlockNumber(ctx, output.BlockRef.DeterministicFinalizedL1())
		if err != nil {
			return nil, err
		}
		ih.TrustedToDeterministic, err = pr.l1Client.GetSyncCommitteesFromTrustedToLatest(ctx, nextTrustedL1, deterministicL1)
		if err != nil {
			return nil, err
		}
		ih.DeterministicToLatest, err = pr.l1Client.GetSyncCommitteesFromTrustedToLatest(ctx, deterministicL1.ExecutionUpdate.BlockNumber, latestL1)
		if err != nil {
			return nil, err
		}
		nextTrustedL1 = deterministicL1.ExecutionUpdate.BlockNumber

	}
	for _, h := range headers {
		ih := h.(*types.Header)
		trustedToDeterministicNums := util.Map(ih.TrustedToDeterministic, func(item *types.L1Header, index int) string {
			return fmt.Sprintf("%d/%t", item.ConsensusUpdate.FinalizedHeader.Slot, item.TrustedSyncCommittee.IsNext)
		})
		deterministicToLatestNums := util.Map(ih.DeterministicToLatest, func(item *types.L1Header, index int) string {
			return fmt.Sprintf("%d/%t", item.ConsensusUpdate.FinalizedHeader.Slot, item.TrustedSyncCommittee.IsNext)
		})
		pr.GetLogger().Info("targetHeaders", "l2", ih.Derivation.L2BlockNumber, "trusted_l2", ih.TrustedHeight.GetRevisionHeight(), "l1_t2d", trustedToDeterministicNums, "l1_d2l", deterministicToLatestNums, "preimages", len(ih.Preimages))
	}
	return headers, nil
}

func (pr *Prover) CheckRefreshRequired(ctx context.Context, counterparty core.ChainInfoICS02Querier) (bool, error) {
	cpQueryHeight, err := counterparty.LatestHeight(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get the latest height of the counterparty chain: %v", err)
	}
	cpQueryCtx := core.NewQueryContext(context.TODO(), cpQueryHeight)

	resCs, err := counterparty.QueryClientState(cpQueryCtx)
	if err != nil {
		return false, fmt.Errorf("failed to query the client state on the counterparty chain: %v", err)
	}

	var cs exported.ClientState
	if err := pr.codec.UnpackAny(resCs.ClientState, &cs); err != nil {
		return false, fmt.Errorf("failed to unpack Any into tendermint client state: %v", err)
	}

	resCons, err := counterparty.QueryClientConsensusState(cpQueryCtx, cs.GetLatestHeight())
	if err != nil {
		return false, fmt.Errorf("failed to query the consensus state on the counterparty chain: %v", err)
	}

	var cons exported.ConsensusState
	if err := pr.codec.UnpackAny(resCons.ConsensusState, &cons); err != nil {
		return false, fmt.Errorf("failed to unpack Any into tendermint consensus state: %v", err)
	}
	lcLastTimestamp := time.Unix(0, int64(cons.GetTimestamp()))

	selfQueryHeight, err := pr.l2Client.LatestFinalizedHeight(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get the latest height of the self chain: %v", err)
	}

	selfTimestamp, err := pr.l2Client.Timestamp(ctx, selfQueryHeight)
	if err != nil {
		return false, fmt.Errorf("failed to get timestamp of the self chain: %v", err)
	}

	elapsedTime := selfTimestamp.Sub(lcLastTimestamp)

	durationMulByFraction := func(d time.Duration, f *types.Fraction) time.Duration {
		nsec := d.Nanoseconds() * int64(f.Numerator) / int64(f.Denominator)
		return time.Duration(nsec) * time.Nanosecond
	}
	needsRefresh := elapsedTime > durationMulByFraction(pr.trustingPeriod, pr.refreshThresholdRate)

	return needsRefresh, nil
}

// CreateInitialLightClientState returns a pair of ClientState and ConsensusState based on the state of the self chain at `height`.
// These states will be submitted to the counterparty chain as MsgCreateClient.
// If `height` is nil, the latest finalized height is selected automatically.
func (pr *Prover) CreateInitialLightClientState(ctx context.Context, height exported.Height) (exported.ClientState, exported.ConsensusState, error) {
	var l2Number uint64
	var l2OutputRoot []byte
	var l1Number uint64
	if height != nil {
		l2Number = height.GetRevisionHeight()
		trustedOutput, err := pr.l2Client.OutputAtBlock(ctx, l2Number)
		if err != nil {
			return nil, nil, err
		}
		l2OutputRoot = trustedOutput.OutputRoot[:]
		l1Header, err := pr.l1Client.GetConsensusHeaderByBlockNumber(ctx, trustedOutput.BlockRef.DeterministicFinalizedL1())
		if err != nil {
			return nil, nil, err
		}
		l1Number = l1Header.ExecutionUpdate.BlockNumber
	} else {
		finalized, err := pr.GetLatestFinalizedHeader(ctx)
		if err != nil {
			return nil, nil, err
		}
		header := finalized.(*types.Header)
		derivation := header.Derivation
		l2Number = derivation.L2BlockNumber
		l2OutputRoot = derivation.L2OutputRoot
		l1Number = header.DeterministicToLatest[0].ExecutionUpdate.BlockNumber
	}

	// L1 information
	rollupConfig, err := pr.l2Client.RollupConfigBytes(ctx)
	if err != nil {
		return nil, nil, err
	}
	chainID, err := pr.l2Client.Client().ChainID(ctx)
	if err != nil {
		return nil, nil, err
	}
	timestamp, err := pr.l2Client.TimestampAt(ctx, l2Number)
	if err != nil {
		return nil, nil, err
	}

	// L1 information
	l1InitialState, err := pr.l1Client.BuildInitialState(ctx, l1Number)
	if err != nil {
		return nil, nil, err
	}
	l1Config, err := pr.l1Client.BuildL1Config(l1InitialState, pr.maxClockDrift, pr.trustingPeriod)
	if err != nil {
		return nil, nil, err
	}

	accountUpdate, err := pr.l2Client.BuildAccountUpdate(ctx, l2Number)
	if err != nil {
		return nil, nil, err
	}
	pr.GetLogger().Info("CreateInitialLightClientState", "l1", l1Number, "l2", l2Number, "slot", l1InitialState.Slot, "period", l1InitialState.Period, "storageRoot", common.Bytes2Hex(accountUpdate.AccountStorageRoot))
	clientState := &types.ClientState{
		ChainId:            chainID.Uint64(),
		IbcStoreAddress:    pr.l2Client.Config().IBCAddress().Bytes(),
		IbcCommitmentsSlot: l2.IBCCommitmentsSlot[:],
		LatestHeight:       util.NewHeight(l2Number),
		Frozen:             false,
		RollupConfigJson:   rollupConfig,
		L1Config:           l1Config,
	}
	consensusState := &types.ConsensusState{
		StorageRoot:            accountUpdate.AccountStorageRoot[:],
		Timestamp:              timestamp,
		OutputRoot:             l2OutputRoot,
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
		L1Timestamp:            l1InitialState.Timestamp,
	}
	return clientState, consensusState, nil
}

// SetRelayInfo sets source's path and counterparty's info to the chain
func (pr *Prover) SetRelayInfo(path *core.PathEnd, counterparty *core.ProvableChain, counterpartyPath *core.PathEnd) error {
	return nil
}

// SetupForRelay performs chain-specific setup before starting the relay
func (pr *Prover) SetupForRelay(ctx context.Context) error {
	return nil
}

func NewProver(chain *ethereum.Chain,
	l1Client *l1.L1Client,
	l2Client *l2.L2Client,
	trustingPeriod time.Duration,
	refreshThresholdRate *types.Fraction,
	maxClockDrift time.Duration,
) *Prover {
	return &Prover{
		l2Client:             l2Client,
		l1Client:             l1Client,
		trustingPeriod:       trustingPeriod,
		refreshThresholdRate: refreshThresholdRate,
		maxClockDrift:        maxClockDrift,
		codec:                chain.Codec(),
	}
}
