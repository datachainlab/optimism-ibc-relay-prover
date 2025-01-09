package module

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

	addressHex, err := os.ReadFile("../tests/contracts/addresses/OwnableIBCHandler")
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
	modules := []config.ModuleI{ethereum.Module{}, Module{}, hd.Module{}}
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

	config := ProverConfig{
		TrustingPeriod: 100 * time.Second,
		MaxClockDrift:  1 * time.Millisecond,
		RefreshThresholdRate: &types.Fraction{
			Numerator:   1,
			Denominator: 2,
		},
		OpNodeEndpoint:        "http://localhost:7545",
		L1ExecutionEndpoint:   "http://localhost:8545",
		L1BeaconEndpoint:      "http://localhost:5052",
		PreimageMakerEndpoint: "http://localhost:10080",
		PreimageMakerTimeout:  30 * time.Second,
		L1L2DistanceThreshold: 10,
	}
	ts.prover = NewProver(l2Chain, config)
}

func (ts *ProverTestSuite) TestCreateInitialLightClientState() {
	anyCs, anyConsState, err := ts.prover.CreateInitialLightClientState(nil)
	ts.Require().NoError(err)

	cs := anyCs.(*ClientState)
	log.GetLogger().Info(fmt.Sprintf("client state: %+v\n", cs))
	consState := anyConsState.(*ConsensusState)
	log.GetLogger().Info(fmt.Sprintf("consensus state: %+v\n", consState))
}

func (ts *ProverTestSuite) TestGetLatestFinalizedHeader() {
	header, err := ts.prover.GetLatestFinalizedHeader()
	ts.Require().NoError(err)
	h := header.(*Header)
	log.GetLogger().Info(fmt.Sprintf("header : %+v\n", h))
	ts.Require().True(len(h.Derivations) > 0)
	ts.Require().True(h.L1Head.ExecutionUpdate.BlockNumber > 0)
}

func (ts *ProverTestSuite) TestSetupHeadersForUpdate() {
	header, err := ts.prover.GetLatestFinalizedHeader()
	ts.Require().NoError(err)
	h := header.(*Header)

	// client state
	trustedHeight := clienttypes.NewHeight(0, header.GetHeight().GetRevisionHeight()-2)
	cs := &ClientState{
		LatestHeight: &trustedHeight,
	}
	protoClientState, err := codectypes.NewAnyWithValue(exported.ClientState(cs).(proto.Message))
	ts.Require().NoError(err)

	// cons state
	tm, err := ts.prover.l1Client.timestampAt(context.Background(), h.L1Head.ExecutionUpdate.BlockNumber)
	ts.Require().NoError(err)
	slot, err := ts.prover.l1Client.getSlotAtTimestamp(tm)
	ts.Require().NoError(err)
	consState := &ConsensusState{
		L1Slot: slot,
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
	ts.Require().True(len(headers) > 0)
	h = headers[0].(*Header)
	ts.Require().True(len(h.Preimages) > 0)
	ts.Require().True(len(h.Derivations) > 0)
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
