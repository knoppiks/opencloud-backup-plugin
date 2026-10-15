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
// leg of internal/ocversion/versions.yaml. The SDK version here governs types, the
// build plugin and the unit tests only.
//
// The one plugin of our own audits the licenses of what the bundle contains
// and writes THIRD_PARTY_NOTICES beside manifest.json (build/, #99). The SDK
// merges `plugins` after its own.
import { defineConfig } from '@opencloud-eu/extension-sdk'
import { thirdPartyLicenses } from './build/third-party-licenses.ts'

// The bundle's version is the build stamp the Makefile and the image build
// pass in BACKUP_VAULT_VERSION (the same `git describe` the Go programs
// carry, #102); unset, it is "dev" (version/appversion.ts).
export default defineConfig({
  name: 'backup-vault',
  define: {
    __APP_VERSION__: JSON.stringify(process.env.BACKUP_VAULT_VERSION ?? '')
  },
  plugins: [thirdPartyLicenses()]
})
