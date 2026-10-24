// Fresh Paris compilation of Rain; synthetic Osaka frames for upstream WZNN.
import assert from 'node:assert/strict'
import {createHash} from 'node:crypto'
import {readFileSync} from 'node:fs'
import {createBlock} from '@ethereumjs/block'
import {createCustomCommon, Hardfork, Mainnet} from '@ethereumjs/common'
import {createAccount, createAddressFromString, bytesToHex, hexToBytes} from '@ethereumjs/util'
import {createVM} from '@ethereumjs/vm'
import {decodeErrorResult, decodeEventLog, decodeFunctionResult, encodeDeployData, encodeFunctionData, getAddress} from 'viem'
import solc from 'solc'

export const ZERO = '0x0000000000000000000000000000000000000000'
export const MAX = (1n << 256n) - 1n, MAX_ID = (1n << 64n) - 1n, INITIAL = 10n ** 30n
export const sorted = (addresses) => [...addresses].sort((a, b) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0)
const file = (name) => readFileSync(new URL(name, import.meta.url))
export const upstream = JSON.parse(file('./upstream-wznn/provenance.json'))
for (const [name, pin] of Object.entries(upstream.files)) {
  const bytes = file('./upstream-wznn/' + name)
  assert.equal(bytes.length, pin.bytes)
  assert.equal(createHash('sha256').update(bytes).digest('hex'), pin.sha256, 'upstream fixture must remain byte-for-byte pinned')
}
export const wrapperArtifact = JSON.parse(file('./upstream-wznn/WZNN.json'))
assert.match(solc.version(), /^0\.8\.19\+commit\.7dd6d404\./)
export const input = {language: 'Solidity', sources: {'Rain.sol': {content: file('../Rain.sol').toString()}},
  settings: {evmVersion: 'paris', libraries: {}, metadata: {bytecodeHash: 'ipfs'}, optimizer: {enabled: true, runs: 200},
    remappings: [], outputSelection: {'*': {'*': ['abi', 'evm.bytecode.object', 'evm.deployedBytecode.object', 'metadata']}}}}
const output = JSON.parse(solc.compile(JSON.stringify(input)))
assert.deepEqual((output.errors ?? []).filter((e) => e.severity === 'error'), [])
const c = output.contracts['Rain.sol'].Rain
assert.ok(c.evm.deployedBytecode.object.length / 2 <= 24_576)
export const artifact = {contractName: 'Rain', sourceName: 'Rain.sol', compiler: solc.version(), compilerVersion: 'v0.8.19+commit.7dd6d404',
  abi: c.abi, bytecode: '0x' + c.evm.bytecode.object, deployedBytecode: '0x' + c.evm.deployedBytecode.object, standardJsonInput: input}
const fixtureOutput = JSON.parse(solc.compile(JSON.stringify({language: 'Solidity',
  sources: {'Fixtures.sol': {content: file('./Fixtures.sol').toString()}}, settings: {evmVersion: 'paris', optimizer: {enabled: true, runs: 200},
    outputSelection: {'*': {'*': ['abi', 'evm.bytecode.object']}}}})))
assert.deepEqual((fixtureOutput.errors ?? []).filter((e) => e.severity === 'error'), [])
export const fixtureArtifacts = Object.fromEntries(Object.entries(fixtureOutput.contracts['Fixtures.sol']).map(([name, value]) =>
  [name, {abi: value.abi, bytecode: '0x' + value.evm.bytecode.object}]))

