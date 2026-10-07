// The admin API client: target management and grants.
//
// Kept apart from BackupApi so these routes load with the admin view and not
// with every user's overview. The server's admin middleware decides who may
// call them; a 403 here means "not an administrator", and nothing in this
// client tries to guess otherwise.
//
// Request bodies may carry S3 secrets. The transport never logs a body or puts
// one into an error, and neither does anything here.

import { ApiTransport, type BackupApiOptions } from './transport'
import type { AdminTarget, AdminTargetRequest, CheckResult, Grant, OpenCloudVersion } from './types'

/** AdminApi is the typed surface of `/admin/targets`. */
export class AdminApi {
  private readonly transport: ApiTransport

  constructor(options: BackupApiOptions) {
    this.transport = new ApiTransport(options)
  }

  /** listTargets returns every target, sorted by name by the server. */
  async listTargets(): Promise<AdminTarget[]> {
    const body = await this.transport.request<{ targets?: AdminTarget[] }>('/admin/targets')
    return body.targets ?? []
  }

  /** target reads one target. Every missing id answers the same 404. */
  target(id: string): Promise<AdminTarget> {
    return this.transport.request<AdminTarget>(this.path(id))
  }

  /** createTarget stores a new target. Credentials are required. */
  createTarget(body: AdminTargetRequest): Promise<AdminTarget> {
    return this.transport.request<AdminTarget>('/admin/targets', { method: 'POST', body })
  }

  /**
   * updateTarget replaces a target's settings. Without `credentials` the
   * stored ones stay; with them, both pairs are replaced.
   */
  updateTarget(id: string, body: AdminTargetRequest): Promise<AdminTarget> {
    return this.transport.request<AdminTarget>(this.path(id), { method: 'PUT', body })
  }

  /**
   * deleteTarget removes a target and its grants. Answers 409
   * `target_in_use` while Spaces still back up to it.
   */
  deleteTarget(id: string): Promise<void> {
    return this.transport.request<void>(this.path(id), {
      method: 'DELETE',
      expectNoContent: true
    })
  }

  /**
   * checkTarget tries the submitted settings and key pairs against the bucket.
   * Stateless: it never uses stored credentials, so an existing target is
   * checked by typing its keys again.
   */
  async checkTarget(body: AdminTargetRequest): Promise<CheckResult[]> {
    const response = await this.transport.request<{ results?: CheckResult[] }>(
      '/admin/targets/check',
      { method: 'POST', body }
    )
    return response.results ?? []
  }

  /** grants returns a target's audience. */
  async grants(id: string): Promise<Grant[]> {
    const body = await this.transport.request<{ grants?: Grant[] }>(this.path(id, '/grants'))
    return body.grants ?? []
  }

  /**
   * replaceGrants sets a target's whole audience and returns what was stored.
   * Anything left out is revoked; an empty list revokes it from everyone.
   */
  async replaceGrants(id: string, grants: Grant[]): Promise<Grant[]> {
    const body = await this.transport.request<{ grants?: Grant[] }>(this.path(id, '/grants'), {
      method: 'PUT',
      body: { grants }
    })
    return body.grants ?? []
  }

  /**
   * openCloudVersion reports the OpenCloud the service runs against and
   * whether this release was tested with it. The route is open to every
   * signed-in user; only the admin view asks for now, so it lives here
   * rather than in every user's bundle.
   */
  async openCloudVersion(): Promise<OpenCloudVersion> {
    const body = await this.transport.request<{ opencloud: OpenCloudVersion }>('/version')
    return body.opencloud
  }

  private path(id: string, suffix = ''): string {
    return `/admin/targets/${encodeURIComponent(id)}${suffix}`
  }
}
