package prover

import (
	"context"
	"fmt"
	"github.com/cockroachdb/errors"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	types2 "github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	types3 "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"time"
)

const ModuleName = "optimism-light-client"

type Prover struct {
	l2Client *l2.L2Client
	l1Client *l1.L1Client

	trustingPeriod       time.Duration
	refreshThresholdRate *types2.Fraction
	maxClockDrift        time.Duration

	codec codec.ProtoCodecMarshaler
}

func (pr *Prover) GetLatestFinalizedHeader() (latestFinalizedHeader core.Header, err error) {
	derivation, err := pr.l2Client.LatestDerivation(context.Background())
	if err != nil {
		return nil, err
	}
	var l1Header *types3.L1Header
	for {
		l1Header, err = pr.l1Client.GetLatestFinalizedL1Header()
		if err != nil {
			return nil, err
		}
		// Must be finalized
		if l1Header.ExecutionUpdate.BlockNumber >= derivation.L1Head.Number {
			break
		}
		pr.GetLogger().Info("waiting for L1 finalization", "sync-status-l1", derivation.L1Head.Number, "finalized-l1", l1Header.ExecutionUpdate.BlockNumber)
		time.Sleep(2 * time.Second)
	}

	accountUpdate, err := pr.l2Client.BuildAccountUpdate(derivation.L2.L2BlockNumber)
	if err != nil {
		return nil, err
	}
	header := &types3.Header{
		AccountUpdate: accountUpdate,
		L1Head:        l1Header,
		Derivation:    &derivation.L2,
	}
	return header, nil
}

func (pr *Prover) SetupHeadersForUpdate(counterparty core.FinalityAwareChain, latestFinalizedHeader core.Header) ([]core.Header, error) {
	ctx := context.Background()
	latest := latestFinalizedHeader.(*types3.Header)

	latestHeightOnDstChain, err := counterparty.LatestHeight()
	if err != nil {
		return nil, err
	}
	csRes, err := counterparty.QueryClientState(core.NewQueryContext(ctx, latestHeightOnDstChain))
	if err != nil {
		return nil, fmt.Errorf("no client state found : SetupHeadersForUpdate: height = %d, %+v", latestHeightOnDstChain.GetRevisionHeight(), err)
	}
	var cs ibcexported.ClientState
	if err = pr.l2Client.Codec().UnpackAny(csRes.ClientState, &cs); err != nil {
		return nil, err
	}

	// Set L1 trusted sync committees
	consStateRes, err := counterparty.QueryClientConsensusState(core.NewQueryContext(ctx, latestHeightOnDstChain), cs.GetLatestHeight())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var ibcConsState ibcexported.ConsensusState
	if err = pr.l2Client.Codec().UnpackAny(consStateRes.ConsensusState, &ibcConsState); err != nil {
		return nil, err
	}
	consState := ibcConsState.(*types3.ConsensusState)
	l1Headers, err := pr.l1Client.GetSyncCommitteeBySlot(ctx, consState.L1Slot, latest.L1Head)
	if err != nil {
		return nil, err
	}

	// Setup trusted height and create preimage data.
	trustedHeight := types.NewHeight(cs.GetLatestHeight().GetRevisionNumber(), cs.GetLatestHeight().GetRevisionHeight())
	latest.TrustedHeight = &trustedHeight
	agreedOutput, err := pr.l2Client.OutputAtBlock(latest.TrustedHeight.GetRevisionHeight())
	if err != nil {
		return nil, err
	}
	latest.Derivation.AgreedL2OutputRoot = agreedOutput.OutputRoot[:]
	pr.GetLogger().Debug("CreatePreimages", "l1", latest.L1Head.ExecutionUpdate.BlockNumber, "l2", latest.Derivation)
	preimages, err := pr.l2Client.CreatePreimages(ctx, latest.L1Head.ExecutionUpdate.BlockHash, latest.Derivation, agreedOutput.BlockRef.Hash)
	if err != nil {
		return nil, err
	}
	latest.Preimages = preimages

	// Make L1 headers to update
	headers := make([]core.Header, len(l1Headers))
	for i, l1Header := range l1Headers {
		// L1 only update
		headers[i] = &types3.Header{
			TrustedHeight: latest.TrustedHeight,
			L1Head:        l1Header,
		}
	}
	headers = append(headers, latest)

	// log
	for _, header := range headers {
		h := header.(*types3.Header)
		args := []interface{}{
			"l1", h.L1Head.ExecutionUpdate.BlockNumber,
			"l1-is-next", h.L1Head.TrustedSyncCommittee.IsNext,
			"l1-finalized-slot", h.L1Head.ConsensusUpdate.FinalizedHeader.Slot,
			"l1-signature-slot", h.L1Head.ConsensusUpdate.SignatureSlot,
			"trusted", h.TrustedHeight.GetRevisionHeight(),
		}
		if h.L1Head.ConsensusUpdate.NextSyncCommittee != nil {
			args = append(args, "l1-next-sc", common.Bytes2Hex(h.L1Head.ConsensusUpdate.NextSyncCommittee.AggregatePubkey))
		}
		if h.Derivation != nil {
			args = append(args, "l2", h.Derivation.L2BlockNumber)
		}
		pr.GetLogger().Debug("SetupHeadersForUpdate", args...)
	}
	return headers, nil
}

