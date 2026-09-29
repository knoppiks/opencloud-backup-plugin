// A target's audience, as the admin view edits it.
//
// The server stores a list of grants and `PUT .../grants` replaces the whole
// list. The view offers two things: "everyone", or "only these people". Space
// grants exist too, but the view cannot edit them yet (8e decision 3). They
// are carried through untouched — as is any grant this build does not
// understand — because leaving them out of a save would revoke them, and that
// is an authorization change nobody chose.

import type { Grant } from '../api'

/** AudienceMode is the choice the view offers. */
export type AudienceMode = 'everyone' | 'people'

/** Audience is the editable shape of a grant list. */
export interface Audience {
  mode: AudienceMode
  /** userIds are OpenCloud (graph) user ids, in the order they were added. */
  userIds: string[]
  /** kept are the grants the view shows but does not edit, sent back as read. */
  kept: Grant[]
}

/** audienceFromGrants reads a stored grant list. */
export function audienceFromGrants(grants: Grant[]): Audience {
  const userIds: string[] = []
  const kept: Grant[] = []
  let everyone = false
  for (const grant of grants) {
    if (grant.scope === 'all_users') {
      everyone = true
    } else if (grant.scope === 'user' && grant.user_id) {
      if (!userIds.includes(grant.user_id)) {
        userIds.push(grant.user_id)
      }
    } else {
      kept.push(grant)
    }
  }
  return { mode: everyone ? 'everyone' : 'people', userIds, kept }
}

/**
 * grantsFromAudience builds the list to send.
 *
 * "Everyone" drops the listed people (8e decision 11): they would be
 * redundant, and a list the admin cannot see take effect is hidden state.
 */
export function grantsFromAudience(audience: Audience): Grant[] {
  const people: Grant[] =
    audience.mode === 'everyone'
      ? [{ scope: 'all_users' }]
      : audience.userIds.map((id) => ({ scope: 'user', user_id: id }))
  return [...people, ...audience.kept]
}

/** hasRedundantPeople reports people a save would drop because everyone is chosen. */
export function hasRedundantPeople(audience: Audience): boolean {
  return audience.mode === 'everyone' && audience.userIds.length > 0
}

/** reachesNobody reports an audience that grants the target to no one at all. */
export function reachesNobody(audience: Audience): boolean {
  return audience.mode === 'people' && audience.userIds.length === 0 && audience.kept.length === 0
}

export function withUser(audience: Audience, userId: string): Audience {
  if (audience.userIds.includes(userId)) {
    return audience
  }
  return { ...audience, userIds: [...audience.userIds, userId] }
}

export function withoutUser(audience: Audience, userId: string): Audience {
  return { ...audience, userIds: audience.userIds.filter((id) => id !== userId) }
}

export function withMode(audience: Audience, mode: AudienceMode): Audience {
  return { ...audience, mode }
}

/**
 * sameGrants reports whether two audiences would store the same thing, so a
 * Save button can say whether there is anything to save.
 */
export function sameGrants(a: Audience, b: Audience): boolean {
  return canonical(grantsFromAudience(a)) === canonical(grantsFromAudience(b))
}

function canonical(grants: Grant[]): string {
  return grants
    .map((g) => `${g.scope}\u0000${g.user_id ?? ''}\u0000${g.space_id ?? ''}`)
    .sort()
    .join('\u0001')
}
