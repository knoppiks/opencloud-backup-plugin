// The service's version, asked once per session (page load): it does not
// change while the page is open, and every page that shows the comparison
// shares the one answer.

import { ref, type Ref } from 'vue'
import type { ServiceVersion } from '../api'

/** VersionSource is the one API call this needs. */
export interface VersionSource {
  serviceVersion(): Promise<ServiceVersion>
}

let pending: Promise<ServiceVersion | undefined> | undefined

/**
 * loadServiceVersion asks the service the first time and returns that answer
 * ever after. The comparison is advisory: a failed request answers undefined
 * (and is not retried), it never becomes an error on the page.
 */
export function loadServiceVersion(source: VersionSource): Promise<ServiceVersion | undefined> {
  pending ??= source.serviceVersion().catch(() => undefined)
  return pending
}

/** useServiceVersion is loadServiceVersion as a ref, undefined until answered. */
export function useServiceVersion(source: VersionSource): Ref<ServiceVersion | undefined> {
  const version = ref<ServiceVersion | undefined>(undefined)
  void loadServiceVersion(source).then((v) => {
    version.value = v
  })
  return version
}

/** forgetServiceVersion starts a new "session", for tests. */
export function forgetServiceVersion(): void {
  pending = undefined
}