func (pr *Prover) CheckRefreshRequired(counterparty core.ChainInfoICS02Querier) (bool, error) {
	cpQueryHeight, err := counterparty.LatestHeight()
	if err != nil {
		return false, fmt.Errorf("failed to get the latest height of the counterparty chain: %v", err)
	}
	cpQueryCtx := core.NewQueryContext(context.TODO(), cpQueryHeight)

	resCs, err := counterparty.QueryClientState(cpQueryCtx)
	if err != nil {
		return false, fmt.Errorf("failed to query the client state on the counterparty chain: %v", err)
	}

	var cs ibcexported.ClientState
	if err := pr.codec.UnpackAny(resCs.ClientState, &cs); err != nil {
		return false, fmt.Errorf("failed to unpack Any into tendermint client state: %v", err)
	}

	resCons, err := counterparty.QueryClientConsensusState(cpQueryCtx, cs.GetLatestHeight())
	if err != nil {
		return false, fmt.Errorf("failed to query the consensus state on the counterparty chain: %v", err)
	}

	var cons ibcexported.ConsensusState
	if err := pr.codec.UnpackAny(resCons.ConsensusState, &cons); err != nil {
		return false, fmt.Errorf("failed to unpack Any into tendermint consensus state: %v", err)
	}
	lcLastTimestamp := time.Unix(0, int64(cons.GetTimestamp()))

	selfQueryHeight, err := pr.l2Client.LatestFinalizedHeight()
	if err != nil {
		return false, fmt.Errorf("failed to get the latest height of the self chain: %v", err)
	}

	selfTimestamp, err := pr.l2Client.Timestamp(selfQueryHeight)
	if err != nil {
		return false, fmt.Errorf("failed to get timestamp of the self chain: %v", err)
	}

	elapsedTime := selfTimestamp.Sub(lcLastTimestamp)

	durationMulByFraction := func(d time.Duration, f *types2.Fraction) time.Duration {
		nsec := d.Nanoseconds() * int64(f.Numerator) / int64(f.Denominator)
		return time.Duration(nsec) * time.Nanosecond
	}
	needsRefresh := elapsedTime > durationMulByFraction(pr.trustingPeriod, pr.refreshThresholdRate)

	return needsRefresh, nil
}

func (pr *Prover) ProveState(ctx core.QueryContext, path string, value []byte) ([]byte, types.Height, error) {
	proofHeight := ctx.Height().GetRevisionHeight()
	height := util.NewHeight(proofHeight)
	proof, err := pr.l2Client.BuildStateProof([]byte(path), proofHeight)
	return proof, *height, err
}

func (pr *Prover) ProveHostConsensusState(ctx core.QueryContext, height ibcexported.Height, consensusState ibcexported.ConsensusState) (proof []byte, err error) {
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

// CreateInitialLightClientState returns a pair of ClientState and ConsensusState based on the state of the self chain at `height`.
// These states will be submitted to the counterparty chain as MsgCreateClient.
// If `height` is nil, the latest finalized height is selected automatically.
func (pr *Prover) CreateInitialLightClientState(height ibcexported.Height) (ibcexported.ClientState, ibcexported.ConsensusState, error) {
	ctx := context.Background()
	derivation, err := pr.l2Client.LatestDerivation(ctx)
	if err != nil {
		return nil, nil, err
	}
	rollupConfig, err := pr.l2Client.RollupConfigBytes()
	if err != nil {
		return nil, nil, err
	}
	chainID, err := pr.l2Client.Client().ChainID(ctx)
	if err != nil {
		return nil, nil, err
	}

	accountUpdate, err := pr.l2Client.BuildAccountUpdate(derivation.L2.L2BlockNumber)
	if err != nil {
		return nil, nil, err
	}
	timestamp, err := pr.l2Client.TimestampAt(ctx, derivation.L2.L2BlockNumber)
	if err != nil {
		return nil, nil, err
	}

	latestHeight := util.NewHeight(derivation.L2.L2BlockNumber)

	l1InitialState, err := pr.l1Client.BuildInitialState(derivation.L1Head.Number)
	if err != nil {
		return nil, nil, err
	}
	l1Config, err := pr.l1Client.BuildL1Config(l1InitialState)
	if err != nil {
		return nil, nil, err
	}

	pr.GetLogger().Info("CreateInitialLightClientState", "l1", derivation.L1Head.Number, "l2", derivation.L2.L2BlockNumber)
	clientState := &types3.ClientState{
		ChainId:            chainID.Uint64(),
		IbcStoreAddress:    pr.l2Client.Config().IBCAddress().Bytes(),
		IbcCommitmentsSlot: l2.IBCCommitmentsSlot[:],
		LatestHeight:       latestHeight,
		TrustingPeriod:     pr.trustingPeriod,
		MaxClockDrift:      pr.maxClockDrift,
		Frozen:             false,
		RollupConfigJson:   rollupConfig,
		L1Config:           l1Config,
	}
	consensusState := &types3.ConsensusState{
		StorageRoot:            accountUpdate.AccountStorageRoot,
		Timestamp:              timestamp,
		OutputRoot:             derivation.L2.L2OutputRoot,
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
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
	refreshThresholdRate *types2.Fraction,
	maxClockDrift time.Duration) *Prover {
	return &Prover{
		l2Client:             l2Client,
		l1Client:             l1Client,
		trustingPeriod:       trustingPeriod,
		refreshThresholdRate: refreshThresholdRate,
		maxClockDrift:        maxClockDrift,
		codec:                chain.Codec(),
	}
}
