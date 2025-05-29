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
	maxHeaderConcurrency uint64
	maxL2NumsForPreimage uint64

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
		deterministicL1Header, l2Output, err = pr.getDeterministicL1Header(ctx, finalizedL2Number)
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

func (pr *Prover) SetupHeadersForUpdate(ctx context.Context, counterparty core.FinalityAwareChain, latestFinalizedHeader core.Header) (<-chan *core.HeaderOrError, error) {
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
	headerChunk, err := pr.splitHeaders(ctx, trustedL1BlockNumber, trustedOutput, latest)
	if err != nil {
		return nil, err
	}
	logger := pr.GetLogger()

	return pr.makeHeaderChan(ctx, headerChunk, func(ctx context.Context, chunk *HeaderChunk) (core.Header, error) {
		ih := &types.Header{}
		ih.AccountUpdate, err = pr.l2Client.BuildAccountUpdate(ctx, chunk.ClaimingOutput.BlockRef.Number)
		if err != nil {
			return nil, err
		}
		ih.TrustedToDeterministic, err = pr.l1Client.GetSyncCommitteesFromTrustedToLatest(ctx, chunk.TrustedL1Number, chunk.DeterministicL1)
		if err != nil {
			return nil, err
		}
		ih.DeterministicToLatest, err = pr.l1Client.GetSyncCommitteesFromTrustedToLatest(ctx, chunk.DeterministicL1.ExecutionUpdate.BlockNumber, latestL1)
		if err != nil {
			return nil, err
		}

		logger.Info("start preimageRequest", "l2", chunk.ClaimingOutput.BlockRef.Number)
		preimage, err := pr.l2Client.CreatePreimages(ctx, &l2.PreimageRequest{
			L1HeadHash:         common.BytesToHash(latestL1.ExecutionUpdate.BlockHash),
			AgreedL2HeadHash:   chunk.TrustedOutput.BlockRef.Hash,
			AgreedL2OutputRoot: common.BytesToHash(chunk.TrustedOutput.OutputRoot[:]),
			L2OutputRoot:       common.BytesToHash(chunk.ClaimingOutput.OutputRoot[:]),
			L2BlockNumber:      chunk.ClaimingOutput.BlockRef.Number,
		})
		if err != nil {
			return nil, err
		}
		logger.Info("success preimageRequest", "l2", chunk.ClaimingOutput.BlockRef.Number, "preimageSize", len(preimage))
		ih.Preimages = preimage

		trustedToDeterministicNums := util.Map(ih.TrustedToDeterministic, func(item *types.L1Header, index int) string {
			return fmt.Sprintf("%d/%t", item.ConsensusUpdate.FinalizedHeader.Slot, item.TrustedSyncCommittee.IsNext)
		})
		deterministicToLatestNums := util.Map(ih.DeterministicToLatest, func(item *types.L1Header, index int) string {
			return fmt.Sprintf("%d/%t", item.ConsensusUpdate.FinalizedHeader.Slot, item.TrustedSyncCommittee.IsNext)
		})
		logger.Info("targetHeader", "l2", ih.Derivation.L2BlockNumber, "trusted_l2", ih.TrustedHeight.GetRevisionHeight(), "l1_t2d", trustedToDeterministicNums, "l1_d2l", deterministicToLatestNums, "preimages", len(ih.Preimages))

		return ih, nil
	}), nil
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
	if err = pr.codec.UnpackAny(resCs.ClientState, &cs); err != nil {
		return false, fmt.Errorf("failed to unpack Any into tendermint client state: %v", err)
	}

	// Get trusted 1 timestamp
	trustedL1Header, _, err := pr.getDeterministicL1Header(ctx, cs.GetLatestHeight().GetRevisionHeight())
	if err != nil {
		return false, fmt.Errorf("failed to get trusted l1 header: %v", err)
	}
	lcLastTimestamp := time.Unix(int64(trustedL1Header.Timestamp), 0)

	// Get latest l1 timestamp on chain
	syncStatus, err := pr.l2Client.SyncStatus(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get the latest height of the self chain: %v", err)
	}
	latestL1Header, _, err := pr.getDeterministicL1Header(ctx, syncStatus.FinalizedL2.Number)
	if err != nil {
		return false, fmt.Errorf("failed to get latest l1 header: %v", err)
	}
	selfTimestamp := time.Unix(int64(latestL1Header.Timestamp), 0)

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
		l1Header, trustedOutput, err := pr.getDeterministicL1Header(ctx, l2Number)
		if err != nil {
			return nil, nil, err
		}
		l2OutputRoot = trustedOutput.OutputRoot[:]
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

func (pr *Prover) getDeterministicL1Header(ctx context.Context, l2Number uint64) (*types.L1Header, *l2.OutputResponse, error) {
	l2Output, err := pr.l2Client.OutputAtBlock(ctx, l2Number)
	if err != nil {
		return nil, nil, err
	}
	l1Header, err := pr.l1Client.GetConsensusHeaderByBlockNumber(ctx, l2Output.BlockRef.DeterministicFinalizedL1())
	if err != nil {
		return nil, nil, err
	}
	return l1Header, l2Output, nil
}

type HeaderChunk struct {
	TrustedOutput   *l2.OutputResponse
	TrustedL1Number uint64
	ClaimingOutput  *l2.OutputResponse
	DeterministicL1 *types.L1Header
}

func (pr *Prover) splitHeaders(ctx context.Context, trustedL1BlockNumber uint64, trustedL2 *l2.OutputResponse, latestHeader *types.Header) ([]*HeaderChunk, error) {
	logger := log.GetLogger()

	l2Numbers := make([]uint64, 0)
	logger.Info("split headers for ", "trustedL2", trustedL2.BlockRef.Number, "latestL2Header", latestHeader.Derivation.L2BlockNumber)

	for start := trustedL2.BlockRef.Number + pr.maxL2NumsForPreimage; start < latestHeader.Derivation.L2BlockNumber; start += pr.maxL2NumsForPreimage {
		l2Numbers = append(l2Numbers, start)
	}
	l2Numbers = append(l2Numbers, latestHeader.Derivation.L2BlockNumber)

	nextTrustedL2 := trustedL2
	nextTrustedL1 := trustedL1BlockNumber

	chunk := make([]*HeaderChunk, len(l2Numbers))
	for i, l2Number := range l2Numbers {
		deterministicL1, claimingOutput, err := pr.getDeterministicL1Header(ctx, l2Number)
		if err != nil {
			return nil, err
		}
		chunk[i] = &HeaderChunk{
			TrustedOutput:   nextTrustedL2,
			TrustedL1Number: nextTrustedL1,
			DeterministicL1: deterministicL1,
			ClaimingOutput:  claimingOutput,
		}
		logger.Info("header chunk",
			"l2", chunk[i].ClaimingOutput.BlockRef.Number,
			"trusted_l2", chunk[i].TrustedOutput.BlockRef.Number,
			"trusted_l1_num", chunk[i].TrustedL1Number,
			"deterministic_l1_num", chunk[i].DeterministicL1.ExecutionUpdate.BlockNumber,
			"deterministic_l1_slot", chunk[i].DeterministicL1.ConsensusUpdate.FinalizedHeader.Slot,
		)
		nextTrustedL2 = claimingOutput
		nextTrustedL1 = deterministicL1.ExecutionUpdate.BlockNumber
	}
	return chunk, nil
}

func (pr *Prover) makeHeaderChan(ctx context.Context, requests []*HeaderChunk, fn func(context.Context, *HeaderChunk) (core.Header, error)) <-chan *core.HeaderOrError {
	out := make(chan *core.HeaderOrError, pr.maxHeaderConcurrency)
	sem := make(chan struct{}, pr.maxHeaderConcurrency)

	resultBuffer := make([]*core.HeaderOrError, len(requests))
	notify := make(chan struct{})

	go func() {
		for i, chunk := range requests {
			sem <- struct{}{}
			go func(index int, chunk *HeaderChunk) {
				defer func() { <-sem }()

				ret, err := fn(ctx, chunk)

				resultBuffer[index] = &core.HeaderOrError{
					Header: ret,
					Error:  err,
				}

				// notify complete
				select {
				case notify <- struct{}{}:
				default: // nonblocking and ignore duplicate notifications
				}
			}(i, chunk)
		}
	}()

	// wait sequence of results
	go func() {
		defer close(out)
		sequence := 0
		for sequence < len(requests) {
			// receive completion.
			<-notify

			for sequence < len(requests) {

				//get result for current sequence
				res := resultBuffer[sequence]

				if res == nil {
					// not ready for current sequence
					break
				}

				out <- res

				// enable to receive next sequence
				sequence++
			}
		}
	}()

	return out
}

func NewProver(chain *ethereum.Chain,
	l1Client *l1.L1Client,
	l2Client *l2.L2Client,
	trustingPeriod time.Duration,
	refreshThresholdRate *types.Fraction,
	maxClockDrift time.Duration,
	maxHeaderConcurrency uint64,
	maxL2NumsForPreimage uint64,
) *Prover {
	if maxL2NumsForPreimage == 0 {
		maxL2NumsForPreimage = 100
	}
	return &Prover{
		l2Client:             l2Client,
		l1Client:             l1Client,
		trustingPeriod:       trustingPeriod,
		refreshThresholdRate: refreshThresholdRate,
		maxClockDrift:        maxClockDrift,
		maxHeaderConcurrency: max(maxHeaderConcurrency, 1),
		maxL2NumsForPreimage: maxL2NumsForPreimage,
		codec:                chain.Codec(),
	}
}
