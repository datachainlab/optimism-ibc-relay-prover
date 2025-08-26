# Run

Make sure that plural Dispute Games are created first.
If a game has been created, the following commands can be executed.

```
export DISPUTE_GAME_FACTORY_ADDRESS_PROXY=`cat ../../optimism-preimage-maker/chain/kurtosis-devnet/tests/simple-devnet.json | jq -r ".l2.[0] .l1_addresses .DisputeGameFactoryProxy"`
# confirm address is set
echo $DISPUTE_GAME_FACTORY_ADDRESS_PROXY
# make misbehaviour data
go run misbehaviour/l2/past/main.go
```
