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
	"github.com/datachainlab/optimism-ibc-relay-prover/module/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/config"
	"github.com/hyperledger-labs/yui-relayer/core"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/stretchr/testify/suite"
	"math/big"
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
	trustedHeight := clienttypes.NewHeight(0, header.GetHeight().GetRevisionHeight()-10)
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

	h = headers[len(headers)-1].(*types2.Header)
	ts.Require().True(len(h.Preimages) > 0)
	ts.Require().True(len(h.Derivations) > 0)
	for _, e := range headers {
		downcast := e.(*types2.Header)
		if len(downcast.Derivations) > 0 {
			ts.Require().NotNil(downcast.AccountUpdate)
		}
	}

	ts.logForUpdateClientTest(headers, 1, "test_update_client_success.bin")
	ts.logForUpdateClientTest(headers, 2, "test_update_client_l1_only_success.bin")
}

func (ts *ProverTestSuite) TestMergeHeader() {
	trustedHeight := clienttypes.NewHeight(0, 100)
	trustedL1 := 100

	l1Headers := make([]*types2.L1Header, 100)
	for i := 0; i < len(l1Headers); i++ {
		l1Headers[i] = &types2.L1Header{
			ExecutionUpdate: &types.ExecutionUpdate{
				BlockNumber: uint64(trustedL1 + i + 1),
			},
		}
	}

	// 1 : 1
	l2Headers := make([]*l2.L2Derivation, 100)
	for i := range l2Headers {
		l2Headers[i] = &l2.L2Derivation{
			L1Head: l2.L1BlockRef{
				Number: l1Headers[i].ExecutionUpdate.BlockNumber,
			},
			L2: types2.Derivation{
				L2BlockNumber: trustedHeight.GetRevisionHeight() + uint64(i+1),
			},
		}
	}
	headers := mergeHeader(trustedHeight, l1Headers, l2Headers, nil)
	ts.Require().Len(headers, len(l1Headers))
	for _, h := range headers {
		header := h.(*types2.Header)
		ts.Require().Len(header.Derivations, 1)
	}

	// latest only
	l2Headers = make([]*l2.L2Derivation, 100)
	for i := range l2Headers {
		l2Headers[i] = &l2.L2Derivation{
			L1Head: l2.L1BlockRef{
				Number: l1Headers[len(l1Headers)-1].ExecutionUpdate.BlockNumber,
			},
			L2: types2.Derivation{
				L2BlockNumber: trustedHeight.GetRevisionHeight() + uint64(i+1),
			},
		}
	}
	headers = mergeHeader(trustedHeight, l1Headers, l2Headers, nil)
	ts.Require().Len(headers, len(l1Headers))
	for _, h := range headers[:len(headers)-1] {
		header := h.(*types2.Header)
		ts.Require().Len(header.Derivations, 0)
	}
	ts.Require().Len(headers[len(headers)-1].(*types2.Header).Derivations, len(l2Headers)) // latest contains all

	// first only
	l2Headers = make([]*l2.L2Derivation, 100)
	for i := range l2Headers {
		l2Headers[i] = &l2.L2Derivation{
			L1Head: l2.L1BlockRef{
				Number: l1Headers[0].ExecutionUpdate.BlockNumber,
			},
			L2: types2.Derivation{
				L2BlockNumber: trustedHeight.GetRevisionHeight() + uint64(i+1),
			},
		}
	}
	headers = mergeHeader(trustedHeight, l1Headers, l2Headers, nil)
	ts.Require().Len(headers, len(l1Headers))
	for _, h := range headers[1:] {
		header := h.(*types2.Header)
		ts.Require().Len(header.Derivations, 0)
	}
	ts.Require().Len(headers[0].(*types2.Header).Derivations, len(l2Headers))

}

func (ts *ProverTestSuite) logForL1Test(headers []core.Header) {

	h := headers[len(headers)-1].(*types2.Header)
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

	consState := &types2.ConsensusState{
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

func (ts *ProverTestSuite) logForUpdateClientTest(headers []core.Header, targetIndex int, fileName string) {

	h := headers[len(headers)-targetIndex].(*types2.Header)

	l1state, err := ts.prover.l1Client.BuildInitialState(h.L1Head.ExecutionUpdate.BlockNumber)
	ts.Require().NoError(err)
	l1Config, err := ts.prover.l1Client.BuildL1Config(l1state)
	ts.Require().NoError(err)

	beforeLatestTrusted := headers[len(headers)-(targetIndex+2)].(*types2.Header)
	latestTrusted := headers[len(headers)-(targetIndex+1)].(*types2.Header)

	trustedHeight := h.TrustedHeight.GetRevisionHeight()
	l2h, err := ts.prover.l2Client.Chain.Client().HeaderByNumber(context.Background(), big.NewInt(0).SetUint64(trustedHeight))
	ts.Require().NoError(err)
	outputRoot, err := ts.prover.l2Client.OutputAtBlock(trustedHeight)
	ts.Require().NoError(err)
	consState := &types2.ConsensusState{
		StorageRoot:            l2h.Root.Bytes(),
		OutputRoot:             outputRoot.OutputRoot[:],
		Hash:                   l2h.Hash().Bytes(),
		Timestamp:              l2h.Time,
		L1Slot:                 latestTrusted.L1Head.ConsensusUpdate.FinalizedHeader.Slot,
		L1CurrentSyncCommittee: beforeLatestTrusted.L1Head.ConsensusUpdate.NextSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    latestTrusted.L1Head.ConsensusUpdate.NextSyncCommittee.AggregatePubkey,
	}
	trustedPeriod := ts.prover.l1Client.ComputeSyncCommitteePeriodBySlot(consState.L1Slot)
	signaturePeriod := ts.prover.l1Client.ComputeSyncCommitteePeriodBySlot(h.L1Head.ConsensusUpdate.SignatureSlot)
	fmt.Println(trustedPeriod, signaturePeriod, h.L1Head.TrustedSyncCommittee.IsNext)

	rollupConfig, err := ts.prover.l2Client.RollupConfigBytes()
	ts.Require().NoError(err)
	chainID, err := ts.prover.l2Client.Client().ChainID(context.Background())
	ts.Require().NoError(err)
	clientState := &types2.ClientState{
		ChainId:            chainID.Uint64(),
		IbcStoreAddress:    ts.prover.l2Client.Config().IBCAddress().Bytes(),
		IbcCommitmentsSlot: l2.IBCCommitmentsSlot[:],
		LatestHeight:       util.NewHeight(l2h.Number.Uint64()),
		TrustingPeriod:     86400 * time.Second,
		MaxClockDrift:      10 * time.Second,
		Frozen:             false,
		RollupConfigJson:   rollupConfig,
		L1Config:           l1Config,
	}
	fmt.Println(l2h.Number.String())
	csMarshal, err := clientState.Marshal()
	ts.Require().NoError(err)
	fmt.Println(common.Bytes2Hex(csMarshal))
	consMarshal, err := consState.Marshal()
	ts.Require().NoError(err)
	fmt.Println(common.Bytes2Hex(consMarshal))
	lastUpdateClient, err := h.Marshal()
	ts.Require().NoError(err)
	err = os.WriteFile(fileName, lastUpdateClient, 0644)
	ts.Require().NoError(err)
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
