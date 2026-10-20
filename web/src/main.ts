// Rain's page: the two things the bot cannot do for anyone, done as
// transactions their own wallet signs. Linking calls link(secret) on Rain;
// letting Rain spend calls approve(Rain, amount) on the token itself, since an
// ERC-20 approval can only come from its owner. ZNN has no approve, so it is
// approved as WZNN, wrapped first when the wallet holds too little.
//
// The bot sends people here with the work filled in: ?link=<secret> from
// /link, ?token=<name or 0x…>&amount=<n> from /fund, and &revoke=1 from
// /revoke.
import '@fontsource-variable/space-grotesk'
import '@fontsource-variable/jetbrains-mono'
import './style.css'
import {
  createPublicClient,
  createWalletClient,
  custom,
  defineChain,
  encodeFunctionData,
  formatUnits,
  getAddress,
  http,
  isAddress,
  maxUint256,
  parseAbi,
  parseUnits,
  type Address,
  type Hex,
} from 'viem'
import {record as deployed} from './deployment'
import {startRain} from './rain'

startRain(document.getElementById('rain') as HTMLCanvasElement)

// --- the chain ---------------------------------------------------------------

// boot.ts starts the page only when the record is there.
const record = deployed!
const RAIN = getAddress(record.rain)
const WZNN = getAddress(record.wznn)
const CHAIN_ID = record.chainId
// The node this page reads, and where the explorer is, come from the
// environment (see .env.example), so no node is written into the source.
// VITE_ZVM_NODE is a ZVM node's base URL, its JSON-RPC at /rpc; a local
// zvm-devnet's is http://localhost:47080/zvm.
const RPC = `${import.meta.env.VITE_ZVM_NODE.replace(/\/+$/, '')}/rpc`
const EXPLORER = `${import.meta.env.VITE_GATEWAY.replace(/\/+$/, '')}/explorer`

// Reads go through that node; the wallet sends through its own copy of the
// same endpoint.
const zvm = defineChain({
  id: CHAIN_ID,
  name: 'ZVM testnet',
  nativeCurrency: {name: 'Zenon', symbol: 'ZNN', decimals: 18},
  rpcUrls: {default: {http: [RPC]}},
})
const reader = createPublicClient({chain: zvm, transport: http(RPC)})

const erc20 = parseAbi([
  'function symbol() view returns (string)',
  'function decimals() view returns (uint8)',
  'function balanceOf(address) view returns (uint256)',
  'function allowance(address, address) view returns (uint256)',
  'function approve(address, uint256) returns (bool)',
  'function deposit() payable',
])
const rainAbi = parseAbi(['function link(uint64 secret)', 'function unlink()', 'function linked(address) view returns (bool)'])

// The relayer refuses a tip under 50 gwei; zvm's own senders offer 100.
const TIP = 100_000_000_000n

// --- tokens ------------------------------------------------------------------

interface Token {
  key: string // what goes in ?token=
  name: string // what is shown
  address: Address // where the approval lives: WZNN for ZNN
  decimals: number
  native: boolean
}

// RAIN_TOKENS from zvm-rain/.env, compiled in: "wQSR=0x…,BULLET=0x…".
const named: {name: string; address: Address}[] = __RAIN_TOKENS__
  .split(',')
  .map((p) => p.trim().split('='))
  .filter(([n, a]) => n && a && isAddress(a.trim()))
  .map(([n, a]) => ({name: n.trim(), address: getAddress(a.trim())}))

const tokenCache = new Map<string, Token>()

async function tokenFor(key: string): Promise<Token> {
  const k = key.trim()
  if (!k || k.toLowerCase() === 'znn') return {key: 'znn', name: 'ZNN', address: WZNN, decimals: 18, native: true}
  const hit = named.find((n) => n.name.toLowerCase() === k.toLowerCase())
  const address = hit?.address ?? (isAddress(k) ? getAddress(k) : null)
  if (!address) throw new Error(`Unknown token ${k}. Use ${['ZNN', ...named.map((n) => n.name)].join(', ')}, or a 0x address.`)
  const cached = tokenCache.get(address)
  if (cached) return cached
  const [decimals, symbol] = await Promise.all([
    reader.readContract({address, abi: erc20, functionName: 'decimals'}),
    hit ? Promise.resolve(hit.name) : reader.readContract({address, abi: erc20, functionName: 'symbol'}).catch(() => address.slice(0, 8)),
  ])
  const t = {key: hit ? hit.name.toLowerCase() : address, name: symbol, address, decimals, native: false}
  tokenCache.set(address, t)
  return t
}

