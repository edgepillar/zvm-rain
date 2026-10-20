// Rain's deployment record: deployments/<VITE_DEPLOYMENT, default zvm>.json,
// written by `zvm-rain deploy` and kept out of git, because every devnet
// deploys its own. Null when this build has none: boot.ts then shows that Rain
// is not deployed instead of starting the page.

export interface Deployment {
  chainId: number
  rain: string
  wznn: string
  bot: string
}

const records = import.meta.glob<Deployment>('../../deployments/*.json', {eager: true, import: 'default'})

export const record: Deployment | null = records[`../../deployments/${import.meta.env.VITE_DEPLOYMENT || 'zvm'}.json`] ?? null
