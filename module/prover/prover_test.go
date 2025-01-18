package prover

import (
	"context"
	"fmt"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/gogoproto/proto"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	"github.com/datachainlab/ibc-hd-signer/pkg/hd"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	types2 "github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/config"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/stretchr/testify/suite"
	"os"
	"testing"
	"time"
)

type ProverTestSuite struct {
	suite.Suite
	prover *Prover
}

func TestProverTestSuite(t *testing.T) {
	suite.Run(t, new(ProverTestSuite))
}

func (ts *ProverTestSuite) SetupTest() {
	err := log.InitLogger("DEBUG", "text", "stdout")
	ts.Require().NoError(err)

	addressHex, err := os.ReadFile("../../tests/contracts/addresses/OwnableIBCHandler")
	ts.Require().NoError(err)

	signerConfig := &hd.SignerConfig{
		Mnemonic: "test test test test test test test test test test test junk",
		Path:     "m/44'/60'/0'/0/0",
	}
	anySignerConfig, err := codectypes.NewAnyWithValue(signerConfig)
	ts.Require().NoError(err)
	l2Chain, err := ethereum.NewChain(ethereum.ChainConfig{
		RpcAddr:    "http://localhost:9545",
		IbcAddress: common.Bytes2Hex(addressHex),
		Signer:     anySignerConfig,
	})
	ts.Require().NoError(err)
	codec := core.MakeCodec()
	modules := []config.ModuleI{ethereum.Module{}, hd.Module{}}
	for _, m := range modules {
		m.RegisterInterfaces(codec.InterfaceRegistry())
	}
	err = l2Chain.Init("", 0, codec, false)
	ts.Require().NoError(err)

	err = l2Chain.SetRelayInfo(&core.PathEnd{
		ClientID:     "mock-client-0",
		ConnectionID: "connection-0",
		ChannelID:    "channel-0",
		PortID:       "transfer",
		Order:        "UNORDERED",
	}, nil, nil)
	ts.Require().NoError(err)

	trustingPeriod := 100 * time.Second
	maxClockDrift := 1 * time.Millisecond
	refreshThresholdRate := &types.Fraction{
		Numerator:   1,
		Denominator: 2,
	}
	opNodeEndpoint := "http://localhost:7545"
	l1ExecutionEndpoint := "http://localhost:8545"
	l1BeaconEndpoint := "http://localhost:5052"
	preimageMakerEndpoint := "http://localhost:10080"
	preimageMakerTimeout := 300 * time.Second
	l1Client, err := l1.NewL1Client(context.Background(), l1BeaconEndpoint, l1ExecutionEndpoint)
	ts.Require().NoError(err)
	l2Client := l2.NewL2Client(l2Chain, l1ExecutionEndpoint, preimageMakerTimeout, preimageMakerEndpoint, opNodeEndpoint)
	ts.prover = NewProver(l2Chain, l1Client, l2Client, trustingPeriod, refreshThresholdRate, maxClockDrift)
}

func (ts *ProverTestSuite) TestCreateInitialLightClientState() {
	anyCs, anyConsState, err := ts.prover.CreateInitialLightClientState(nil)
	ts.Require().NoError(err)

	cs := anyCs.(*types2.ClientState)
	log.GetLogger().Info(fmt.Sprintf("client state: %+v\n", cs))
	consState := anyConsState.(*types2.ConsensusState)
	log.GetLogger().Info(fmt.Sprintf("consensus state: %+v\n", consState))
}

func (ts *ProverTestSuite) TestGetLatestFinalizedHeader() {
	header, err := ts.prover.GetLatestFinalizedHeader()
	ts.Require().NoError(err)
	h := header.(*types2.Header)
	log.GetLogger().Info(fmt.Sprintf("header : %+v\n", h))
	ts.Require().True(len(h.Derivations) > 0)
	ts.Require().True(h.L1Head.ExecutionUpdate.BlockNumber > 0)
}