function fmt(v: bigint, decimals: number): string {
  const [whole, frac = ''] = formatUnits(v, decimals).split('.')
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const f = frac.replace(/0+$/, '').slice(0, 6)
  return f ? `${grouped}.${f}` : grouped
}

// --- the wallet --------------------------------------------------------------

const wallets = new Map<string, any>()
window.addEventListener('eip6963:announceProvider', (e: any) => {
  if (e.detail?.info?.uuid) wallets.set(e.detail.info.uuid, e.detail.provider)
})
window.dispatchEvent(new Event('eip6963:requestProvider'))

let provider: any = null
let me: Address | null = null

function pickProvider(): any {
  return [...wallets.values()][0] ?? (window as any).ethereum ?? null
}

async function onChain(p: any) {
  const hexId = '0x' + CHAIN_ID.toString(16)
  try {
    await p.request({method: 'wallet_switchEthereumChain', params: [{chainId: hexId}]})
  } catch (err: any) {
    const code = err?.code ?? err?.data?.originalError?.code
    if (code !== 4902 && code !== -32603) throw err
    await p.request({
      method: 'wallet_addEthereumChain',
      params: [{
        chainId: hexId,
        chainName: 'ZVM testnet (Zenon based rollup)',
        nativeCurrency: {name: 'Zenon', symbol: 'ZNN', decimals: 18},
        rpcUrls: [RPC],
        blockExplorerUrls: [EXPLORER],
      }],
    })
  }
}

// Disconnecting is remembered, so a reload does not quietly reconnect a
// wallet that still trusts the page. Only this page's own convenience lives
// here; losing it costs one extra click.
const DISCONNECTED = 'rain.disconnected'
const remember = (off: boolean) => {
  try {
    if (off) localStorage.setItem(DISCONNECTED, '1')
    else localStorage.removeItem(DISCONNECTED)
  } catch {}
}
const chosenOff = () => {
  try {
    return localStorage.getItem(DISCONNECTED) === '1'
  } catch {
    return false
  }
}

const listening = new WeakSet<object>()

