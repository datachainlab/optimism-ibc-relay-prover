package prover

import (
	"context"
	"encoding/json"
	"fmt"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/gogoproto/proto"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/cosmos/ibc-go/v8/modules/core/exported"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/ibc-hd-signer/pkg/hd"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l2"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
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

type HostPort struct {
	L1BeaconPort int `json:"l1BeaconPort"`
	L1GethPort   int `json:"l1GethPort"`
	L2RollupPort int `json:"l2RollupPort"`
	L2GethPort   int `json:"l2GethPort"`
}

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

	hostPortJson, err := os.ReadFile("../../hostPort.json")
	ts.Require().NoError(err)
	var hostPort HostPort
	ts.Require().NoError(json.Unmarshal(hostPortJson, &hostPort))

	signerConfig := &hd.SignerConfig{
		Mnemonic: "test test test test test test test test test test test junk",
		Path:     "m/44'/60'/0'/0/0",
	}
	anySignerConfig, err := codectypes.NewAnyWithValue(signerConfig)
	ts.Require().NoError(err)
	l2Chain, err := ethereum.NewChain(context.Background(), ethereum.ChainConfig{
		RpcAddr:    fmt.Sprintf("http://localhost:%d", hostPort.L2GethPort),
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

	trustingPeriod := 86400 * time.Second
	maxClockDrift := 1 * time.Millisecond
	refreshThresholdRate := &types.Fraction{
		Numerator:   1,
		Denominator: 2,
	}
	opNodeEndpoint := fmt.Sprintf("http://localhost:%d", hostPort.L2RollupPort)
	l1ExecutionEndpoint := fmt.Sprintf("http://localhost:%d", hostPort.L1GethPort)
	l1BeaconEndpoint := fmt.Sprintf("http://localhost:%d", hostPort.L1BeaconPort)
	preimageMakerEndpoint := "http://localhost:10080"
	preimageMakerTimeout := 300 * time.Second
	l1Client, err := l1.NewL1Client(context.Background(), l1BeaconEndpoint, l1ExecutionEndpoint)
	ts.Require().NoError(err)
	l2Client := l2.NewL2Client(l2Chain, l1ExecutionEndpoint, preimageMakerTimeout, preimageMakerEndpoint, opNodeEndpoint, 100, 4)
	ts.prover = NewProver(l2Chain, l1Client, l2Client, trustingPeriod, refreshThresholdRate, maxClockDrift)
}

func (ts *ProverTestSuite) TestCreateInitialLightClientState() {
	anyCs, anyConsState, err := ts.prover.CreateInitialLightClientState(context.Background(), nil)
	ts.Require().NoError(err)

	cs := anyCs.(*types2.ClientState)
	log.GetLogger().Info(fmt.Sprintf("client state: %+v\n", cs))
	consState := anyConsState.(*types2.ConsensusState)
	log.GetLogger().Info(fmt.Sprintf("consensus state: %+v\n", consState))
}

func (ts *ProverTestSuite) TestGetLatestFinalizedHeader() {
	header, err := ts.prover.GetLatestFinalizedHeader(context.Background())
	ts.Require().NoError(err)
	h := header.(*types2.Header)
	ts.Require().Len(h.TrustedToDeterministic, 0)
	ts.Require().Len(h.DeterministicToLatest, 2)
	ts.Require().True(h.Derivation.L2BlockNumber > 0)
}

func (ts *ProverTestSuite) TestSetupHeadersForUpdateShort() {
	headers, trustedHeight := ts.setupHeadersForUpdate(1)
	cs, consState, err := ts.prover.CreateInitialLightClientState(context.Background(), trustedHeight)
	ts.Require().NoError(err)
	rawUpdateClient, err := clienttypes.PackClientMessage(headers[0])
	ts.Require().NoError(err)
	encodedUpdateClient, err := rawUpdateClient.Marshal()
	ts.Require().NoError(err)

	rawCs := cs.(*types.ClientState)
	encodedCs, err := rawCs.Marshal()
	ts.Require().NoError(err)

	rawConsState := consState.(*types.ConsensusState)
	encodedConsState, err := rawConsState.Marshal()
	ts.Require().NoError(err)

	l1Config, err := rawCs.L1Config.Marshal()
	ts.Require().NoError(err)
	println("l1Config", common.Bytes2Hex(l1Config))

	trustedL1, err := headers[0].(*types.Header).TrustedToDeterministic[0].Marshal()
	println("rawL1Header", common.Bytes2Hex(trustedL1))

	println("trusted_timestamp", rawConsState.L1Timestamp)
	println("trusted_slot", int64(rawConsState.L1Slot))
	println("trusted_current_sync_committee", common.Bytes2Hex(rawConsState.L1CurrentSyncCommittee))

	ts.Require().NoError(os.WriteFile("update_client_header.bin", encodedUpdateClient, 0644))
	println("cs", common.Bytes2Hex(encodedCs))
	println("consState", common.Bytes2Hex(encodedConsState))
	println("now", time.Now().Unix())
}

func (ts *ProverTestSuite) TestSetupHeadersForUpdateLong() {
	headers, trustedHeight := ts.setupHeadersForUpdate(600)
	cs, consState, err := ts.prover.CreateInitialLightClientState(context.Background(), trustedHeight)
	ts.Require().NoError(err)
	rawCs := cs.(*types.ClientState)
	rawL1Config, err := rawCs.L1Config.Marshal()
	println("rawL1Config", common.Bytes2Hex(rawL1Config))
	ts.Require().NoError(err)
	rawConsState := consState.(*types.ConsensusState)
	println("now", time.Now().Unix())
	l1HeaderKeys := map[uint64]struct{}{}
	var l1Headers []*types.L1Header
	for _, header := range headers {
		h := header.(*types.Header)
		for _, l1H := range h.TrustedToDeterministic {
			if _, ok := l1HeaderKeys[l1H.ExecutionUpdate.BlockNumber]; !ok {
				l1HeaderKeys[l1H.ExecutionUpdate.BlockNumber] = struct{}{}
				l1Headers = append(l1Headers, l1H)
			}
		}
	}
	for i, l1H := range l1Headers {
		rawL1H, err := l1H.Marshal()
		ts.Require().NoError(err)
		println("rawL1Header", common.Bytes2Hex(rawL1H))
		if i == 0 {
			println("cons_slot", rawConsState.L1Slot)
			println("cons_l1_current_sync_committee", common.Bytes2Hex(rawConsState.L1CurrentSyncCommittee))
			println("cons_l1_next_sync_committee", common.Bytes2Hex(rawConsState.L1NextSyncCommittee))
			println("cons_l1_timestamp", rawConsState.Timestamp)
		} else {
			println("cons_slot", l1Headers[i-1].ConsensusUpdate.FinalizedHeader.Slot)
			if i == 1 {
				println("cons_l1_current_sync_committee", common.Bytes2Hex(rawConsState.L1NextSyncCommittee))
			} else {
				println("cons_l1_current_sync_committee", common.Bytes2Hex(l1Headers[i-2].ConsensusUpdate.NextSyncCommittee.AggregatePubkey))
			}
			println("cons_l1_next_sync_committee", common.Bytes2Hex(l1Headers[i-1].ConsensusUpdate.NextSyncCommittee.AggregatePubkey))
			println("cons_l1_timestamp", l1Headers[i-1].Timestamp)
		}
	}
}

func (ts *ProverTestSuite) setupHeadersForUpdate(latestToTrusted uint64) ([]core.Header, clienttypes.Height) {
	latest, err := ts.prover.GetLatestFinalizedHeader(context.Background())
	ts.Require().NoError(err)
	h := latest.(*types2.Header)

	// client state
	trustedHeight := clienttypes.NewHeight(0, latest.GetHeight().GetRevisionHeight()-latestToTrusted)
	cs := &types2.ClientState{
		LatestHeight: &trustedHeight,
	}
	protoClientState, err := codectypes.NewAnyWithValue(exported.ClientState(cs).(proto.Message))
	ts.Require().NoError(err)

	// setup headers from trusted to latest
	chain := &mockChain{
		Chain: ts.prover.l2Client.Chain,
		mockClientState: &clienttypes.QueryClientStateResponse{
			ClientState: protoClientState,
		},
	}
	headers, err := ts.prover.SetupHeadersForUpdate(context.Background(), chain, latest)
	ts.Require().NoError(err)

	nextTrusted := trustedHeight.RevisionHeight
	lastT2D := uint64(0)
	for i, h := range headers {
		ih := h.(*types2.Header)
		ts.Require().True(len(ih.AccountUpdate.AccountStorageRoot) > 0)
		ts.Require().True(len(ih.DeterministicToLatest) > 0)
		ts.Require().Equal(ih.TrustedHeight.RevisionHeight, nextTrusted, i)
		nextTrusted = ih.Derivation.L2BlockNumber
		if i > 0 {
			for j, t2d := range ih.TrustedToDeterministic {
				ts.Require().True(t2d.ExecutionUpdate.BlockNumber >= lastT2D)
				if j > 0 {
					ts.Require().True(t2d.ExecutionUpdate.BlockNumber >= ih.TrustedToDeterministic[j-1].ExecutionUpdate.BlockNumber)
				}
			}
			for j, d2t := range ih.DeterministicToLatest {
				if j > 0 {
					ts.Require().True(d2t.ExecutionUpdate.BlockNumber >= ih.DeterministicToLatest[j-1].ExecutionUpdate.BlockNumber)
				}
				for _, t2d := range ih.TrustedToDeterministic {
					ts.Require().True(d2t.ExecutionUpdate.BlockNumber >= t2d.ExecutionUpdate.BlockNumber)
				}
			}
		}
		if len(ih.TrustedToDeterministic) > 0 {
			lastT2D = ih.TrustedToDeterministic[len(ih.TrustedToDeterministic)-1].ExecutionUpdate.BlockNumber
		}
	}
	ts.Require().True(len(headers) > 0)
	ts.Require().True(len(h.Preimages) > 0)
	return headers, trustedHeight
}

type mockChain struct {
	*ethereum.Chain
	mockLatestHeader core.Header
	mockClientState  *clienttypes.QueryClientStateResponse
	mockConsState    *clienttypes.QueryConsensusStateResponse
}

func (m *mockChain) GetLatestFinalizedHeader(ctx context.Context) (latestFinalizedHeader core.Header, err error) {
	return m.mockLatestHeader, nil
}

func (m *mockChain) QueryClientState(ctx core.QueryContext) (*clienttypes.QueryClientStateResponse, error) {
	return m.mockClientState, nil
}

func (m *mockChain) QueryClientConsensusState(ctx core.QueryContext, dstClientConsHeight exported.Height) (*clienttypes.QueryConsensusStateResponse, error) {
	return m.mockConsState, nil
}
