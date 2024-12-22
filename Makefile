# git clone https://github.com/datachainlab/optimism-elc ../optimism-elc
OP_IBC_PROTO ?= ../optimism-elc/proto/definitions
ETH_IBC_PROTO ?= ../ethereum-ibc-relay-prover/proto

DOCKER := $(shell which docker)

protoVer=0.14.0
protoImageName=ghcr.io/cosmos/proto-builder:$(protoVer)
protoImage=$(DOCKER) run --user 0 --rm -v $(CURDIR):/workspace --workdir /workspace $(protoImageName)

.PHONY: proto-import
proto-import:
	@echo "Importing Protobuf files"
	@rm -rf ./proto/ibc
	@cp -a $(OP_IBC_PROTO)/ibc ./proto/
	@cp -a $(ETH_IBC_PROTO)/ibc ./proto/

.PHONY: proto-gen
proto-gen:
	@echo "Generating Protobuf files"
	$(protoImage) sh ./scripts/protocgen.sh

.PHONY: proto-update-deps
proto-update-deps:
	@echo "Updating Protobuf dependencies"
	$(DOCKER) run --user 0 --rm -v $(CURDIR)/proto:/workspace --workdir /workspace $(protoImageName) buf mod update