async function connect(): Promise<Address> {
  if (me && provider) return me
  provider = pickProvider()
  if (!provider) {
    // No wallet in this browser: on a phone that is usually Telegram's own.
    const here = location.href.replace(/^https?:\/\//, '')
    throw new Error(`No wallet in this browser. Open this page in MetaMask's app: https://metamask.app.link/dapp/${here}`)
  }
  const accounts: string[] = await provider.request({method: 'eth_requestAccounts'})
  if (!accounts?.length) throw new Error('The wallet shared no account.')
  me = getAddress(accounts[0])
  remember(false)
  await onChain(provider)
  if (!listening.has(provider)) {
    listening.add(provider)
    provider.on?.('accountsChanged', (a: string[]) => {
      // An empty list is the wallet disconnecting from its own side.
      me = a?.length ? getAddress(a[0]) : null
      if (!me) provider = null
      void refreshAll()
    })
  }
  await refreshAll()
  return me
}

// disconnect asks the wallet to forget this page too, where it can
// (MetaMask and others take wallet_revokePermissions). A wallet that cannot
// is still disconnected here, and stays so until Connect is pressed again.
async function disconnect() {
  const p = provider ?? pickProvider()
  me = null
  provider = null
  remember(true)
  try {
    await p?.request({method: 'wallet_revokePermissions', params: [{eth_accounts: {}}]})
  } catch {}
  await refreshAll()
}

// --- sending -----------------------------------------------------------------

interface Call {
  label: string
  to: Address
  data: Hex
  value?: bigint
}

const stepsCard = document.getElementById('steps-card')!
const stepsList = document.getElementById('steps')!

function stepRow(label: string) {
  stepsCard.hidden = false
  const li = document.createElement('li')
  const name = document.createElement('span')
  name.textContent = label
  const state = document.createElement('span')
  state.className = 'state'
  li.append(name, state)
  stepsList.prepend(li)
  return (text: string, kind: '' | 'busy' | 'done' | 'failed', hash?: Hex) => {
    state.className = `state ${kind}`
    state.textContent = ''
    if (hash) {
      const a = document.createElement('a')
      a.href = `${EXPLORER}/tx/${hash}`
      a.textContent = `${text} ${hash.slice(0, 10)}…`
      state.append(a)
    } else state.textContent = text
  }
}

// run sends calls one after another, each after the last has landed, since the
// wallet estimates each against the chain as it then is.
async function run(calls: Call[]): Promise<boolean> {
  const from = await connect()
  await onChain(provider)
  const signer = createWalletClient({account: from, chain: zvm, transport: custom(provider)})
  for (const c of calls) {
    const set = stepRow(c.label)
    try {
      set('waiting for your wallet', 'busy')
      const gas = ((await reader.estimateGas({account: from, to: c.to, data: c.data, value: c.value ?? 0n})) * 13n) / 10n
      const block = await reader.getBlock()
      const maxFeePerGas = (block.baseFeePerGas ?? 0n) * 2n + TIP
      const hash = await signer.sendTransaction({to: c.to, data: c.data, value: c.value ?? 0n, gas, maxFeePerGas, maxPriorityFeePerGas: TIP})
      set('landing', 'busy', hash)
      const r = await reader.waitForTransactionReceipt({hash, pollingInterval: 2_000, timeout: 600_000})
      if (r.status !== 'success') throw new Error('reverted')
      set('done', 'done', hash)
    } catch (err: any) {
      set(err?.shortMessage ?? err?.details ?? err?.message ?? String(err), 'failed')
      return false
    }
  }
  await refreshAll()
  return true
}

// --- the page ----------------------------------------------------------------

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T
const params = new URLSearchParams(location.search)
const bot = __RAIN_BOT__
for (const a of document.querySelectorAll<HTMLAnchorElement>('[data-bot-link]')) {
  if (bot) a.href = `https://t.me/${bot}`
  else a.removeAttribute('href')
}

function say(el: HTMLElement, text: string, bad = false) {
  el.textContent = text
  el.style.color = bad ? 'var(--danger)' : ''
}

$('connect').addEventListener('click', () => {
  if (me) return
  connect().catch((e) => say($('wallet-note'), e.message, true))
})
for (const id of ['disconnect', 'disconnect-card']) {
  $(id).addEventListener('click', () => void disconnect())
}

// The token picker: ZNN, the named tokens, and any address.
const select = $<HTMLSelectElement>('token')
for (const [value, label] of [['znn', 'ZNN'], ...named.map((n) => [n.name.toLowerCase(), n.name]), ['custom', 'Other, by address']]) {
  const o = document.createElement('option')
  o.value = value
  o.textContent = label
  select.append(o)
}
const custom_ = $<HTMLInputElement>('custom')
const amount = $<HTMLInputElement>('amount')
const tokenKey = () => (select.value === 'custom' ? custom_.value.trim() : select.value)

async function current(): Promise<Token | null> {
  try {
    return await tokenFor(tokenKey())
  } catch {
    return null
  }
}

async function refreshWallet() {
  const pill = $('chain-state')
  const connected = me !== null
  $('disconnect').hidden = !connected
  $('disconnect-card').hidden = !connected
  $('connect').classList.toggle('is-connected', connected)
  if (!me) {
    $('me').textContent = '—'
    $('linked').textContent = '—'
    $('linked').style.color = ''
    pill.textContent = 'not connected'
    pill.className = 'pill'
    $('connect').textContent = 'Connect wallet'
    showLinked(false)
    return
  }
  $('me').innerHTML = `<a class="mono" href="${EXPLORER}/address/${me}">${me.slice(0, 8)}…${me.slice(-6)}</a>`
  pill.textContent = 'chain 69'
  pill.className = 'pill ok'
  $('connect').textContent = `${me.slice(0, 6)}…${me.slice(-4)}`
  const linked = await reader.readContract({address: RAIN, abi: rainAbi, functionName: 'linked', args: [me]})
  $('linked').textContent = linked ? 'yes' : 'not yet'
  $('linked').style.color = linked ? 'var(--znn-ink)' : 'var(--dim)'
  showLinked(linked)
}

// showLinked swaps the Link form for Unlink once the wallet is linked. A
// secret that came in the URL keeps the form too: the bot learns a new link
// only from a new link transaction, as when relinking after /unlink or tying
// this wallet to another Telegram account.
function showLinked(linked: boolean) {
  $('unlink-box').hidden = !linked
  $('link-box').hidden = linked && !params.get('link')
}

async function refreshToken() {
  const t = await current()
  const rows = $('token-rows')
  const go = $<HTMLButtonElement>('fund-go')
  const note = $('fund-note')
  if (!t) {
    rows.hidden = true
    go.textContent = 'Approve'
    return
  }
  const amt = amount.value.trim()
  const revoking = amt === '0'
  go.textContent = revoking ? `Revoke ${t.name}` : `Approve${amt ? ` ${amt}` : ''} ${t.name}`
  if (!me) {
    rows.hidden = true
    say(note, 'Connect a wallet to see what you hold and what Rain may spend.')
    return
  }
  // For ZNN, held is WZNN (what the approval covers) and coin is the plain
  // ZNN in the wallet, which is what the person thinks of as their balance.
  const [held, approved, coin] = await Promise.all([
    reader.readContract({address: t.address, abi: erc20, functionName: 'balanceOf', args: [me]}),
    reader.readContract({address: t.address, abi: erc20, functionName: 'allowance', args: [me, RAIN]}),
    t.native ? reader.getBalance({address: me}) : Promise.resolve(0n),
  ])
  rows.hidden = false
  $('held').textContent = !t.native
    ? `${fmt(held, t.decimals)} ${t.name}`
    : held > 0n
      ? `${fmt(coin, 18)} ZNN, and ${fmt(held, 18)} already wrapped`
      : `${fmt(coin, 18)} ZNN`
  $('approved').textContent = approved >= maxUint256 / 2n ? `unlimited ${t.name}` : `${fmt(approved, t.decimals)} ${t.name}`
  let want = 0n
  try {
    want = amt ? parseUnits(amt.replace(/,/g, ''), t.decimals) : 0n
  } catch {}
  if (t.native && want > held + coin) {
    say(note, `You have ${fmt(held + coin, 18)} ZNN in all, less than ${fmt(want, 18)}.`, true)
  } else if (t.native && want > held) {
    say(note, `${fmt(want - held, 18)} of your ZNN is wrapped first, so this is two signatures: wrap, then approve.`)
  } else say(note, '')
}

async function refreshApprovals() {
  const list = $('approvals')
  if (!me) {
    list.innerHTML = '<div><dt class="note">Connect a wallet to see what Rain may spend.</dt></div>'
    return
  }
  const tokens = [await tokenFor('znn'), ...(await Promise.all(named.filter((n) => n.address !== WZNN).map((n) => tokenFor(n.name))))]
  const rows = await Promise.all(tokens.map(async (t) => [t, await reader.readContract({address: t.address, abi: erc20, functionName: 'allowance', args: [me!, RAIN]})] as const))
  list.replaceChildren()
  for (const [t, v] of rows) {
    const row = document.createElement('div')
    row.className = 'approval-row'
    const dt = document.createElement('dt')
    dt.textContent = t.name
    const dd = document.createElement('dd')
    const val = document.createElement('span')
    val.textContent = v === 0n ? 'none' : v >= maxUint256 / 2n ? 'unlimited' : fmt(v, t.decimals)
    dd.append(val)
    if (v > 0n) {
      const b = document.createElement('button')
      b.className = 'btn btn-small'
      b.type = 'button'
      b.textContent = 'Revoke'
      b.onclick = () => void run([{label: `Revoke ${t.name}`, to: t.address, data: encodeFunctionData({abi: erc20, functionName: 'approve', args: [RAIN, 0n]})}])
      dd.append(b)
    }
    row.append(dt, dd)
    list.append(row)
  }
}

async function refreshAll() {
  await Promise.allSettled([refreshWallet(), refreshToken(), refreshApprovals()])
}

select.addEventListener('change', () => {
  $('custom-field').hidden = select.value !== 'custom'
  void refreshToken()
})
custom_.addEventListener('change', () => void refreshToken())
amount.addEventListener('input', () => void refreshToken())
$('refresh').addEventListener('click', () => void refreshAll())

$('link-form').addEventListener('submit', (e) => {
  e.preventDefault()
  const s = $<HTMLInputElement>('secret').value.trim()
  if (!/^\d+$/.test(s) || BigInt(s) === 0n || BigInt(s) >= 1n << 64n) {
    say($('wallet-note'), 'The secret is the number /link gave you.', true)
    return
  }
  void run([{label: 'Link this wallet', to: RAIN, data: encodeFunctionData({abi: rainAbi, functionName: 'link', args: [BigInt(s)]})}]).then((ok) => {
    if (ok) say($('wallet-note'), 'Linked. The bot will message you in a moment.')
  })
})

// Unlinking on the contract stops every rain to or from this wallet at once,
// whatever it has approved.
$('unlink').addEventListener('click', () => {
  void run([{label: 'Unlink this wallet', to: RAIN, data: encodeFunctionData({abi: rainAbi, functionName: 'unlink'})}]).then((ok) => {
    if (ok) say($('wallet-note'), 'Unlinked. No rain can pay this wallet or spend from it until you link again.')
  })
})

$('fund-form').addEventListener('submit', async (e) => {
  e.preventDefault()
  const note = $('fund-note')
  try {
    const t = await tokenFor(tokenKey())
    const raw = amount.value.trim().replace(/,/g, '')
    if (!/^\d*\.?\d+$/.test(raw)) throw new Error('Type an amount.')
    const want = parseUnits(raw, t.decimals)
    const from = await connect()
    const calls: Call[] = []
    if (t.native && want > 0n) {
      const held = await reader.readContract({address: WZNN, abi: erc20, functionName: 'balanceOf', args: [from]})
      if (want > held) calls.push({label: `Wrap ${fmt(want - held, 18)} ZNN`, to: WZNN, data: encodeFunctionData({abi: erc20, functionName: 'deposit'}), value: want - held})
    }
    calls.push({
      label: want === 0n ? `Revoke ${t.name}` : `Approve ${fmt(want, t.decimals)} ${t.name}`,
      to: t.address,
      data: encodeFunctionData({abi: erc20, functionName: 'approve', args: [RAIN, want]}),
    })
    if (await run(calls)) say(note, want === 0n ? `Rain can spend none of your ${t.name}.` : `Rain may spend up to ${fmt(want, t.decimals)} ${t.name}. Now /rain in a group.`)
  } catch (err: any) {
    say(note, err?.message ?? String(err), true)
  }
})

// What the bot filled in.
function prefill() {
  const link = params.get('link')
  if (link) {
    $<HTMLInputElement>('secret').value = link
    $('link-card').classList.add('flash')
    $('link-card').scrollIntoView({block: 'center'})
  }
  const tok = params.get('token')
  if (tok) {
    const hit = [...select.options].find((o) => o.value === tok.toLowerCase())
    if (hit) select.value = hit.value
    else {
      select.value = 'custom'
      $('custom-field').hidden = false
      custom_.value = tok
    }
    amount.value = params.get('revoke') ? '0' : params.get('amount') ?? ''
    $('fund-card').classList.add('flash')
    if (!link) $('fund-card').scrollIntoView({block: 'center'})
  }
}
prefill()
void refreshToken()

// The addresses, from the deployment record the bot uses.
const addrs: [string, string][] = [
  ['Rain contract', RAIN],
  ['WZNN, which ZNN is approved as', WZNN],
  ['Bot, the contract’s operator', getAddress(record.bot)],
]
for (const [label, a] of addrs) {
  const row = document.createElement('div')
  const dt = document.createElement('dt')
  dt.textContent = label
  const dd = document.createElement('dd')
  const link = document.createElement('a')
  link.href = `${EXPLORER}/address/${a}?tab=contract`
  link.className = 'mono'
  link.textContent = a
  dd.append(link)
  row.append(dt, dd)
  $('addr').append(row)
}

// A wallet that already trusts this page connects without a prompt, unless
// it was disconnected here.
if (!chosenOff()) {
  pickProvider()
    ?.request({method: 'eth_accounts'})
    .then((a: string[]) => (a?.length ? connect() : null))
    .catch(() => {})
}
