// Build configuration for the Backup Vault extension.
//
// Everything that matters here is inside `defineConfig` from OpenCloud's
// extension SDK: it wires Module Federation with the host's shared singletons
// (vue, pinia, @opencloud-eu/web-pkg, …), emits `dist/manifest.json`, and sets
// the Vitest defaults. The `name` is the federation remote name, the entry
// chunk name, and the app id the host mounts our routes under, all at once.
//
// Pinned against OpenCloud 7.3.0 / extension-sdk 7.2.0 (see
// .agents/plan/phase-0-findings.md). The host provides the shared packages at
// runtime, so their versions here must stay on the same major as the OpenCloud
// release we target.
import { defineConfig } from '@opencloud-eu/extension-sdk'

export default defineConfig({
  name: 'backup-vault'
})
