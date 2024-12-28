package main

import (
	"github.com/datachainlab/ibc-hd-signer/pkg/hd"
	"log"

	"github.com/datachainlab/ethereum-ibc-relay-chain/pkg/relay/ethereum"
	"github.com/datachainlab/optimism-ibc-relay-prover/module"
	"github.com/hyperledger-labs/yui-relayer/cmd"
)

func main() {
	if err := cmd.Execute(
		ethereum.Module{},
		module.Module{},
		hd.Module{},
	); err != nil {
		log.Fatal(err)
	}
}
