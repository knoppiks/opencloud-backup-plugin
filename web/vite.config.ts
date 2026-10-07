// Build configuration for the Backup Vault extension.
//
// Everything that matters here is inside `defineConfig` from OpenCloud's
// extension SDK: it wires Module Federation with the host's shared singletons
// (vue, pinia, @opencloud-eu/web-pkg, …), emits `dist/manifest.json`, and sets
// the Vitest defaults. The `name` is the federation remote name, the entry
// chunk name, and the app id the host mounts our routes under, all at once.
//
// Built with extension-sdk 8.1.0. The host provides the shared packages and
// every `oc-*` component at runtime (`import: false` singletons), so one
// bundle serves every OpenCloud in the support window — 7.2 hosts included.
// That is a claim CI checks, not an assumption: the browser E2E runs on every
// leg of pkg/ocversion/versions.yaml. The SDK version here governs types, the
// build plugin and the unit tests only.
import { defineConfig } from '@opencloud-eu/extension-sdk'

export default defineConfig({
  name: 'backup-vault'
})
