package prover

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/cosmos/cosmos-sdk/codec"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1/beacon"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
)

const ModuleName = "optimism-light-client"

const SyncWaitTTL = 12 * time.Second

type Prover struct {
	l2Client *l2.L2Client
	l1Client *l1.L1Client

	trustingPeriod            time.Duration
	refreshThresholdRate      *types.Fraction
	maxClockDrift             time.Duration
	maxHeaderConcurrency      uint64
	disputeGameFactoryAddress common.Address

	logger *log.RelayLogger
	codec  codec.ProtoCodecMarshaler
}

var _ core.Prover = (*Prover)(nil)

func (pr *Prover) GetLogger() *log.RelayLogger {
	return pr.logger
}

func (pr *Prover) Init(homePath string, timeout time.Duration, codec codec.ProtoCodecMarshaler, debug bool) error {
	pr.codec = codec
	return nil
}

func (pr *Prover) ProveState(ctx core.QueryContext, path string, value []byte) ([]byte, clienttypes.Height, error) {
	proofHeight := ctx.Height().GetRevisionHeight()
	height := util.NewHeight(proofHeight)
	proof, err := pr.l2Client.BuildStateProof(ctx.Context(), []byte(path), int64(proofHeight))
	return proof, *height, err
}

func (pr *Prover) ProveHostConsensusState(ctx core.QueryContext, height exported.Height, consensusState exported.ConsensusState) (proof []byte, err error) {
	return clienttypes.MarshalConsensusState(pr.codec, consensusState)
}

