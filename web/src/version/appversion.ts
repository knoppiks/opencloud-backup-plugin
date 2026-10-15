// The bundle's own version, stamped at build time (vite.config.ts) from the
// same `git describe` as the service's binaries (#102). "dev" when nobody
// stamped it: `pnpm build` in a checkout, the unit tests, the E2E fixture.

export const DEV_VERSION = 'dev'

/** APP_VERSION is this bundle's version, or DEV_VERSION. */
export const APP_VERSION: string = __APP_VERSION__ || DEV_VERSION
