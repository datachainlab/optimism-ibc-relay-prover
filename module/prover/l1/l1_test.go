package l1

import (
	"context"
	"fmt"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/hyperledger-labs/yui-relayer/log"
	"github.com/stretchr/testify/suite"
	"testing"
	"time"
)

type L1TestSuite struct {
	suite.Suite
	l1Client *L1Client
}

func TestL1TestSuite(t *testing.T) {
	suite.Run(t, new(L1TestSuite))
}

func (ts *L1TestSuite) SetupTest() {
	err := log.InitLogger("DEBUG", "text", "stdout")
	ts.Require().NoError(err)

	//TODO Use lighthouse devnet
	l1ExecutionEndpoint := "http://localhost:8546"
	l1BeaconEndpoint := "http://localhost:19596"

	l1Client, err := NewL1Client(context.Background(), l1BeaconEndpoint, l1ExecutionEndpoint)
	ts.Require().NoError(err)
	ts.l1Client = l1Client

}
func (ts *L1TestSuite) TestBuildL1Config() {
	finality, err := ts.l1Client.beaconClient.GetLightClientFinalityUpdate()
	ts.Require().NoError(err)
	state, err := ts.l1Client.BuildInitialState(finality.Data.FinalizedHeader.Execution.BlockNumber)
	ts.Require().NoError(err)
	l1Config, err := ts.l1Client.BuildL1Config(state)
	ts.Require().NoError(err)

	bytes, err := l1Config.Marshal()
	ts.Require().NoError(err)
	fmt.Println(common.Bytes2Hex(bytes))
}

func (ts *L1TestSuite) Test_SamePeriod() {
	l1Header, err := ts.l1Client.GetLatestFinalizedL1Header()
	ts.Require().NoError(err)

	// Not additional period
	finalizedSlot := l1Header.ConsensusUpdate.FinalizedHeader.Slot
	ctx := context.Background()
	pastPeriod, err := ts.l1Client.GetSyncCommitteeBySlot(ctx, finalizedSlot, l1Header)
	ts.Require().NoError(err)
	ts.Require().Len(pastPeriod, 0)

	// With sync committee
	ts.Require().True(l1Header.TrustedSyncCommittee != nil)
	bytes, err := l1Header.Marshal()
	ts.Require().NoError(err)

	consState := &types.ConsensusState{
		L1Slot:                 finalizedSlot - 1,
		L1CurrentSyncCommittee: l1Header.TrustedSyncCommittee.SyncCommittee.AggregatePubkey,
	}
	fmt.Println(common.Bytes2Hex(bytes))
	fmt.Println(consState.L1Slot)
	fmt.Println(common.Bytes2Hex(consState.L1CurrentSyncCommittee))
	fmt.Println(time.Now().Unix())
}

func (ts *L1TestSuite) Test_MultiPeriod() {
	l1Header, err := ts.l1Client.GetLatestFinalizedL1Header()
	ts.Require().NoError(err)

	// Not additional period
	trustedSlot := l1Header.ConsensusUpdate.SignatureSlot - (3 * ts.l1Client.slotsPerEpoch() * ts.l1Client.epochsPerSyncCommitteePeriod())
	ctx := context.Background()
	pastPeriod, err := ts.l1Client.GetSyncCommitteeBySlot(ctx, trustedSlot, l1Header)
	ts.Require().NoError(err)
	ts.Require().Len(pastPeriod, 3)

	// oldest is same period as first
	consState := &types.ConsensusState{
		L1Slot:              trustedSlot,
		L1NextSyncCommittee: pastPeriod[0].TrustedSyncCommittee.SyncCommittee.AggregatePubkey,
	}

	for i, period := range pastPeriod {
		fmt.Println(i)
		bytes, err := period.Marshal()
		ts.Require().NoError(err)
		fmt.Println(common.Bytes2Hex(bytes))
		fmt.Println(consState.L1Slot)
		fmt.Println(common.Bytes2Hex(consState.L1CurrentSyncCommittee))
		fmt.Println(common.Bytes2Hex(consState.L1NextSyncCommittee))
		fmt.Println(time.Now().Unix())

		consState = &types.ConsensusState{
			L1Slot:                 period.ConsensusUpdate.FinalizedHeader.Slot,
			L1CurrentSyncCommittee: consState.L1NextSyncCommittee,
			L1NextSyncCommittee:    period.ConsensusUpdate.NextSyncCommittee.AggregatePubkey,
		}
	}

	// With sync committee
	ts.Require().True(l1Header.TrustedSyncCommittee != nil)
	bytes, err := l1Header.Marshal()
	ts.Require().NoError(err)
	fmt.Println(common.Bytes2Hex(bytes))
	fmt.Println(consState.L1Slot)
	fmt.Println(common.Bytes2Hex(consState.L1CurrentSyncCommittee))
	fmt.Println(common.Bytes2Hex(consState.L1NextSyncCommittee))
	fmt.Println(time.Now().Unix())

}