func (pr *Prover) GetLatestFinalizedHeader(ctx context.Context) (latestFinalizedHeader core.Header, err error) {
	// Get latest derived preimage metadata.
	preimageMetadata, err := pr.l2Client.GetLatestPreimageMetadata(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to call GetLatestPreimageMetadata")
	}

	pr.GetLogger().InfoContext(ctx, "GetLatestFinalizedHeader", "claimed_l2", preimageMetadata.Claimed, "agreed_l2", preimageMetadata.Agreed, "latest_l1", preimageMetadata.L1Head)

	// Find L1 where preimages are created.
	latestL1Header, latestLcUpdate, latestPeriod, err := pr.l1Client.GetFinalizedL1Header(ctx, preimageMetadata.L1Head)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get finalized L1 header: number=%d", preimageMetadata.L1Head)
	}

	// Find L2 where L1 is deterministic.
	deterministicL1Header, l2Output, err := pr.getDeterministicL1Header(ctx, preimageMetadata.Claimed, latestL1Header, latestLcUpdate, latestPeriod)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deterministic L1 header: number=%d", preimageMetadata.Claimed)
	}

	pr.GetLogger().DebugContext(ctx, "deterministicL1Header", "number", deterministicL1Header.ExecutionUpdate.BlockNumber,
		"finalized-slot", deterministicL1Header.ConsensusUpdate.FinalizedHeader.Slot,
		"signature-slot", deterministicL1Header.ConsensusUpdate.SignatureSlot)
	pr.GetLogger().DebugContext(ctx, "latestL1Header", "number", latestL1Header.ExecutionUpdate.BlockNumber,
		"finalized-slot", latestL1Header.ConsensusUpdate.FinalizedHeader.Slot,
		"signature-slot", latestL1Header.ConsensusUpdate.SignatureSlot)

	header := &types.Header{
		DeterministicToLatest: []*types.L1Header{deterministicL1Header, latestL1Header},
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
		return nil, errors.Wrap(err, "failed to get latest height")
	}
	csRes, err := counterparty.QueryClientState(core.NewQueryContext(ctx, latestHeightOnDstChain))
	if err != nil {
		return nil, errors.Wrapf(err, "no client state found : SetupHeadersForUpdate: height=%d", latestHeightOnDstChain.GetRevisionHeight())
	}
	var cs exported.ClientState
	if err = pr.l2Client.Codec().UnpackAny(csRes.ClientState, &cs); err != nil {
		return nil, errors.Wrap(err, "failed unpack client state")
	}
	trustedHeight := clienttypes.NewHeight(cs.GetLatestHeight().GetRevisionNumber(), cs.GetLatestHeight().GetRevisionHeight())

	pr.GetLogger().InfoContext(ctx, "Setup Headers For Update", "trustedHeight", trustedHeight.GetRevisionHeight(), "latest", latest.Derivation.L2BlockNumber)

	// No need to update
	if trustedHeight.GetRevisionHeight() == latest.Derivation.L2BlockNumber {
		pr.GetLogger().InfoContext(ctx, "latest is trusted", "l2", latest.Derivation.L2BlockNumber)
		return core.MakeHeaderStream(), nil
	}
	if trustedHeight.GetRevisionHeight() > latest.Derivation.L2BlockNumber {
		pr.GetLogger().InfoContext(ctx, "past l2 header", "trustedL2", trustedHeight.GetRevisionHeight(), "targetL2", latest.Derivation.L2BlockNumber)
		return core.MakeHeaderStream(), nil
	}

	// Get preimage metadata from trusted height to latest.
	preimageMetadataList, err := pr.l2Client.ListPreimageMetadata(ctx, trustedHeight.RevisionHeight, latest.Derivation.L2BlockNumber)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list preimage metadata : trusted height=%d", trustedHeight.RevisionHeight)
	}

	return pr.makeHeaderChan(ctx, preimageMetadataList, func(ctx context.Context, metadata *l2.PreimageMetadata) (core.Header, error) {

		// Get the latest finalized L1 that created preimages (must be fetched first to pass to getDeterministicL1Header)
		latestL1, latestLcUpdateSnapshot, latestPeriod, err := pr.l1Client.GetFinalizedL1Header(ctx, metadata.L1Head)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get latest l1 header: l1Number=%s", metadata.L1Head.String())
		}

		// Get deterministic L1 (pass latestL1 to ensure consistency when same period)
		agreedDeterministicL1, agreedOutput, err := pr.getDeterministicL1Header(ctx, metadata.Agreed, latestL1, latestLcUpdateSnapshot, latestPeriod)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get deterministic l1 header: agreed l2Number=%d", metadata.Agreed)
		}
		claimedDeterministicL1, claimedOutput, err := pr.getDeterministicL1Header(ctx, metadata.Claimed, latestL1, latestLcUpdateSnapshot, latestPeriod)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get deterministic l1 header: claimed l2Number=%d", metadata.Claimed)
		}

		pr.GetLogger().InfoContext(ctx, "header chunk",
			"agreed_l2", agreedOutput.BlockRef.Number,
			"agreed_deterministic_l1_slot", agreedDeterministicL1.ConsensusUpdate.FinalizedHeader.Slot,
			"claimed_l2", claimedOutput.BlockRef.Number,
			"claimed_deterministic_l1_slot", claimedDeterministicL1.ConsensusUpdate.FinalizedHeader.Slot,
			"latest_l1_slot", latestL1.ConsensusUpdate.FinalizedHeader.Slot,
		)

		// Get preimage data and build header for update
		ih := &types.Header{
			TrustedHeight: util.NewHeight(agreedOutput.BlockRef.Number),
			Derivation: &types.Derivation{
				AgreedL2OutputRoot: agreedOutput.OutputRoot[:],
				L2OutputRoot:       claimedOutput.OutputRoot[:],
				L2BlockNumber:      claimedOutput.BlockRef.Number,
			},
		}
		ih.AccountUpdate, err = pr.l2Client.BuildAccountUpdate(ctx, claimedOutput.BlockRef.Number)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to build account update: number=%d", claimedOutput.BlockRef.Number)
		}
		ih.TrustedToDeterministic, err = pr.l1Client.GetSyncCommitteesFromTrustedToLatest(ctx, agreedDeterministicL1, claimedDeterministicL1)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get sync committees from trusted to latest: trusted=%d, deterministic=%d", agreedDeterministicL1.ConsensusUpdate.FinalizedHeader.Slot, claimedDeterministicL1.ConsensusUpdate.FinalizedHeader.Slot)
		}
		ih.DeterministicToLatest, err = pr.l1Client.GetSyncCommitteesFromTrustedToLatest(ctx, claimedDeterministicL1, latestL1)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get sync committees from trusted to latest: deterministic=%d, latest=%d", claimedDeterministicL1.ConsensusUpdate.FinalizedHeader.Slot, latestL1.ConsensusUpdate.FinalizedHeader.Slot)
		}
		preimage, err := pr.l2Client.GetPreimage(ctx, metadata)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get preimages: claimed=%d ", claimedOutput.BlockRef.Number)
		}
		ih.Preimages = preimage

		return ih, nil
	}), nil
}

