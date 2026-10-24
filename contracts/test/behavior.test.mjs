import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import test from 'node:test'
import {decodeErrorResult} from 'viem'
import {artifact, fixture, fixtureArtifacts, INITIAL, MAX, MAX_ID, sorted, upstream, ZERO} from './evm.mjs'

const first = (f, result, name) => f.ownEvents(result).find((e) => e.eventName === name)?.args
async function ready(options) {
  const f = await fixture(options)
  for (const at of [f.alice, f.bob, f.carol, f.dave]) await f.link(at)
  await f.funded()
  return f
}
async function receiver(f, mode) {
  const at = await f.deploy(fixtureArtifacts.Receiver)
  await f.write(f.outsider, at, 'configure', [mode, ZERO, '0x'])
  await f.forward(at, f.rain, 'link', [7n])
  return at
}

test('fresh source matches the complete tracked creation/runtime artifact and compiler input', () => {
  assert.deepEqual(artifact, JSON.parse(readFileSync(new URL('../../internal/contracts/artifacts/Rain.json', import.meta.url))))
  assert.equal(artifact.standardJsonInput.settings.evmVersion, 'paris')
  assert.equal(upstream.commit, 'ae90b945b0ea95887c31cfb2f19e0b88504186d8')
  assert.equal(upstream.source_recompiled_by_this_suite, false)
})

test('real constructors pin operator and wrapper, emit the role event and reject invalid inputs', async () => {
  const f = await fixture()
  assert.equal(await f.read(f.rain, 'operator'), f.operator)
  assert.equal(await f.read(f.rain, 'wznn'), f.wznn)
  assert.equal(await f.read(f.rain, 'ZNN'), ZERO)
  assert.equal(await f.read(f.rain, 'MAX_RECIPIENTS'), 100n)
  assert.equal(await f.read(f.rain, 'PUSH_GAS'), 30_000n)
  assert.deepEqual(first(f, f.deployments[1].result, 'OperatorChanged'), {previous: ZERO, next: f.operator})
  await f.deploy(artifact, [ZERO, f.wznn], {error: 'null operator'})
  for (const at of [ZERO, f.alice]) await f.deploy(artifact, [f.operator, at], {error: 'WZNN is not a contract'})
})

test('link and unlink affect only the calling address and emit exact uint64 secrets', async () => {
  const f = await fixture()
  for (const secret of [0n, MAX_ID, 7n]) {
    assert.deepEqual(first(f, await f.link(f.alice, secret), 'Linked'), {account: f.alice, secret})
    assert.equal(await f.read(f.rain, 'linked', [f.alice]), true)
    assert.equal(await f.read(f.rain, 'linked', [f.bob]), false)
  }
  for (let i = 0; i < 2; i++) {
    assert.deepEqual(first(f, await f.write(f.alice, f.rain, 'unlink'), 'Unlinked'), {account: f.alice})
    assert.equal(await f.read(f.rain, 'linked', [f.alice]), false)
  }
})

test('only the operator can pay or rotate the role and a successful rotation revokes the previous role', async () => {
  const f = await ready()
  await f.pay({caller: f.outsider, error: 'not the operator'})
  await f.write(f.outsider, f.rain, 'setOperator', [f.heir], 0n, {error: 'not the operator'})
  await f.write(f.operator, f.rain, 'setOperator', [ZERO], 0n, {error: 'null operator'})
  const changed = await f.write(f.operator, f.rain, 'setOperator', [f.heir])
  assert.deepEqual(first(f, changed, 'OperatorChanged'), {previous: f.operator, next: f.heir})
  await f.pay({error: 'not the operator'})
  await f.pay({caller: f.heir})
  await f.write(f.operator, f.rain, 'setOperator', [f.operator], 0n, {error: 'not the operator'})
  assert.equal(await f.read(f.rain, 'operator'), f.heir)
  await f.conserved()
})

