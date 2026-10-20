import {defineConfig, loadEnv} from 'vite'

// The node and origins this page reads are never built in (see .env.example).
// A build without them would ship a page that talks to nothing, so it stops
// here and says what to set.
function requireEnv(env: Record<string, string>, names: string[]) {
  const missing = names.filter((n) => !env[n])
  if (missing.length) {
    throw new Error(`${missing.join(', ')} not set: copy .env.example to .env, or set them in the environment`)
  }
}

// Served at /rain/ on the devnet's gateway, which forwards the prefix to this
// project's own container. Settings come from zvm-rain/.env, the one settings
// file: the node and the explorer's origin (VITE_), and two of the bot's, the
// token names it knows (RAIN_TOKENS) and its username. Those two are compiled
// in by name, so the Telegram token beside them never reaches the bundle.
export default defineConfig(({mode}) => {
  requireEnv(loadEnv(mode, '..', 'VITE_'), ['VITE_ZVM_NODE', 'VITE_GATEWAY'])
  const env = {...loadEnv(mode, '..', 'RAIN_'), ...process.env}
  return {
    base: process.env.BASE_PATH ?? '/rain/',
    envDir: '..',
    define: {
      __RAIN_TOKENS__: JSON.stringify(env.RAIN_TOKENS ?? ''),
      __RAIN_BOT__: JSON.stringify(env.RAIN_BOT_USERNAME ?? ''),
    },
    server: {port: 47610, strictPort: true},
  }
})
