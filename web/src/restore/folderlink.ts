// Where a restore's folder lives in the Files app.
//
// The server records a restore's folder as a path relative to the Space's
// root ("Restore/2026-09-25T03-00-00Z"). Linking to it needs the host's idea
// of that Space (its drive alias), which only the host's spaces store has. So
// the lookup and the route building are injected, and this module decides only
// two things:
//
//   - **Is this a folder we would link to at all?** The value comes from the
//     server, but it is rendered as a link inside someone's Files app, so a
//     path that leaves the restore area ("..", an absolute path, a backslash)
//     is refused rather than trusted. The service only ever writes
//     "Restore/<stamp>" (internal/restore.RestoreFolderName).
//   - **Is there a Space to link into?** When the host has not loaded the
//     Space, there is no honest link to build, and the caller shows the path
//     as text instead of inventing a URL.

/** RESTORE_ROOT is the top-level folder every restore is written below. */
export const RESTORE_ROOT = 'Restore'

/**
 * isRestoreFolder reports whether folder is a direct child of the restore
 * root: exactly "Restore/<name>", with a name that is not "." or "..".
 */
export function isRestoreFolder(folder: string): boolean {
  if (folder.includes('\\')) {
    return false
  }
  const segments = folder.split('/')
  if (segments.length !== 2 || segments[0] !== RESTORE_ROOT) {
    return false
  }
  const name = segments[1] ?? ''
  return name !== '' && name !== '.' && name !== '..'
}

/**
 * hostSpaceIds lists the ids the host may know a service Space id by, most
 * likely first.
 *
 * The service names a Space in CS3 form, `storage$space!opaque`. The host's
 * spaces store holds the graph drive id, which drops `!opaque` when it only
 * repeats the space part. Measured on OpenCloud 7.3.0 in 8f: a personal Space
 * is `…$9399…!9399…` to the service and `…$9399…` to the host, and an exact
 * lookup finds nothing.
 */
export function hostSpaceIds(serviceId: string): string[] {
  const bang = serviceId.lastIndexOf('!')
  const dollar = serviceId.indexOf('$')
  if (bang < 0 || dollar < 0 || bang < dollar) {
    return [serviceId]
  }
  const spacePart = serviceId.slice(dollar + 1, bang)
  const opaque = serviceId.slice(bang + 1)
  return opaque === spacePart ? [serviceId, serviceId.slice(0, bang)] : [serviceId]
}

/**
 * restoreFolderLocation builds the route to a restore folder, or undefined
 * when there is nothing trustworthy to link to.
 *
 * `findSpace` may return undefined (the host has not loaded that Space) or
 * throw; both mean "no link", never an error on the page.
 */
export function restoreFolderLocation<S, L>(
  folder: string,
  findSpace: () => S | undefined,
  build: (space: S, path: string) => L
): L | undefined {
  if (!isRestoreFolder(folder)) {
    return undefined
  }
  let space: S | undefined
  try {
    space = findSpace()
  } catch {
    return undefined
  }
  return space === undefined ? undefined : build(space, folder)
}