test('unsolicited native value is rejected while real WZNN withdrawals are accepted', async () => {
  const f = await ready(), before = await f.balance(f.alice)
  await f.frame({from: f.alice, to: f.rain, value: 5n, error: 'send nothing here; approve instead'})
  assert.equal(await f.balance(f.alice), before)
  await f.pay()
  assert.equal(await f.balance(f.bob), INITIAL + 10n)
  assert.equal(await f.balance(f.carol), INITIAL + 10n)
  assert.equal(await f.read(f.wznn, 'balanceOf', [f.alice]), 980n)
  assert.equal(await f.read(f.wznn, 'allowance', [f.alice, f.rain]), 980n)
  await f.conserved()
})

for (const [name, invalid, error] of [
  ['an unlinked sender', (f) => ({from: f.outsider}), 'sender not linked'],
  ['no recipients', () => ({to: []}), 'bad recipient count'],
  ['a zero share', () => ({each: 0n}), 'nothing to rain'],
  ['the zero recipient', () => ({to: [ZERO]}), 'recipients not ascending'],
  ['a duplicate recipient', (f) => ({to: [f.bob, f.bob]}), 'recipients not ascending'],
  ['descending recipients', (f) => ({to: [f.carol, f.bob]}), 'recipients not ascending'],
  ['the sender as recipient', (f) => ({to: [f.alice]}), 'sender among recipients'],
  ['an unlinked recipient', (f) => ({to: [f.outsider]}), 'recipient not linked'],
]) test(name + ' restores the rain id, balances, allowance and complete state', async () => {
  const f = await ready()
  await f.pay({...invalid(f), error})
  assert.equal(await f.read(f.rain, 'rained', [1n]), false)
  await f.pay() // The failed id remains usable.
  await f.conserved()
})

test('one and one hundred recipients are paid, while one hundred and one fail before transfers', async () => {
  const f = await ready(), recipients = []
  for (let i = 0; i < 101; i++) {
    const at = '0x' + (0x30000n + BigInt(i)).toString(16).padStart(40, '0')
    recipients.push(at); await f.link(at)
  }
  await f.pay({to: recipients, error: 'bad recipient count'})
  await f.pay({id: 1n, to: [recipients[0]], each: 1n})
  const result = await f.pay({id: 2n, to: recipients.slice(0, 100), each: 2n})
  assert.equal(first(f, result, 'Rained').recipients, 100n)
  for (let i = 0; i < 101; i++) assert.equal(await f.balance(recipients[i]), i === 0 ? 3n : i < 100 ? 2n : 0n)
  await f.conserved()
})

test('opting out immediately blocks approved spending and receipt until the address links again', async () => {
  const f = await ready()
  await f.write(f.alice, f.rain, 'unlink')
  await f.pay({error: 'sender not linked'})
  await f.link(f.alice); await f.write(f.bob, f.rain, 'unlink')
  await f.pay({error: 'recipient not linked'})
  await f.link(f.bob); await f.pay()
  await f.conserved()
})

test('zero and maximum uint64 ids are valid once and cannot be reused with a changed payload', async () => {
  const f = await ready()
  for (const id of [0n, MAX_ID]) {
    const result = await f.pay({id})
    assert.deepEqual(first(f, result, 'Rained'), {id, from: f.alice, token: ZERO, each: 10n, recipients: 2n})
    await f.pay({id, token: f.token, each: 1n, to: [f.dave], error: 'rain already paid'})
    assert.equal(await f.read(f.rain, 'rained', [id]), true)
  }
  await f.conserved()
})

test('native amount multiplication overflow fails atomically before a wrapper call', async () => {
  const f = await ready()
  await f.pay({each: MAX, error: {panic: 0x11n}})
  assert.equal(await f.read(f.rain, 'rained', [1n]), false)
  await f.conserved()
})

test('real WZNN enforces finite approvals and balance and preserves an infinite approval', async () => {
  const f = await ready()
  await f.write(f.alice, f.wznn, 'approve', [f.rain, 19n])
  await f.pay({error: 'token transfer failed'})
  await f.write(f.alice, f.wznn, 'approve', [f.rain, MAX])
  await f.pay({each: 501n, error: 'token transfer failed'})
  await f.pay({each: 500n})
  assert.equal(await f.read(f.wznn, 'allowance', [f.alice, f.rain]), MAX)
  assert.equal(await f.read(f.wznn, 'balanceOf', [f.alice]), 0n)
  await f.conserved()
})