func (ts *ProverTestSuite) TestSetupHeadersForUpdate() {
	header, err := ts.prover.GetLatestFinalizedHeader()
	ts.Require().NoError(err)
	h := header.(*types2.Header)

	// client state
	trustedHeight := clienttypes.NewHeight(0, header.GetHeight().GetRevisionHeight()-100)
	cs := &types2.ClientState{
		LatestHeight: &trustedHeight,
	}
	protoClientState, err := codectypes.NewAnyWithValue(exported.ClientState(cs).(proto.Message))
	ts.Require().NoError(err)

	// cons state
	tm, err := ts.prover.l1Client.TimestampAt(context.Background(), h.L1Head.ExecutionUpdate.BlockNumber)
	ts.Require().NoError(err)
	slot, err := ts.prover.l1Client.GetSlotAtTimestamp(tm)
	ts.Require().NoError(err)
	const additionalPeriods = 10
	const additionalSlots = l1.MINIMAL_SLOTS_PER_EPOCH * l1.MINIMAL_EPOCHS_PER_SYNC_COMMITTEE_PERIOD * additionalPeriods
	consState := &types2.ConsensusState{
		L1Slot: slot - additionalSlots,
	}
	protoConsState, err := codectypes.NewAnyWithValue(exported.ConsensusState(consState).(proto.Message))
	ts.Require().NoError(err)

	// setup headers from trusted to latest
	chain := &mockChain{
		Chain: ts.prover.l2Client.Chain,
		mockClientState: &clienttypes.QueryClientStateResponse{
			ClientState: protoClientState,
		},
		mockConsState: &clienttypes.QueryConsensusStateResponse{
			ConsensusState: protoConsState,
		},
	}
	headers, err := ts.prover.SetupHeadersForUpdate(chain, header)
	ts.Require().NoError(err)
	ts.Require().True(len(headers) == additionalPeriods+1 || len(headers) == additionalPeriods+1+1, len(headers))

	// Only the last header contains L2 derivation
	h = headers[len(headers)-1].(*types2.Header)
	ts.Require().True(len(h.Preimages) > 0)
	ts.Require().True(len(h.Derivations) > 0)

	// output l1 header to test in elc
	protoL1Head, err := h.L1Head.Marshal()
	ts.Require().NoError(err)

	l1state, err := ts.prover.l1Client.BuildInitialState(h.L1Head.ExecutionUpdate.BlockNumber)
	ts.Require().NoError(err)
	l1Config, err := ts.prover.l1Client.BuildL1Config(l1state)
	ts.Require().NoError(err)
	l1ConfigBytes, err := proto.Marshal(l1Config)
	ts.Require().NoError(err)

	beforeLatestTrusted := headers[len(headers)-3].(*types2.Header)
	latestTrusted := headers[len(headers)-2].(*types2.Header)

	consState = &types2.ConsensusState{
		L1Slot:                 latestTrusted.L1Head.ConsensusUpdate.FinalizedHeader.Slot,
		L1CurrentSyncCommittee: beforeLatestTrusted.L1Head.ConsensusUpdate.NextSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    latestTrusted.L1Head.ConsensusUpdate.NextSyncCommittee.AggregatePubkey,
	}
	fmt.Println(common.Bytes2Hex(protoL1Head))
	fmt.Println(common.Bytes2Hex(l1ConfigBytes))
	fmt.Println(consState.L1Slot)
	fmt.Println(common.Bytes2Hex(consState.L1CurrentSyncCommittee))
	fmt.Println(common.Bytes2Hex(consState.L1NextSyncCommittee))
}

type mockChain struct {
	*ethereum.Chain
	mockLatestHeader core.Header
	mockClientState  *clienttypes.QueryClientStateResponse
	mockConsState    *clienttypes.QueryConsensusStateResponse
}

func (m *mockChain) GetLatestFinalizedHeader() (latestFinalizedHeader core.Header, err error) {
	return m.mockLatestHeader, nil
}

func (m *mockChain) QueryClientState(ctx core.QueryContext) (*clienttypes.QueryClientStateResponse, error) {
	return m.mockClientState, nil
}

func (m *mockChain) QueryClientConsensusState(ctx core.QueryContext, dstClientConsHeight exported.Height) (*clienttypes.QueryConsensusStateResponse, error) {
	return m.mockConsState, nil
}
