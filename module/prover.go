package module

import (
	"context"
	"fmt"
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
	codec    codec.ProtoCodecMarshaler
}

func (pr *Prover) GetLatestFinalizedHeader() (latestFinalizedHeader core.Header, err error) {
	l1Ref, derivation, err := pr.l2Client.LatestDerivation(context.Background())
	if err != nil {
		return nil, err
	}
	accountUpdate, err := pr.l2Client.BuildAccountUpdate(l1Ref.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to build account update: %v", err)
	}
	header := &Header{
		AccountUpdate: accountUpdate,
		L1Head: &L1Header{
			// TODO
			TrustedSyncCommittee: nil,
			ConsensusUpdate:      nil,
			ExecutionUpdate:      nil,
		},
		Derivations: []*Derivation{derivation},
	}
	return header, nil
}

func (pr *Prover) SetupHeadersForUpdate(counterparty core.FinalityAwareChain, latestFinalizedHeader core.Header) ([]core.Header, error) {
	//TODO implement me
	panic("implement me")
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
	//TODO implement me
	panic("implement me")
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
	_, derivation, err := pr.l2Client.LatestDerivation(ctx)
	if err != nil {
		return nil, nil, err
	}
	rollupConfig, err := pr.l2Client.RollupConfigBytes(ctx)
	if err != nil {
		return nil, nil, err
	}
	chainID, err := pr.l2Client.Client().ChainID(ctx)
	if err != nil {
		return nil, nil, err
	}

	accountUpdate, err := pr.l2Client.BuildAccountUpdate(derivation.L2BlockNumber)
	if err != nil {
		return nil, nil, err
	}
	timestamp, err := pr.l2Client.TimestampAt(ctx, derivation.L2BlockNumber)
	if err != nil {
		return nil, nil, err
	}

	latestHeight := pr.newHeight(derivation.L2BlockNumber)

	clientState := &ClientState{
		ChainId:            chainID.Uint64(),
		IbcStoreAddress:    pr.l2Client.Config().IBCAddress().Bytes(),
		IbcCommitmentsSlot: IBCCommitmentsSlot[:],
		LatestHeight:       &latestHeight,
		TrustingPeriod:     pr.config.TrustingPeriod,
		MaxClockDrift:      pr.config.MaxClockDrift,
		Frozen:             false,
		RollupConfigJson:   rollupConfig,
		// TODO
		L1Config: nil,
	}
	consensusState := &ConsensusState{
		StorageRoot: accountUpdate.AccountStorageRoot,
		Timestamp:   timestamp,
		OutputRoot:  derivation.L2OutputRoot,
		Hash:        derivation.L2HeadHash,
		//TODO
		L1Slot:                 0,
		L1CurrentSyncCommittee: nil,
		L1NextSyncCommittee:    nil,
	}
	return clientState, consensusState, nil
}

func (pr *Prover) newHeight(blockNumber uint64) clienttypes.Height {
	return clienttypes.NewHeight(0, blockNumber)
}

func NewProver(chain *ethereum.Chain, config ProverConfig) *Prover {
	l2Client, err := NewL2Client(context.Background(), &config, chain)
	if err != nil {
		//TODO avoid dial
		panic(err)
	}
	return &Prover{config: config, l2Client: l2Client}
}
