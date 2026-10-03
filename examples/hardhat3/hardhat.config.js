// Hardhat 3 (validated with 3.18.1): a custom compiler is a config field.
// `path` makes Hardhat run that binary with `--standard-json --no-import-callback`
// instead of downloading solc for `version`. Export SOLC_PAR_SOLC before compiling:
//
//   export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
//   npx hardhat compile --force
//
// package.json needs `"type": "module"` (Hardhat 3 configs are ESM).
export default {
  solidity: {
    version: "0.8.37",
    path: "/absolute/path/to/solc-par",
    settings: { optimizer: { enabled: true, runs: 200 }, viaIR: true },
  },
};

// With build profiles / several compilers, `path` goes on each compiler object:
// export default {
//   solidity: {
//     profiles: {
//       default: { compilers: [{ version: "0.8.37", path: "/absolute/path/to/solc-par" }] },
//     },
//   },
// };
