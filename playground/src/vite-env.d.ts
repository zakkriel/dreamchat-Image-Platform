/// <reference types="vite/client" />

// Dev-server-injected values. scripts/dev.sh rewrites playground/.env.local on
// every run; Vite inlines them at dev-server start. All optional: the
// playground must still run when it is started by hand with no script.
interface ImportMetaEnv {
  readonly VITE_API_TARGET?: string
  readonly VITE_DEV_TENANT_TOKEN?: string
  readonly VITE_DEV_ADMIN_TOKEN?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