func (pr *Prover) CheckRefreshRequired(ctx context.Context, counterparty core.ChainInfoICS02Querier) (bool, error) {
	cpQueryHeight, err := counterparty.LatestHeight(ctx)
	if err != nil {
		return false, errors.Wrapf(err, "failed to get latest height on counterparty chain")
	}
	cpQueryCtx := core.NewQueryContext(ctx, cpQueryHeight)

	resCs, err := counterparty.QueryClientState(cpQueryCtx)
	if err != nil {
		return false, errors.Wrapf(err, "failed to query client state on counterparty chain")
	}

	var cs exported.ClientState
	if err = pr.codec.UnpackAny(resCs.ClientState, &cs); err != nil {
		return false, errors.Wrapf(err, "failed to unpack client state")
	}

	// Get trusted 1 timestamp
	l2Output, err := pr.l2Client.OutputAtBlock(ctx, cs.GetLatestHeight().GetRevisionHeight())
	if err != nil {
		return false, errors.Wrapf(err, "failed to get output at block: l2Number=%d", cs.GetLatestHeight().GetRevisionHeight())
	}
	l1HeaderTimestamp, err := pr.l1Client.TimestampAt(ctx, l2Output.BlockRef.L1Origin.Number)
	if err != nil {
		return false, errors.Wrapf(err, "failed to get l1 timestamp at: l1Number=%d", l2Output.BlockRef.L1Origin.Number)
	}
	lcLastTimestamp := time.Unix(int64(l1HeaderTimestamp), 0)

	// Get latest l1 timestamp on chain
	latestL1Header, err := pr.l1Client.GetLatestETHHeader(ctx)
	if err != nil {
		return false, errors.Wrapf(err, "failed to get latest l1 header")
	}
	selfTimestamp := time.Unix(int64(latestL1Header.Time), 0)

	elapsedTime := selfTimestamp.Sub(lcLastTimestamp)

	durationMulByFraction := func(d time.Duration, f *types.Fraction) time.Duration {
		nsec := d.Nanoseconds() * int64(f.Numerator) / int64(f.Denominator)
		return time.Duration(nsec) * time.Nanosecond
	}
	needsRefresh := elapsedTime > durationMulByFraction(pr.trustingPeriod, pr.refreshThresholdRate)

	pr.GetLogger().DebugContext(ctx, "CheckRefreshRequired", "needsRefresh", needsRefresh, "selfTimestamp", selfTimestamp, "lcLastTimestamp", lcLastTimestamp)

	return needsRefresh, nil
}

