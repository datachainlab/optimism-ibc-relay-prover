package module

import (
	"fmt"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/ethereum-ibc-relay-prover/light-clients/ethereum/types"
	"github.com/datachainlab/ibc-hd-signer/pkg/hd"
	"github.com/ethereum/go-ethereum/common"
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
	ts.Require().True(len(h.L1Head.ConsensusUpdate.NextSyncCommittee.Pubkeys) > 0)
	ts.Require().True(h.L1Head.ExecutionUpdate.BlockNumber > 0)
}
