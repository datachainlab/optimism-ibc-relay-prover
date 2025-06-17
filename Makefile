# git clone https://github.com/datachainlab/optimism-elc ../optimism-elc
OP_IBC_PROTO ?= ../optimism-elc/proto/definitions

DOCKER := $(shell which docker)

protoVer=0.14.0
protoImageName=ghcr.io/cosmos/proto-builder:$(protoVer)
protoImage=$(DOCKER) run --user 0 --rm -v $(CURDIR):/workspace --workdir /workspace $(protoImageName)

.PHONY: proto-import
proto-import:
	@echo "Importing Protobuf files"
	@rm -rf ./proto/ibc
	@cp -a $(OP_IBC_PROTO)/ibc ./proto/

.PHONY: proto-gen
proto-gen:
	@echo "Generating Protobuf files"
	$(protoImage) sh ./scripts/protocgen.sh

.PHONY: proto-update-deps
proto-update-deps:
	@echo "Updating Protobuf dependencies"
	$(DOCKER) run --user 0 --rm -v $(CURDIR)/proto:/workspace --workdir /workspace $(protoImageName) buf mod update

PREIMAGE_MAKER ?= ../
.PHONY: set-port
set-port:
	$(PREIMAGE_MAKER)/optimism-preimage-maker/scripts/port.sh
	@L2_ROLLUP_PORT=$$(jq -r '.l2RollupPort' hostPort.json);\
    L2_GETH_PORT=$$(jq -r '.l2GethPort' hostPort.json);\
    L1_GETH_PORT=$$(jq -r '.l1GethPort' hostPort.json);\
    L1_BEACON_PORT=$$(jq -r '.l1BeaconPort' hostPort.json);\
    scripts/template.sh $$L2_ROLLUP_PORT $$L2_GETH_PORT $${L1_BEACON_PORT} $$L1_GETH_PORT

.PHONY: contracts
contracts:
	make -C tests contracts