test('the ERC20 branch pays real WZNN without unwrapping or changing native balances', async () => {
  const f = await ready(), collateral = await f.balance(f.wznn)
  const result = await f.pay({token: f.wznn})
  assert.deepEqual(first(f, result, 'Rained'), {id: 1n, from: f.alice, token: f.wznn, each: 10n, recipients: 2n})
  assert.equal(first(f, result, 'PaidWrapped'), undefined)
  for (const at of [f.bob, f.carol]) {
    assert.equal(await f.read(f.wznn, 'balanceOf', [at]), 10n)
    assert.equal(await f.balance(at), INITIAL)
  }
  assert.equal(await f.balance(f.wznn), collateral)
  await f.conserved()
})

test('an EOA token address fails rather than appearing to transfer successfully', async () => {
  const f = await ready()
  await f.pay({token: f.outsider, error: 'token is not a contract'})
  await f.conserved()
})

test('true, empty and extended true ERC20 return values complete actual transfers', async (t) => {
  for (const mode of [0, 1, 6]) await t.test('response ' + mode, async () => {
    const f = await ready()
    await f.write(f.outsider, f.token, 'configure', [mode, 0n])
    await f.pay({token: f.token})
    assert.equal(await f.read(f.token, 'balanceOf', [f.alice]), 980n)
    assert.equal(await f.read(f.token, 'allowance', [f.alice, f.rain]), 980n)
    for (const at of [f.bob, f.carol]) assert.equal(await f.read(f.token, 'balanceOf', [at]), 10n)
    await f.conserved()
  })
})

test('false, revert, short and invalid boolean replies undo earlier transfers and allow the same id to retry', async (t) => {
  for (const mode of [2, 3, 4, 5]) await t.test('response ' + mode, async () => {
    const f = await ready()
    await f.write(f.outsider, f.token, 'configure', [mode, 2n])
    await f.pay({token: f.token, error: mode === 5 ? true : 'token transfer failed'})
    assert.equal(await f.read(f.token, 'calls'), 0n)
    assert.equal(await f.read(f.rain, 'rained', [1n]), false)
    assert.equal(await f.read(f.token, 'balanceOf', [f.bob]), 0n)
    await f.write(f.outsider, f.token, 'configure', [0n, 0n])
    await f.pay({token: f.token})
    assert.equal(await f.read(f.token, 'calls'), 2n)
    await f.conserved()
  })
})

test('aggregate ERC20 allowance and balance exhaustion roll back the first recipient as well', async () => {
  const f = await ready()
  await f.write(f.alice, f.token, 'approve', [f.rain, 19n])
  await f.pay({token: f.token, error: 'token transfer failed'})
  await f.write(f.alice, f.token, 'approve', [f.rain, MAX])
  await f.pay({token: f.token, each: 501n, error: 'token transfer failed'})
  assert.equal(await f.read(f.token, 'calls'), 0n)
  await f.pay({token: f.token, each: 500n})
  assert.equal(await f.read(f.token, 'allowance', [f.alice, f.rain]), MAX)
  await f.conserved()
})

test('a receiving contract links through its own call and accepts the exact native share', async () => {
  const f = await ready(), at = await receiver(f, 0)
  const result = await f.pay({to: sorted([f.bob, at])})
  assert.equal(await f.read(f.rain, 'linked', [at]), true)
  assert.equal(await f.read(f.rain, 'linked', [f.outsider]), false)
  assert.equal(await f.balance(at), 10n)
  assert.deepEqual(f.events(result).find((e) => e.eventName === 'Received').args, {amount: 10n})
  assert.equal(first(f, result, 'PaidWrapped'), undefined)
  await f.conserved()
})

