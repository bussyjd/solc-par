// Hardhat 2 (validated with 2.29.1): override the subtask that resolves the solc build.
// Export SOLC_PAR_SOLC before compiling:
//
//   export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
//   npx hardhat compile --force
const { subtask } = require("hardhat/config");
const { TASK_COMPILE_SOLIDITY_GET_SOLC_BUILD } = require("hardhat/builtin-tasks/task-names");

const SOLC_PAR = "/absolute/path/to/solc-par";
const VERSION = "0.8.37";
const LONG_VERSION = "0.8.37+commit.f401782d"; // `solc --version` of the real binary

subtask(TASK_COMPILE_SOLIDITY_GET_SOLC_BUILD, async (args, hre, runSuper) => {
  if (args.solcVersion === VERSION) {
    return { compilerPath: SOLC_PAR, isSolcJs: false, version: VERSION, longVersion: LONG_VERSION };
  }
  return runSuper(); // other versions: Hardhat's own download
});

module.exports = {
  solidity: {
    version: VERSION,
    settings: { optimizer: { enabled: true, runs: 200 }, viaIR: true },
  },
};