func (pr *Prover) CreateInitialLightClientState(ctx context.Context, height exported.Height) (exported.ClientState, exported.ConsensusState, error) {
	var l2Number uint64
	var l2OutputRoot []byte
	var l1Number uint64
	var l1Origin uint64
	if height != nil {
		l2Number = height.GetRevisionHeight()
		l1Header, trustedOutput, err := pr.getDeterministicL1Header(ctx, l2Number, nil, nil, 0)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "failed to get deterministic l1 header: l2Number=%d", l2Number)
		}
		l2OutputRoot = trustedOutput.OutputRoot[:]
		l1Number = l1Header.ExecutionUpdate.BlockNumber
		l1Origin = trustedOutput.BlockRef.L1Origin.Number
	} else {
		finalized, err := pr.GetLatestFinalizedHeader(ctx)
		if err != nil {
			return nil, nil, errors.Wrap(err, "failed to get latest finalized header")
		}
		header := finalized.(*types.Header)
		derivation := header.Derivation
		l2Number = derivation.L2BlockNumber
		l2OutputRoot = derivation.L2OutputRoot
		l1Number = header.DeterministicToLatest[0].ExecutionUpdate.BlockNumber

		output, err := pr.l2Client.OutputAtBlock(ctx, l2Number)
		if err != nil {
			return nil, nil, errors.Wrap(err, "failed to get latest finalized header")
		}
		l1Origin = output.BlockRef.L1Origin.Number
	}

	// L2
	chainID, err := pr.l2Client.Client().ChainID(ctx)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to get chain id from l2")
	}
	timestamp, err := pr.l2Client.TimestampAt(ctx, l2Number)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to get timestamp at : l2Number=%d", l2Number)
	}

	// L1
	l1InitialState, err := pr.l1Client.BuildInitialState(ctx, l1Number)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to build l1 initial state: l1Number=%d", l1Number)
	}
	l1Config, err := pr.l1Client.BuildL1Config(l1InitialState, pr.maxClockDrift, pr.trustingPeriod)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to build l1 config")
	}

	// account
	accountUpdate, err := pr.l2Client.BuildAccountUpdate(ctx, l2Number)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to build account update for initial state: number=%d", l2Number)
	}
	pr.GetLogger().InfoContext(ctx, "CreateInitialLightClientState", "l1", l1Number, "l2", l2Number, "slot", l1InitialState.Slot, "period", l1InitialState.Period, "storageRoot", common.Bytes2Hex(accountUpdate.AccountStorageRoot))
	clientState := &types.ClientState{
		ChainId:            chainID.Uint64(),
		IbcStoreAddress:    pr.l2Client.Config().IBCAddress().Bytes(),
		IbcCommitmentsSlot: l2.IBCCommitmentsSlot[:],
		LatestHeight:       util.NewHeight(l2Number),
		Frozen:             false,
		L1Config:           l1Config,
		FaultDisputeGameConfig: &types.FaultDisputeGameConfig{
			DisputeGameFactoryAddress: pr.disputeGameFactoryAddress.Bytes(),
			// forge inspect src/dispute/DisputeGameFactory.sol storage-layout
			// |------------------+--------------------------------------------+------+--------+-------+-------------------------------------------------------|
			// | _disputeGames    | mapping(Hash => GameId)                    | 103  | 0      | 32    | src/dispute/DisputeGameFactory.sol:DisputeGameFactory |
			// |------------------+--------------------------------------------+------+--------+-------+-------------------------------------------------------|
			DisputeGameFactoryTargetStorageSlot: 103,
			// forge inspect src/dispute/FaultDisputeGame.sol storage-layout
			//|---------------------------------+------------------------------------------------------------------+------+--------+-------+---------------------------------------------------|
			//| createdAt                       | Timestamp                                                        | 0    | 0      | 8     | src/dispute/FaultDisputeGame.sol:FaultDisputeGame |
			//|---------------------------------+------------------------------------------------------------------+------+--------+-------+---------------------------------------------------|
			//| resolvedAt                      | Timestamp                                                        | 0    | 8      | 8     | src/dispute/FaultDisputeGame.sol:FaultDisputeGame |
			//|---------------------------------+------------------------------------------------------------------+------+--------+-------+---------------------------------------------------|
			//| status                          | enum GameStatus                                                  | 0    | 16     | 1     | src/dispute/FaultDisputeGame.sol:FaultDisputeGame |
			//|---------------------------------+------------------------------------------------------------------+------+--------+-------+-------------------------------------
			FaultDisputeGameStatusSlot: 0,
			// status offset index is not 16 but 32 - 8 - 8 - 1 = 15 because items are lined up from behind
			FaultDisputeGameStatusSlotOffset:    15,
			FaultDisputeGameCreatedAtSlotOffset: 24,
		},
	}
	consensusState := &types.ConsensusState{
		StorageRoot:            accountUpdate.AccountStorageRoot[:],
		Timestamp:              timestamp,
		OutputRoot:             l2OutputRoot,
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
		L1Timestamp:            l1InitialState.Timestamp,
		L1Origin:               l1Origin,
	}
	return clientState, consensusState, nil
}

