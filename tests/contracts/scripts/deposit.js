async function main() {
    console.log("Network", network.name)
    // ethers is available in the global scope
    const [deployer] = await hre.ethers.getSigners();
    console.log(
        "Deploying the contracts with the account:",
        await deployer.getAddress()
    );
    console.log("Account balance:", (await hre.ethers.provider.getBalance(deployer.getAddress())).toString());



    const L1_STANDARD_BRIDGE_ABI = [
        "function depositETH(uint32 _minGasLimit, bytes calldata _extraData) external payable"
    ];
    const amount = hre.ethers.parseEther("0.0001"); // Amount of ETH to deposit
    const gasLimit = 20000000;
    const tx = await deployer.sendTransaction({
        to: "0x9D34A2610Ea283f6d9AE29f9Cad82e00c4d38507",
        value: amount
    });
    /*
    const bridgeContract = await hre.ethers.getContractAt(L1_STANDARD_BRIDGE_ABI, '0x9D34A2610Ea283f6d9AE29f9Cad82e00c4d38507');
    const tx = await bridgeContract.depositETH(gasLimit, "0x", {
        value: amount,
        gasLimit: gasLimit,
        from: deployer
    });
     */
    console.log("Deposit tx result:", tx);
    const receipt = await tx.wait();
    console.log(receipt);

}

if (require.main === module) {
    main()
        .then(() => process.exit(0))
        .catch((error) => {
            console.error(error);
            process.exit(1);
        });
}