test('rejecting, gas-exhausting and reverted-state receivers receive actual WZNN instead', async (t) => {
  for (const mode of [1, 2, 3]) await t.test('receiver mode ' + mode, async () => {
    const f = await ready(), at = await receiver(f, mode), result = await f.pay({to: sorted([f.bob, at])})
    assert.equal(await f.balance(at), 0n)
    assert.equal(await f.read(at, 'attempted'), 0n)
    assert.equal(await f.read(f.wznn, 'balanceOf', [at]), 10n)
    assert.equal(await f.balance(f.bob), INITIAL + 10n)
    assert.deepEqual(first(f, result, 'PaidWrapped'), {id: 1n, account: at, amount: 10n})
    assert.equal(f.ownEvents(result).at(-1).eventName, 'Rained')
    assert.equal(f.events(result).filter((e) => e.eventName === 'Received').length, 0)
    await f.conserved()
  })
})

test('a recipient that is also the operator reaches the reentrancy guard and cannot pay a second id', async () => {
  const f = await ready(), at = await receiver(f, 4)
  const nested = f.calldata(f.rain, 'rain', [2n, f.alice, ZERO, 1n, [f.dave]])
  await f.write(f.outsider, at, 'configure', [4n, f.rain, nested])
  await f.write(f.operator, f.rain, 'setOperator', [at])
  const result = await f.forward(at, f.rain, 'rain', [1n, f.alice, ZERO, 10n, sorted([f.bob, at])])
  const callback = f.events(result).find((e) => e.eventName === 'Callback')
  assert.ok(callback, 'the callback must execute within PUSH_GAS, rather than silently falling back')
  assert.equal(callback.args.ok, false)
  assert.equal(decodeErrorResult({abi: artifact.abi, data: callback.args.result}).args[0], 'reentrant call')
  assert.equal(await f.read(f.rain, 'rained', [1n]), true)
  assert.equal(await f.read(f.rain, 'rained', [2n]), false)
  assert.equal(await f.balance(f.dave), INITIAL)
  assert.equal(await f.balance(at), 10n)
  assert.equal(first(f, result, 'PaidWrapped'), undefined)
  // A later, nonnested operation proves that the lock is released.
  await f.forward(at, f.rain, 'rain', [2n, f.alice, ZERO, 1n, [f.dave]])
  await f.conserved()
})

test('wrapper failures at collection, withdrawal, rewrap and fallback transfer restore all state', async (t) => {
  for (const stage of [1, 2, 3, 4]) await t.test('wrapper stage ' + stage, async () => {
    const f = await ready({wrapper: fixtureArtifacts.FaultyWrapper}), at = await receiver(f, 1)
    // At least one plain-native recipient precedes the rejecting recipient.
    const recipients = sorted([f.bob, at])
    await f.write(f.outsider, f.wznn, 'configure', [stage])
    await f.pay({to: recipients, error: true})
    assert.equal(await f.read(f.rain, 'rained', [1n]), false)
    assert.equal(await f.balance(f.bob), INITIAL)
    assert.equal(await f.read(f.wznn, 'balanceOf', [at]), 0n)
    await f.write(f.outsider, f.wznn, 'configure', [0n])
    await f.pay({to: recipients})
    assert.equal(await f.read(f.wznn, 'balanceOf', [at]), 10n)
    await f.conserved()
  })
})

function random(seed) {
  let state = seed >>> 0
  return () => { state ^= state << 13; state ^= state >>> 17; state ^= state << 5; return state >>> 0 }
}

