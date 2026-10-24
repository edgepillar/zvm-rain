# Isolated contract behavior

Run `npm ci && npm test` in this directory with Node 22 or later. The independent
lock keeps the production compiler and web dependency graphs unchanged.

The suite freshly compiles the current `Rain.sol` with Solidity 0.8.19, Paris,
optimizer 200 and the production build settings. It compares the complete
creation/runtime artifact, ABI and standard JSON input with the Go-embedded
artifact. CI also runs the actual production build and rejects artifact drift.

Native payments and ordinary wrapped-token transfers execute the original
`zvm-node` WZNN creation bytecode retained in `upstream-wznn`, including actual
deposit, approval, transfer and withdrawal calls. Its exact source, artifact,
upstream commit and SHA-256 hashes are recorded in `provenance.json` and checked
before execution. The source is GPL-3.0-or-later; its SPDX notice and the full GPL
text are retained. The wrapper is not rebuilt by the Rain compiler, and this
suite does not establish a fresh source-to-bytecode equivalence for it.

Rain is compiled for Paris. The synthetic EthereumJS VM uses Osaka explicitly
to execute the retained Solidity 0.8.30 wrapper. The suite checks constructors,
operator rotation, opt-in, ordering, duplicate and count limits, replay IDs,
finite/infinite approvals, amount overflow, native and ERC20 payments, wrapped
fallback and atomic rollback. Small controlled fixtures exercise ERC20 return
formats, wrapper failures and recipients that reject, exhaust their gas or call
back. A callback recipient is also the operator so the reentrancy assertion
actually reaches Rain's lock. Four fixed seeds exercise 128 mixed operations
against independent payment/allowance and decoded receipt models. Failed calls
must restore the entire state root and retain no logs; Rain storage is never
overridden. Native inventory and wrapped collateral are checked throughout.

These are unsigned in-memory EVM frames. They do not cover canonical transaction
validation, intrinsic gas, fees, ZVM execution, RPC, the Telegram bot, real wallet
flows or production operation. The existing Go live test remains gated by its
explicit environment configuration; a skip is not integration evidence.