export async function fixture({wrapper = wrapperArtifact} = {}) {
  // The retained Solidity 0.8.30 wrapper can use post-Paris instructions.
  // This is an explicit VM choice, not evidence of ZVM engine compatibility.
  const common = createCustomCommon({chainId: 7340469}, Mainnet, {hardfork: Hardfork.Osaka})
  const vm = await createVM({common})
  const actors = Array.from({length: 8}, (_, n) => getAddress('0x' + (0x10000n + BigInt(n)).toString(16).padStart(40, '0')))
  const [deployer, operator, alice, bob, carol, dave, heir, outsider] = actors
  const address = createAddressFromString, inventory = new Set(actors.map((at) => at.toLowerCase()))
  for (const actor of actors) await vm.stateManager.putAccount(address(actor), createAccount({balance: INITIAL}))
  const abis = new Map(), deployments = []
  const root = async () => bytesToHex(await vm.stateManager.getStateRoot())
  const balance = async (at) => (await vm.stateManager.getAccount(address(at)))?.balance ?? 0n
  async function frame({from = operator, to, data = '0x', value = 0n, gas = 50_000_000n, isStatic = false, error}) {
    vm.stateManager.originalStorageCache.clear(); vm.evm.journal.cleanJournal()
    const before = to ? await root() : undefined
    const block = createBlock({header: {number: 100n, timestamp: 1_900_000_100n, gasLimit: 60_000_000n}}, {common})
    // No signing, intrinsic gas, fees, transaction inclusion or ZVM execution.
    const result = await vm.evm.runCall({caller: address(from), ...(to ? {to: address(to)} : {}), data: hexToBytes(data),
      value, gasLimit: gas, isStatic, block, skipNonceIncrement: Boolean(to)})
    const returned = bytesToHex(result.execResult.returnValue), failed = Boolean(result.execResult.exceptionError)
    if (error !== undefined) {
      assert.ok(failed, 'expected an actual EVM failure')
      if (typeof error === 'string') {
        const decoded = decodeErrorResult({abi: artifact.abi, data: returned})
        assert.equal(decoded.errorName, 'Error'); assert.equal(decoded.args[0], error)
      } else if (typeof error === 'object') {
        const decoded = decodeErrorResult({abi: artifact.abi, data: returned})
        assert.equal(decoded.errorName, 'Panic'); assert.equal(BigInt(decoded.args[0]), error.panic)
      }
      if (to) assert.equal(await root(), before, 'failed frames restore the entire state root, including native balances')
      assert.equal(result.execResult.logs?.length ?? 0, 0, 'failed frames retain no events')
    } else assert.equal(result.execResult.exceptionError, undefined, `EVM frame failed: ${returned}`)
    return {...result, returned, failed}
  }
  async function deploy(code, args = [], options = {}) {
    const result = await frame({from: deployer, data: encodeDeployData({abi: code.abi, bytecode: code.bytecode, args}), ...options})
    if (result.failed) return result
    const at = getAddress(result.createdAddress.toString())
    assert.ok((await vm.stateManager.getCode(address(at))).length > 0)
    abis.set(at.toLowerCase(), code.abi); inventory.add(at.toLowerCase()); deployments.push({address: at, result}); return at
  }
  const calldata = (to, functionName, args = []) => encodeFunctionData({abi: abis.get(to.toLowerCase()), functionName, args})
  const write = (from, to, name, args = [], value = 0n, options = {}) => frame({from, to, data: calldata(to, name, args), value, ...options})
  async function read(to, name, args = []) {
    const result = await frame({to, data: calldata(to, name, args), isStatic: true})
    return decodeFunctionResult({abi: abis.get(to.toLowerCase()), functionName: name, data: result.returned})
  }
  function events(result) {
    return (result.execResult.logs ?? []).map(([at, topics, data]) => ({address: bytesToHex(at),
      ...decodeEventLog({abi: abis.get(bytesToHex(at).toLowerCase()), topics: topics.map(bytesToHex), data: bytesToHex(data)})}))
  }
  const wznn = await deploy(wrapper), rain = await deploy(artifact, [operator, wznn])
  const token = await deploy(fixtureArtifacts.Token)
  const ownEvents = (result) => events(result).filter((e) => e.address.toLowerCase() === rain.toLowerCase())
  async function link(at, secret = 1n) { inventory.add(at.toLowerCase()); return write(at, rain, 'link', [secret]) }
  async function forward(receiver, to, name, args = [], value = 0n, options = {}) {
    return write(outsider, receiver, 'execute', [to, calldata(to, name, args)], value, options)
  }
  async function funded({from = alice, amount = 1000n, approval = amount} = {}) {
    await write(from, wznn, 'deposit', [], amount); await write(from, wznn, 'approve', [rain, approval])
    await write(deployer, token, 'mint', [from, amount]); await write(from, token, 'approve', [rain, approval])
  }
  async function pay({id = 1n, from = alice, token: paidToken = ZERO, each = 10n, to = [bob, carol], caller = operator, ...options} = {}) {
    return write(caller, rain, 'rain', [id, from, paidToken, each, to], 0n, options)
  }
  async function conserved() {
    let sum = 0n
    for (const at of inventory) sum += await balance(at)
    assert.equal(sum, BigInt(actors.length) * INITIAL, 'the known synthetic native-value inventory is conserved')
    let wrapped = 0n
    for (const at of inventory) wrapped += await read(wznn, 'balanceOf', [at])
    assert.equal(wrapped, await balance(wznn), 'all wrapped balances are fully collateralized')
    if (wrapper === wrapperArtifact) assert.equal(await read(wznn, 'totalSupply'), wrapped)
    assert.equal(await balance(rain), 0n)
    assert.equal(await read(wznn, 'balanceOf', [rain]), 0n)
  }
  return {vm, actors, deployer, operator, alice, bob, carol, dave, heir, outsider, wznn, rain, token, deployments,
    root, balance, frame, deploy, read, write, calldata, events, ownEvents, link, forward, funded, pay, conserved}
}
