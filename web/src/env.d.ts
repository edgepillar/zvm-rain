/// <reference types="vite/client" />

interface ImportMetaEnv {
  // A ZVM node's base URL, its JSON-RPC at /rpc (see .env.example).
  readonly VITE_ZVM_NODE: string
  // Where the explorer is.
  readonly VITE_GATEWAY: string
}

declare const __RAIN_TOKENS__: string
declare const __RAIN_BOT__: string
