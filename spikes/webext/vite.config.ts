import { defineConfig } from '@opencloud-eu/extension-sdk'

// The extension-sdk provides the canonical Vite config for OpenCloud Web
// apps/extensions: it externalizes the web runtime packages, emits an ESM
// bundle under dist/, and generates dist/manifest.json pointing at the
// entrypoint. `name` becomes the app id / output base name.
export default defineConfig({
  name: 'backup-spike'
})