test('seeded histories match independent payment, opt-in, allowance and receipt models', async (t) => {
  for (const seed of [3, 17, 0x12345678, 0x9e3779b9]) await t.test('seed ' + seed, async () => {
    const f = await ready(), reject = await receiver(f, 1), next = random(seed)
    await f.funded({from: f.bob})
    const accounts = [...f.actors, reject], senders = [f.alice, f.bob], tokens = [f.wznn, f.token]
    const linked = new Map(accounts.map((at) => [at, [f.alice, f.bob, f.carol, f.dave, reject].includes(at)]))
    const receiptLinks = new Map(linked), receiptIds = new Map(), ids = new Set()
    const balances = new Map(tokens.map((token) => [token, new Map(accounts.map((at) => [at, senders.includes(at) ? 1000n : 0n]))]))
    const approvals = new Map(tokens.map((token) => [token, new Map(senders.map((at) => [at, 1000n]))]))
    const native = new Map(accounts.map((at) => [at, at === reject ? 0n : INITIAL - (senders.includes(at) ? 1000n : 0n)]))
    let successes = 0, failures = 0
    for (let step = 0; step < 32; step++) {
      const kind = step < 3 ? 2 : next() % 5
      if (kind === 0) {
        const at = accounts[2 + next() % 4], active = !linked.get(at)
        const result = active ? await f.link(at, BigInt(step)) : await f.write(at, f.rain, 'unlink')
        linked.set(at, active)
        for (const event of f.ownEvents(result)) receiptLinks.set(event.args.account, event.eventName === 'Linked')
      } else if (kind === 1) {
        const from = senders[next() % 2], token = tokens[next() % 2], amount = next() % 3 === 0 ? MAX : BigInt(next() % 80)
        await f.write(from, token, 'approve', [f.rain, amount]); approvals.get(token).set(from, amount)
      } else {
        const from = senders[next() % 2], paid = step === 0 ? ZERO : step === 1 ? f.wznn : step === 2 ? f.token : [ZERO, ...tokens][next() % 3]
        const id = BigInt(step < 3 ? step : next() % 16), each = BigInt(1 + next() % 9)
        let to = sorted([f.carol, f.dave, reject].slice(0, 1 + next() % 3))
        if (step > 2 && next() % 7 === 0) to = [f.carol, f.carol]
        const token = paid === ZERO ? f.wznn : paid, total = each * BigInt(to.length)
        const valid = !ids.has(id) && linked.get(from) && to.every((at) => linked.get(at)) && new Set(to).size === to.length &&
          balances.get(token).get(from) >= total && approvals.get(token).get(from) >= total
        const result = await f.pay({id, from, token: paid, each, to, ...(valid ? {} : {error: true})})
        if (valid) {
          successes++; ids.add(id)
          balances.get(token).set(from, balances.get(token).get(from) - total)
          if (approvals.get(token).get(from) !== MAX) approvals.get(token).set(from, approvals.get(token).get(from) - total)
          for (const at of to) {
            if (paid === ZERO && at !== reject) native.set(at, native.get(at) + each)
            else balances.get(token).set(at, balances.get(token).get(at) + each)
          }
          const logs = f.ownEvents(result), paidLogs = logs.filter((e) => e.eventName === 'PaidWrapped')
          assert.deepEqual(paidLogs.map((e) => e.args), paid === ZERO && to.includes(reject) ? [{id, account: reject, amount: each}] : [])
          for (const event of logs.filter((e) => e.eventName === 'Rained')) {
            assert.ok(!receiptIds.has(event.args.id)); receiptIds.set(event.args.id, event.args)
          }
          assert.deepEqual(receiptIds.get(id), {id, from, token: paid, each, recipients: BigInt(to.length)})
        } else failures++
      }
      assert.deepEqual(receiptLinks, linked)
      assert.deepEqual(new Set(receiptIds.keys()), ids)
      for (const at of accounts) {
        assert.equal(await f.read(f.rain, 'linked', [at]), linked.get(at))
        assert.equal(await f.balance(at), native.get(at))
        for (const token of tokens) assert.equal(await f.read(token, 'balanceOf', [at]), balances.get(token).get(at))
      }
      for (const id of Array.from({length: 16}, (_, i) => BigInt(i))) assert.equal(await f.read(f.rain, 'rained', [id]), ids.has(id))
      for (const token of tokens) for (const from of senders) assert.equal(await f.read(token, 'allowance', [from, f.rain]), approvals.get(token).get(from))
      await f.conserved()
    }
    assert.ok(successes >= 3 && failures > 0, 'each seed must exercise actual success and rollback')
  })
})
