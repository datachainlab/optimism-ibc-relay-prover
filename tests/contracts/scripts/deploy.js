const PortTransfer = "transfer"

function saveAddress(contractName, contract) {
    const fs = require("fs");
    const path = require("path");

    const dirpath = "addresses";
    if (!fs.existsSync(dirpath)) {
        fs.mkdirSync(dirpath, {recursive: true});
    }

    const filepath = path.join(dirpath, contractName);
    fs.writeFileSync(filepath, contract.target);

    console.log(`${contractName} address:`, contract.target);
}

async function deploy(deployer, contractName, args = []) {
    const factory = await hre.ethers.getContractFactory(contractName);
    console.log(`Contract ${contractName} deploy start`);
    const contract = await factory.connect(deployer).deploy(...args);
    await contract.waitForDeployment();
    saveAddress(contractName, contract)
    return contract;
}

async function deployIBC(deployer) {
    const logicNames = [
        "IBCClient",
        "IBCConnectionSelfStateNoValidation",
        "IBCChannelHandshake",
        "IBCChannelPacketSendRecv",
        "IBCChannelPacketTimeout",
        "IBCChannelUpgradeInitTryAck",
        "IBCChannelUpgradeConfirmOpenTimeoutCancel"
    ];
    const logics = [];
    for (const name of logicNames) {
        const logic = await deploy(deployer, name);
        logics.push(logic);
    }
    return deploy(deployer, "OwnableIBCHandler", logics.map(l => l.target));
}

async function main() {
    // This is just a convenience check
    if (network.name === "hardhat") {
        console.warn(
            "You are trying to deploy a contract to the Hardhat Network, which" +
            "gets automatically created and destroyed every time. Use the Hardhat" +
            " option '--network localhost'"
        );
    }

    // ethers is available in the global scope
    const [deployer] = await hre.ethers.getSigners();
    console.log(
        "Deploying the contracts with the account:",
        await deployer.getAddress()
    );
    console.log("Account balance:", (await hre.ethers.provider.getBalance(deployer.getAddress())).toString());

    const ibcHandler = await deployIBC(deployer);

    // deploy client
    const mockClient = await deploy(deployer, "MockClient", [ibcHandler.target]);
    await ibcHandler.registerClient("mock-client", mockClient.target).then(tx => tx.wait());

    // deploy ERC20 token
    await deploy(deployer, "ERC20Token", ["simple_erc_20_token_for_test", "simple_erc_20_token_for_test", 1000000]);
    const ibc20Bank = await deploy(deployer, "ICS20Bank");
    const ics20TransferBank = await deploy(deployer, "ICS20TransferBank", [ibcHandler.target, ibc20Bank.target]);
    await ibcHandler.bindPort(PortTransfer, ics20TransferBank.target).then(tx => tx.wait());
    const accounts = await hre.ethers.getSigners();
    await ibc20Bank.setOperator(ics20TransferBank.target).then(tx => tx.wait());
    await ibc20Bank.setOperator(accounts[0].address).then(tx => tx.wait());
}

if (require.main === module) {
    main()
        .then(() => process.exit(0))
        .catch((error) => {
            console.error(error);
            process.exit(1);
        });
}

exports.deployIBC = deployIBC;
