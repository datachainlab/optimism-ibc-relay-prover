# Run

Make sure that plural Dispute Games are created first.
If a game has been created, the following commands can be executed.

```
# Set proxy address where DisputeGameFactoryProxy contract
# In devnet, address is seen in log by op-node
export DISPUTE_GAME_FACTORY_ADDRESS_PROXY=<DisputeGameFactoryProxy address>

# Make misbehaviour data for ELC test
go run misbehaviour/l2/past/main.go
```
