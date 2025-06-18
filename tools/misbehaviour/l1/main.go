package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/errors"
	types2 "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/prover/l1"
	"github.com/datachainlab/optimism-ibc-relay-prover/module/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hyperledger-labs/yui-relayer/log"
	"os"
	"time"
)

func main() {
	_ = log.InitLogger("debug", "text", "stdout")
	ctx := context.Background()
	if err := run(ctx); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}

type HostPort struct {
	L1BeaconPort int `json:"l1BeaconPort"`
	L1GethPort   int `json:"l1GethPort"`
	L2GethPort   int `json:"l2GethPort"`
}

type Config struct {
	ProverL1Client *l1.L1Client
	L1Client       *ethclient.Client
}

func run(ctx context.Context) error {
	hostPortJson, err := os.ReadFile("../hostPort.json")
	if err != nil {
		return errors.WithStack(err)
	}
	var hostPort HostPort
	if err = json.Unmarshal(hostPortJson, &hostPort); err != nil {
		return errors.WithStack(err)
	}

	proverL1Client, err := l1.NewL1Client(ctx,
		fmt.Sprintf("http://localhost:%d", hostPort.L1BeaconPort),
		fmt.Sprintf("http://localhost:%d", hostPort.L1GethPort),
		log.GetLogger().WithModule("l1"),
	)
	if err != nil {
		return errors.WithStack(err)
	}

	l1Header1, err := proverL1Client.GetLatestFinalizedL1Header(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Header2, err := proverL1Client.GetLatestFinalizedL1Header(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	if l1Header1.ConsensusUpdate.FinalizedHeader.Slot != l1Header2.ConsensusUpdate.FinalizedHeader.Slot {
		return errors.New("target l1 headers are not equal")
	}

	l1InitialState, err := proverL1Client.BuildInitialState(ctx, l1Header1.ExecutionUpdate.BlockNumber)
	if err != nil {
		return errors.WithStack(err)
	}
	l1Config, err := proverL1Client.BuildL1Config(l1InitialState, 0, 86400*time.Second)
	if err != nil {
		return errors.WithStack(err)
	}

	l1Header1.TrustedSyncCommittee = &types.TrustedSyncCommittee{
		IsNext: false,
		SyncCommittee: &types.SyncCommittee{
			Pubkeys:         l1InitialState.CurrentSyncCommittee.Pubkeys,
			AggregatePubkey: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		},
	}
	l1Header2.TrustedSyncCommittee = l1Header1.TrustedSyncCommittee

	// Change for misbehaviour detection
	l1Header2.ConsensusUpdate.FinalizedHeader.ProposerIndex = l1Header1.ConsensusUpdate.FinalizedHeader.ProposerIndex + 1

	misbehaviour := types.FinalizedHeaderMisbehaviour{
		ClientId: "optimism-01",
		TrustedHeight: &types2.Height{
			RevisionNumber: 0,
			RevisionHeight: 100,
		},
		TrustedSyncCommittee: l1Header1.TrustedSyncCommittee,
		ConsensusUpdate_1:    l1Header1.ConsensusUpdate,
		ConsensusUpdate_2:    l1Header2.ConsensusUpdate,
	}
	clientMessage, err := types2.PackClientMessage(&misbehaviour)
	if err != nil {
		return errors.WithStack(err)
	}

	cs := &types.ClientState{
		LatestHeight: misbehaviour.TrustedHeight,
		L1Config:     l1Config,
	}
	consState := types.ConsensusState{
		L1Slot:                 l1InitialState.Slot,
		L1CurrentSyncCommittee: l1InitialState.CurrentSyncCommittee.AggregatePubkey,
		L1NextSyncCommittee:    l1InitialState.NextSyncCommittee.AggregatePubkey,
		L1Timestamp:            l1InitialState.Timestamp,
		// unused
		OutputRoot:  make([]byte, 32),
		StorageRoot: make([]byte, 32),
	}

	misbehaviourBytes, err := clientMessage.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	if err = os.WriteFile("submit_misbehaviour_l1.bin", misbehaviourBytes, 0644); err != nil {
		return errors.WithStack(err)
	}

	csBytes, err := cs.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	consStateBytes, err := consState.Marshal()
	if err != nil {
		return errors.WithStack(err)
	}
	fmt.Printf("ClientState: %s\n", common.Bytes2Hex(csBytes))
	fmt.Printf("ConsState: %s\n", common.Bytes2Hex(consStateBytes))
	fmt.Printf("now: %d\n", time.Now().Unix())

	return nil
}
