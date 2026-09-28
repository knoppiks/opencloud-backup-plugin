// What the UI offers to whom.
//
// These mirror the server's role table (pkg/api/access.go) so the UI does not
// show a button that can only fail. They decide presentation only: every route
// enforces its own minimum server-side, and a client that got this wrong would
// get a 403, not a capability.

import type { SpaceRole } from '../api'

const rank: Record<SpaceRole, number> = { viewer: 1, editor: 2, manager: 3, owner: 4 }

/**
 * roleRank orders a role. A word the server sends that this build does not
 * know ranks as viewer: offering too little is a missing button, offering too
 * much is a button that fails.
 */
function roleRank(role: string): number {
  return rank[role as SpaceRole] ?? rank.viewer
}

/** canOperate: start a backup and change configuration (editor and above). */
export function canOperate(role: string): boolean {
  return roleRank(role) >= rank.editor
}

/** canManageKeys: set up and replace keys (manager and above). */
export function canManageKeys(role: string): boolean {
  return roleRank(role) >= rank.manager
}
