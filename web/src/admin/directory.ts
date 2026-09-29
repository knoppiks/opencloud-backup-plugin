// Finding OpenCloud users for a grant, and naming the ones already granted.
//
// A user grant stores the graph user `id`, which is also what the backend
// authorizes on (decisions.md, "Amendments before the first real
// deployment"). So the picker searches graph and stores what graph returns,
// and a stored grant is named by asking graph for that id (8e decision 8).
//
// The graph client is injected: composables/useUserDirectory.ts hands in the
// host's authenticated one, and tests hand in a fake. Only the two calls used
// here are described, structurally, so nothing binds to the rest of it.

/** DirectoryUser is what the admin view shows for a person. */
export interface DirectoryUser {
  id: string
  displayName: string
  mail?: string
}

/** UserDirectory is the admin view's whole use of the user directory. */
export interface UserDirectory {
  /** search finds users matching a typed name or address. */
  search(query: string): Promise<DirectoryUser[]>
  /**
   * lookup names one user. Undefined for a user that cannot be named — gone,
   * or not readable just now — so the grant still shows, by id, and can still
   * be removed.
   */
  lookup(id: string): Promise<DirectoryUser | undefined>
}

/** GraphUser is the slice of a graph user this module reads. */
interface GraphUser {
  id?: string
  displayName?: string
  mail?: string | null
}

type Selected = 'id' | 'displayName' | 'mail'

/** GraphUsersClient is the slice of web-client's `GraphUsers` this module calls. */
export interface GraphUsersClient {
  getUser(id: string, options?: { select?: Selected[] }): Promise<GraphUser>
  listUsers(options?: {
    search?: string
    orderBy?: 'displayName'[]
    select?: Selected[]
  }): Promise<GraphUser[]>
}

const SELECT: Selected[] = ['id', 'displayName', 'mail']

/** graphUserDirectory adapts a graph users client. */
export function graphUserDirectory(graph: GraphUsersClient): UserDirectory {
  return {
    async search(query) {
      const term = searchTerm(query)
      if (term === '') {
        return []
      }
      const users = await graph.listUsers({
        search: `"${term}"`,
        orderBy: ['displayName'],
        select: SELECT
      })
      return users.flatMap((user) => {
        const known = toDirectoryUser(user)
        return known ? [known] : []
      })
    },

    async lookup(id) {
      try {
        return toDirectoryUser(await graph.getUser(id, { select: SELECT }))
      } catch {
        // Why it failed is not something the admin can act on here, and the
        // error may carry request details. The grant shows by its id instead.
        return undefined
      }
    }
  }
}

/**
 * searchTerm is the query as graph's `$search` gets it: trimmed, and without
 * double quotes, which delimit the term and cannot be part of a name search.
 */
export function searchTerm(query: string): string {
  return query.replace(/"/g, '').trim()
}

function toDirectoryUser(user: GraphUser): DirectoryUser | undefined {
  if (!user.id) {
    return undefined
  }
  const result: DirectoryUser = { id: user.id, displayName: user.displayName || user.id }
  if (user.mail) {
    result.mail = user.mail
  }
  return result
}
