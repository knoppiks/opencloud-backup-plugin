// HTTP from the test process to the fixture, trusting the fixture's own CA
// rather than turning verification off.

import { request as httpsRequest } from 'node:https'
import { request as httpRequest } from 'node:http'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { FIXTURE_DIR } from './env'

export interface HttpResponse {
  status: number
  body: Buffer
  text(): string
}

export interface HttpOptions {
  method?: string
  headers?: Record<string, string>
  body?: string | Buffer
  /** basicAuth is [user, password]. The fixture enables basic auth for seeding. */
  basicAuth?: [string, string]
  timeoutMs?: number
}

let ca: Buffer | undefined

function fixtureCa(): Buffer {
  ca ??= readFileSync(join(FIXTURE_DIR, 'ca.crt'))
  return ca
}

/** http sends one request and buffers the answer. It never throws on a status. */
export function http(url: string, options: HttpOptions = {}): Promise<HttpResponse> {
  const target = new URL(url)
  const headers: Record<string, string> = { ...options.headers }
  if (options.basicAuth) {
    const [user, password] = options.basicAuth
    headers.Authorization = `Basic ${Buffer.from(`${user}:${password}`).toString('base64')}`
  }
  const send = target.protocol === 'https:' ? httpsRequest : httpRequest
  return new Promise((resolve, reject) => {
    const req = send(
      target,
      {
        method: options.method ?? 'GET',
        headers,
        timeout: options.timeoutMs ?? 30_000,
        ...(target.protocol === 'https:' ? { ca: fixtureCa() } : {})
      },
      (res) => {
        const chunks: Buffer[] = []
        res.on('data', (chunk: Buffer) => chunks.push(chunk))
        res.on('end', () => {
          const body = Buffer.concat(chunks)
          resolve({ status: res.statusCode ?? 0, body, text: () => body.toString('utf8') })
        })
        res.on('error', reject)
      }
    )
    req.on('timeout', () => req.destroy(new Error(`timeout: ${options.method ?? 'GET'} ${url}`)))
    req.on('error', reject)
    req.end(options.body)
  })
}