func (pr *Prover) SetRelayInfo(path *core.PathEnd, counterparty *core.ProvableChain, counterpartyPath *core.PathEnd) error {
	return nil
}

func (pr *Prover) SetupForRelay(ctx context.Context) error {
	return nil
}

// getDeterministicL1Header retrieves a deterministic L1 header and its associated L2 output response for a given L2 block number.
// If latestL1/latestLcUpdate/latestPeriod are provided and the deterministic L1 is in the same period with slot exceeding,
// it uses latestLcUpdate to ensure data consistency with preimage-maker.
func (pr *Prover) getDeterministicL1Header(
	ctx context.Context,
	l2Number uint64,
	latestL1 *types.L1Header,
	latestLcUpdateSnapshot *beacon.LightClientUpdateData,
	latestPeriod uint64,
) (*types.L1Header, *l2.OutputResponse, error) {
	l2Output, err := pr.l2Client.OutputAtBlock(ctx, l2Number)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to get output at block: l2Number=%d", l2Number)
	}
	l1Header, err := pr.l1Client.GetSyncCommitteeByBlockNumber(ctx, l2Output.BlockRef.L1Origin.Number, latestL1, latestLcUpdateSnapshot, latestPeriod)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to get l1 consensus: l1Number=%d", l2Output.BlockRef.L1Origin.Number)
	}
	return l1Header, l2Output, nil
}

func (pr *Prover) makeHeaderChan(ctx context.Context, requests []*l2.PreimageMetadata, fn func(context.Context, *l2.PreimageMetadata) (core.Header, error)) <-chan *core.HeaderOrError {
	out := make(chan *core.HeaderOrError)
	sem := make(chan struct{}, pr.maxHeaderConcurrency)
	done := make(chan struct{}, len(requests))
	buffer := make([]atomic.Pointer[core.HeaderOrError], len(requests))

	// worker
	go func() {
		for i, chunk := range requests {
			// block if the number of concurrent workers reaches maxHeaderConcurrency
			sem <- struct{}{}
			go func(index int, chunk *l2.PreimageMetadata) {
				defer func() { done <- struct{}{} }()
				ret, err := fn(ctx, chunk)
				buffer[index].Store(&core.HeaderOrError{
					Header: ret,
					Error:  err,
				})
			}(i, chunk)
		}
	}()

	// sequencer
	go func() {
		defer close(out)
		sequence := 0
		for sequence < len(requests) {
			<-done
			for sequence < len(requests) {
				result := buffer[sequence].Load()
				if result == nil {
					break
				}

				// log
				if result.Header != nil {
					ih := result.Header.(*types.Header)
					args := append([]interface{}{"sequence", sequence}, ih.ToLog()...)
					pr.GetLogger().InfoContext(ctx, "deliver header success", args...)
				} else {
					pr.GetLogger().DebugContext(ctx, "deliver header error", "sequence", sequence, "err", result.Error)
				}

				// Always deliver in order from zero.
				out <- result
				// Reset buffer to avoid large memory usage.
				buffer[sequence].Swap(nil)
				sequence++
				// Allow next worker to start only when the collect sequence is delivered.
				// If it is released on the worker side, the number of tasks will significantly exceed the number of concurrent executions when lcp-go takes a long time.
				<-sem
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
	disputeGameFactoryAddress common.Address,
	logger *log.RelayLogger,
) *Prover {
	return &Prover{
		l2Client:                  l2Client,
		l1Client:                  l1Client,
		trustingPeriod:            trustingPeriod,
		refreshThresholdRate:      refreshThresholdRate,
		maxClockDrift:             maxClockDrift,
		maxHeaderConcurrency:      max(maxHeaderConcurrency, 1),
		codec:                     chain.Codec(),
		disputeGameFactoryAddress: disputeGameFactoryAddress,
		logger:                    logger,
	}
}
