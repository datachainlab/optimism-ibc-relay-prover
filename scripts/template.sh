#!/bin/bash

L2_ROLLUP_PORT=$1
L2_GETH_PORT=$2
L1_BEACON_PORT=$3
L1_GETH_PORT=$4

sed \
    -e "s/L2_ROLLUP_PORT/${L2_ROLLUP_PORT}/g" \
    -e "s/L2_GETH_PORT/${L2_GETH_PORT}/g" \
    -e "s/L1_BEACON_PORT/${L1_BEACON_PORT}/g" \
    -e "s/L1_GETH_PORT/${L1_GETH_PORT}/g" \
    tests/contracts/hardhat.config.template > tests/contracts/hardhat.config.js