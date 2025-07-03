# optimism-ibc-relay-prover

![CI](https://github.com/datachainlab/optimism-ibc-relay-prover/workflows/CI/badge.svg?branch=main)

## Setup Relayer

Add this module to [yui-relayer](https://github.com/hyperledger-labs/yui-relayer) and activate it.

```go
package main

import (
	"log"
	"github.com/hyperledger-labs/yui-relayer/cmd"
	optimism "github.com/datachainlab/optimism-ibc-relay-prover/module"
)

func main() {
	if err := cmd.Execute(
		// counterparty.Module{}, //counter party
		optimism.Module{}, // Optimism Prover Module 
    ); err != nil {
		log.Fatal(err)
	}
}
```

## Development

### Generate proto
```
make proto-import
make proto-gen
```

### Test

```
cd ../
git clone https://github.com/datachainlab/optimism-preimage-maker.git
```

First of all Start optimism-devnet and `optimism-preimage-maker`
 - See [optimism-preimage-maker](https://github.com/datachainlab/optimism-preimage-maker) to launch server.

```
make set-port
make contracts
go run test ./...
```