package module

import (
	"context"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/client"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/ethereum-ibc-relay-prover/beacon"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"time"
)

var IBCCommitmentsSlot = common.HexToHash("1ee222554989dda120e26ecacf756fe1235cd8d726706b57517715dde4f0c900")

type Prover struct {
	chain           *ethereum.Chain
	config          ProverConfig
	executionClient *client.ETHClient
	beaconClient    beacon.Client
	l2Client        *L2Client
	codec           codec.ProtoCodecMarshaler
}

func (pr *Prover) GetLatestFinalizedHeader() (latestFinalizedHeader core.Header, err error) {
	//TODO implement me
	panic("implement me")
}

func (pr *Prover) SetupHeadersForUpdate(counterparty core.FinalityAwareChain, latestFinalizedHeader core.Header) ([]core.Header, error) {
	//TODO implement me
	panic("implement me")
}

func (pr *Prover) CheckRefreshRequired(counterparty core.ChainInfoICS02Querier) (bool, error) {
	//TODO implement me
	panic("implement me")
}

func (pr *Prover) ProveState(ctx core.QueryContext, path string, value []byte) (proof []byte, proofHeight types.Height, err error) {
	//TODO implement me
	panic("implement me")
}

func (pr *Prover) ProveHostConsensusState(ctx core.QueryContext, height ibcexported.Height, consensusState ibcexported.ConsensusState) (proof []byte, err error) {
	//TODO implement me
	panic("implement me")
}

func (pr *Prover) GetLogger() *log.RelayLogger {
	return log.GetLogger().WithChain(pr.chain.ChainID()).WithModule(ModuleName)
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
	l1Ref, derivation, err := pr.l2Client.LatestDerivation(context.Background())
	if err != nil {
		return nil, nil, err
	}

	clientState := &ClientState{
		ChainId:            0,
		IbcStoreAddress:    nil,
		IbcCommitmentsSlot: nil,
		LatestHeight:       nil,
		TrustingPeriod:     0,
		MaxClockDrift:      0,
		Frozen:             false,
		RollupConfigJson:   nil,
		L1Config:           nil,
	}
	consensusState := &ConsensusState{
		StorageRoot: nil,
		Timestamp:   0,
		OutputRoot:  derivation.L2OutputRoot,
		Hash:        derivation.L2HeadHash,
		//TODO
		L1Slot:                 0,
		L1CurrentSyncCommittee: nil,
		L1NextSyncCommittee:    nil,
	}
	return clientState, consensusState, nil
}

func NewProver(chain *ethereum.Chain, config ProverConfig) *Prover {
	beaconClient := beacon.NewClient(config.L1BeaconEndpoint)
	l2Client, err := NewL2Client(context.Background(), &config)
	if err != nil {
		//TODO avoid dial
		panic(err)
	}
	return &Prover{chain: chain, config: config, executionClient: chain.Client(), beaconClient: beaconClient, l2Client: l2Client}
}
