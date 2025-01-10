package module

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
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"time"
)

var IBCCommitmentsSlot = common.HexToHash("1ee222554989dda120e26ecacf756fe1235cd8d726706b57517715dde4f0c900")

type Prover struct {
	config   ProverConfig
	l2Client *L2Client
	l1Client *L1Client
	codec    codec.ProtoCodecMarshaler
}

func (pr *Prover) GetLatestFinalizedHeader() (latestFinalizedHeader core.Header, err error) {
	derivation, err := pr.l2Client.LatestDerivation(context.Background())
	if err != nil {
		return nil, err
	}

	l1Header, err := pr.l1Client.GetLatestFinalizedL1Header()
	if err != nil {
		return nil, err
	}
	if derivation.L1.Number > l1Header.ExecutionUpdate.BlockNumber {
		return nil, errors.Errorf("l1 finalized block is behind l2: l1=%d, l2=%d", l1Header.ExecutionUpdate.BlockNumber, derivation.L1.Number)
	}

	accountUpdate, err := pr.l2Client.BuildAccountUpdate(derivation.L2.L2BlockNumber)
	if err != nil {
		return nil, err
	}
	header := &Header{
		AccountUpdate: accountUpdate,
		L1Head:        l1Header,
		Derivations:   []*Derivation{&derivation.L2},
	}
	return header, nil
}

func (pr *Prover) SetupHeadersForUpdate(counterparty core.FinalityAwareChain, latestFinalizedHeader core.Header) ([]core.Header, error) {
	header := latestFinalizedHeader.(*Header)
	// LCP doesn't need height / EVM needs latest height
	latestHeightOnDstChain, err := counterparty.LatestHeight()
	if err != nil {
		return nil, err
	}
	csRes, err := counterparty.QueryClientState(core.NewQueryContext(context.TODO(), latestHeightOnDstChain))
	if err != nil {
		return nil, fmt.Errorf("no client state found : SetupHeadersForUpdate: height = %d, %+v", latestHeightOnDstChain.GetRevisionHeight(), err)
	}
	var cs ibcexported.ClientState
	if err = pr.l2Client.Codec().UnpackAny(csRes.ClientState, &cs); err != nil {
		return nil, err
	}

	// Add derivations from latest finalized to trusted height
	ctx := context.Background()
	trustedHeight := cs.GetLatestHeight()
	last := header.Derivations[len(header.Derivations)-1]
	lastAgreedNumber := last.L2BlockNumber - 1
	derivations, err := pr.l2Client.SetupDerivations(ctx, trustedHeight.GetRevisionHeight(), lastAgreedNumber, header.L1Head.ExecutionUpdate.BlockNumber)
	if err != nil {
		return nil, err
	}
	for _, derivation := range derivations {
		pr.GetLogger().Debug("target derivation ", "l2", derivation.L2.L2BlockNumber, "l1", derivation.L1.Number, "finalized_l1", header.L1Head.ExecutionUpdate.BlockNumber)
	}
	// Create preimage data for all derivations
	preimages, err := pr.l2Client.CreatePreimages(ctx, derivations)
	if err != nil {
		return nil, err
	}
	for _, derivation := range derivations {
		header.Derivations = append(header.Derivations, &derivation.L2)
	}
	header.Preimages = preimages

	// Set L1 trusted sync committee
	consStateRes, err := counterparty.QueryClientConsensusState(core.NewQueryContext(ctx, latestHeightOnDstChain), cs.GetLatestHeight())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var ibcConsState ibcexported.ConsensusState
	if err = pr.l2Client.Codec().UnpackAny(consStateRes.ConsensusState, &ibcConsState); err != nil {
		return nil, err
	}
	consState := ibcConsState.(*ConsensusState)
	l1HeadersToUpdateSyncCommittee, err := pr.l1Client.GetSyncCommitteeBySlot(ctx, consState.L1Slot, header.L1Head)
	if err != nil {
		return nil, err
	}
	pr.GetLogger().Info("SetupHeadersForUpdate",
		"l2", last.L2BlockNumber,
		"l1", header.L1Head.ExecutionUpdate.BlockNumber,
		"l1-slot", header.L1Head.ConsensusUpdate.FinalizedHeader.Slot,
		"trusted-l2", trustedHeight.GetRevisionHeight(),
		"trusted-l1-slot", consState.L1Slot,
		"l1-sync-committee-additional-len", len(l1HeadersToUpdateSyncCommittee))

	trusted := clienttypes.NewHeight(trustedHeight.GetRevisionNumber(), trustedHeight.GetRevisionHeight())
	header.TrustedHeight = &trusted

	// Only L1 update headers
	headers := make([]core.Header, len(l1HeadersToUpdateSyncCommittee))
	for i, l1Header := range l1HeadersToUpdateSyncCommittee {
		headers[i] = &Header{
			TrustedHeight: &trusted,
			L1Head:        l1Header,
		}
	}
	return append(headers, header), nil
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
	needsRefresh := elapsedTime > durationMulByFraction(pr.config.GetTrustingPeriod(), pr.config.RefreshThresholdRate)

	return needsRefresh, nil
}

func (pr *Prover) ProveState(ctx core.QueryContext, path string, value []byte) ([]byte, types.Height, error) {
	proofHeight := ctx.Height().GetRevisionHeight()
	height := pr.newHeight(proofHeight)
	proof, err := pr.l2Client.BuildStateProof([]byte(path), proofHeight)
	return proof, height, err
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

	latestHeight := pr.newHeight(derivation.L2.L2BlockNumber)

	l1InitialState, err := pr.l1Client.BuildInitialState(derivation.L1.Number)
	if err != nil {
		return nil, nil, err
	}
	l1Config, err := pr.l1Client.BuildL1Config(l1InitialState)
	if err != nil {
		return nil, nil, err
	}

	pr.GetLogger().Info("CreateInitialLightClientState", "l1", derivation.L1.Number, "l2", derivation.L2.L2BlockNumber)
	clientState := &ClientState{
		ChainId:            chainID.Uint64(),
		IbcStoreAddress:    pr.l2Client.Config().IBCAddress().Bytes(),
		IbcCommitmentsSlot: IBCCommitmentsSlot[:],
		LatestHeight:       &latestHeight,
		TrustingPeriod:     pr.config.TrustingPeriod,
		MaxClockDrift:      pr.config.MaxClockDrift,
		Frozen:             false,
		RollupConfigJson:   rollupConfig,
		L1Config:           l1Config,
	}
	consensusState := &ConsensusState{
		StorageRoot:            accountUpdate.AccountStorageRoot,
		Timestamp:              timestamp,
		OutputRoot:             derivation.L2.L2OutputRoot,
		Hash:                   derivation.L2.L2HeadHash,
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
	}
	return clientState, consensusState, nil
}

func (pr *Prover) newHeight(blockNumber uint64) clienttypes.Height {
	return clienttypes.NewHeight(0, blockNumber)
}

func NewProver(chain *ethereum.Chain, config ProverConfig) *Prover {
	l2Client := NewL2Client(&config, chain)
	l1Client, err := NewL1Client(context.Background(), &config)
	if err != nil {
		panic(err)
	}
	return &Prover{
		config:   config,
		l2Client: l2Client,
		l1Client: l1Client,
		codec:    chain.Codec(),
	}
}
