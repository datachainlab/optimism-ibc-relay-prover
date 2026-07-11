#!/usr/bin/env bash

set -eo pipefail

echo "Generating gogo proto code"
cd proto

# Generate code, excluding ethereum.proto (imported from ethereum-light-client-types)
buf generate --template buf.gen.gogo.yaml --exclude-path ibc/lightclients/ethereum/v1/ethereum.proto

cd ..

# move proto files to the right places
cp -r github.com/datachainlab/optimism-ibc-relay-prover/* ./
rm -rf github.com

# go mod tidy is run separately after proto generation
