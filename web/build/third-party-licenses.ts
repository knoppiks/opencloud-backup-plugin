/// <reference types="node" />
// Third-party licenses of the bundle, checked and reproduced at build time
// (phase-11 §11.1, #99).
//
// What is audited is what the bundle contains: every module the build put
// into a chunk, traced back to the package it came from. A dependency list
// (`pnpm licenses --prod`) is the wrong source. It misses the Module
// Federation runtime, which the extension SDK, a devDependency, bundles, and
// it says nothing about whether a host-provided package slipped in.
//
// The build fails when:
// - a host-provided package (Vue, OpenCloud's web-pkg, …) is in the bundle:
//   the host provides these at runtime, and a second copy is both a bug and
//   a license we would then be distributing;
// - a package's license is outside the allow-list and not a named exception,
//   or it has no license file;
// - a module cannot be traced to our sources or to a package.
//
// Otherwise it writes THIRD_PARTY_NOTICES next to manifest.json, so the
// notices travel with the bundle into OpenCloud's apps directory, and the
// list of bundled packages to BUNDLED_PACKAGES_FILE, outside the bundle, for
// the vulnerability audit (vulnerability-audit.ts).
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  writeFileSync
} from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'
import type { Plugin } from 'vite'

// Permissive licenses only. Anything else is a named exception below.
export const ALLOWED_LICENSES = [
  'Apache-2.0',
  'BSD-2-Clause',
  'BSD-3-Clause',
  'MIT',
  'ISC',
  'CC0-1.0'
] as const

// The modules OpenCloud's host shares with every extension (the extension
// SDK's externalModules, 8.1.0), plus Vue's own internals, which a bundled
// Vue would bring along.
const HOST_PROVIDED: readonly RegExp[] = [
  /^vue$/,
  /^@vue\//,
  /^luxon$/,
  /^pinia$/,
  /^vue3-gettext$/,
  /^@opencloud-eu\/web-pkg$/,
  /^@opencloud-eu\/web-client$/,
  /^yjs$/,
  /^y-protocols$/
]

export function isHostProvided(name: string): boolean {
  return HOST_PROVIDED.some((pattern) => pattern.test(name))
}

// A package outside the allow-list that the owner admitted, limited to the
// files of it the bundle may contain (decisions.md, phase 11.1 amendments).
export interface LicenseException {
  license: string
  modules: readonly string[]
  note: string
}

export const LICENSE_EXCEPTIONS: Readonly<Record<string, LicenseException>> = {
  // The SDK's stylesheet is the theme the `ext-*` utility classes are built
  // from; Tailwind compiles it into the bundle's CSS, so that part of the
  // bundle is AGPL-3.0 material. A standing exception: the bundle runs
  // inside OpenCloud Web (AGPL-3.0) anyway, and the theme keeps it in step
  // with the host's design tokens.
  '@opencloud-eu/extension-sdk': {
    license: 'AGPL-3.0',
    modules: ['dist/tailwind.css'],
    note:
      "The bundle's stylesheet is compiled by Tailwind CSS from this package's\n" +
      'dist/tailwind.css. That part of the bundle is covered by the GNU Affero\n' +
      'General Public License v3.0; the rest of the bundle by the Apache License,\n' +
      'Version 2.0, which is compatible with it.'
  }
}

// An attribution for code that is not read from a package but produced by
// one: a build plugin's generated modules, or a transformer's output. `via`
// is the chain of packages to resolve it through, when it is not a direct
// dependency of ours.
interface Attribution {
  name: string
  via: readonly string[]
}

interface AttributionRule {
  matches: (id: string) => boolean
  attribution: Attribution
}

// Modules a build plugin generates, attributed to the plugin's package.
const GENERATED: readonly AttributionRule[] = [
  {
    matches: (id) =>
      id.startsWith('virtual:mf-') ||
      id.includes('/node_modules/__mf__virtual/') ||
      id.includes('/.__mf__temp/'),
    attribution: { name: '@module-federation/vite', via: ['@opencloud-eu/extension-sdk'] }
  },
  { matches: (id) => id.startsWith('\0vite/'), attribution: { name: 'vite', via: [] } }
]

// Modules whose output carries another package's code besides their own.
const TRANSFORMED: readonly AttributionRule[] = [
  {
    // Tailwind writes its own preflight and utilities into the stylesheet.
    matches: (id) =>
      id.endsWith(['', '@opencloud-eu', 'extension-sdk', 'dist', 'tailwind.css'].join(sep)),
    attribution: { name: 'tailwindcss', via: ['@opencloud-eu/extension-sdk', '@tailwindcss/vite'] }
  }
]

// Supplements for packages whose own license file does not cover everything
// they ship. Read from build/notices/<file>.
const SUPPLEMENTS: Readonly<Record<string, string>> = {
  'hash-wasm': 'hash-wasm.txt'
}

export type ModuleOrigin =
  | { kind: 'own' }
  | { kind: 'package'; dir: string; module: string }
  | { kind: 'generated'; attribution: Attribution }
  | { kind: 'unknown' }

const NODE_MODULES = `${sep}node_modules${sep}`

// originsOf says where a module of the bundle came from: usually one origin,
// two for a module a transformer adds its own code to. ids are absolute
// paths (pnpm's real paths) or plugin-generated virtual ids.
export function originsOf(id: string, root: string): ModuleOrigin[] {
  const generated = GENERATED.find((rule) => rule.matches(id))
  if (generated) return [{ kind: 'generated', attribution: generated.attribution }]
  const path = id.replace(/[?#].*$/, '')
  const extra: ModuleOrigin[] = TRANSFORMED.filter((rule) => rule.matches(path)).map((rule) => ({
    kind: 'generated',
    attribution: rule.attribution
  }))
  const dir = packageDirOf(path)
  if (dir) return [{ kind: 'package', dir, module: relative(dir, path) }, ...extra]
  if (path.startsWith(root + sep)) return [{ kind: 'own' }, ...extra]
  return [{ kind: 'unknown' }]
}

// packageDirOf returns the package directory a file under node_modules
// belongs to: the last node_modules segment, then the (scoped) name.
export function packageDirOf(path: string): string | undefined {
  const at = path.lastIndexOf(NODE_MODULES)
  if (at < 0) return undefined
  const rest = path.slice(at + NODE_MODULES.length).split(sep)
  const nameParts = rest[0]?.startsWith('@') ? 2 : 1
  if (rest.length <= nameParts) return undefined
  return path.slice(0, at + NODE_MODULES.length) + rest.slice(0, nameParts).join(sep)
}

// findPackageDir resolves a package the way Node does, without its exports
// map: node_modules/<name> in `from` or the nearest ancestor that has one.
export function findPackageDir(name: string, from: string): string | undefined {
  for (let dir = from; ; dir = dirname(dir)) {
    const candidate = join(dir, 'node_modules', ...name.split('/'))
    if (existsSync(join(candidate, 'package.json'))) return realpathSync(candidate)
    if (dirname(dir) === dir) return undefined
  }
}

function resolveAttribution(attribution: Attribution, root: string): string | undefined {
  let from: string | undefined = root
  for (const step of attribution.via) {
    from = from && findPackageDir(step, from)
  }
  return from && findPackageDir(attribution.name, from)
}

export interface BundledPackage {
  name: string
  version: string
  license: string
  // The package's own files in the bundle, relative to its directory; empty
  // for a package whose code is generated or transformed into the bundle.
  modules: string[]
  // file name -> content: the package's license and notice files.
  files: Record<string, string>
  repository?: string
  supplement?: string
}

const LICENSE_FILE = /^(licen[sc]e|unlicen[sc]e|copying|notice)/i

export function readPackage(
  dir: string,
  modules: readonly string[],
  supplementsDir: string
): BundledPackage {
  const manifest = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')) as {
    name?: string
    version?: string
    license?: string | { type?: string }
    repository?: string | { url?: string }
  }
  const name = manifest.name ?? dir
  const license =
    typeof manifest.license === 'string' ? manifest.license : (manifest.license?.type ?? '')
  const files: Record<string, string> = {}
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isFile() && LICENSE_FILE.test(entry.name)) {
      files[entry.name] = readFileSync(join(dir, entry.name), 'utf8')
    }
  }
  const pkg: BundledPackage = {
    name,
    version: manifest.version ?? '',
    license,
    modules: [...modules].sort(),
    files
  }
  const repository =
    typeof manifest.repository === 'string' ? manifest.repository : manifest.repository?.url
  if (repository) pkg.repository = repository
  const supplementFile = SUPPLEMENTS[name]
  if (supplementFile) pkg.supplement = readFileSync(join(supplementsDir, supplementFile), 'utf8')
  return pkg
}

// isLicenseAllowed reads an SPDX expression: "A OR B" needs one allowed
// alternative, anything else (a single id, "A AND B") needs every id allowed.
export function isLicenseAllowed(expression: string): boolean {
  const allowed = (id: string) => (ALLOWED_LICENSES as readonly string[]).includes(id)
  const ids = expression
    .replace(/[()]/g, ' ')
    .split(/\s+(?:OR|AND)\s+|\s+/)
    .filter((id) => id !== '')
  if (ids.length === 0) return false
  if (/\sOR\s/.test(expression) && !/\sAND\s/.test(expression)) return ids.some(allowed)
  return ids.every(allowed)
}

// exceptionFor returns the named exception that admits pkg as bundled, if
// one does: same license, and nothing of the package beyond its listed files.
export function exceptionFor(pkg: BundledPackage): LicenseException | undefined {
  const exception = LICENSE_EXCEPTIONS[pkg.name]
  if (!exception || exception.license !== pkg.license) return undefined
  return pkg.modules.every((m) => exception.modules.includes(m)) ? exception : undefined
}

export function licenseProblems(packages: readonly BundledPackage[]): string[] {
  const problems: string[] = []
  for (const pkg of packages) {
    const label = `${pkg.name}@${pkg.version}`
    if (isHostProvided(pkg.name)) {
      problems.push(`${label} is provided by the OpenCloud host and must not be bundled`)
      continue
    }
    if (!isLicenseAllowed(pkg.license) && !exceptionFor(pkg)) {
      problems.push(
        `${label}: license "${pkg.license}" is not allowed (allowed: ${ALLOWED_LICENSES.join(', ')}` +
          `; bundled: ${pkg.modules.join(', ') || 'generated code'})`
      )
    }
    if (Object.keys(pkg.files).length === 0) {
      problems.push(`${label}: no license file to reproduce`)
    }
  }
  return problems
}

// npmTarballURL is where the registry serves a published version's files,
// immutably.
export function npmTarballURL(name: string, version: string): string {
  const base = name.startsWith('@') ? name.slice(name.indexOf('/') + 1) : name
  return `https://registry.npmjs.org/${name}/-/${base}-${version}.tgz`
}

const RULE = '-'.repeat(79)

export function renderNotices(packages: readonly BundledPackage[]): string {
  const sorted = [...packages].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  const parts = [
    'THIRD-PARTY NOTICES',
    '',
    'This bundle includes the software listed below. Each entry reproduces that',
    "software's license and, where it has one, its NOTICE file. The bundle's own",
    'code is licensed under the Apache License, Version 2.0.'
  ]
  for (const pkg of sorted) {
    parts.push('', RULE, `${pkg.name} ${pkg.version} (${pkg.license})`, RULE, '')
    const exception = exceptionFor(pkg)
    if (exception && !isLicenseAllowed(pkg.license)) {
      parts.push(exception.note, '', 'Its source code is available at')
      parts.push(npmTarballURL(pkg.name, pkg.version))
      if (pkg.repository) parts.push(`and in ${pkg.repository}`)
      parts.push('')
    }
    Object.keys(pkg.files)
      .sort()
      .forEach((file, i) => {
        if (i > 0) parts.push('')
        parts.push(`-- ${file} --`, '', (pkg.files[file] ?? '').trimEnd())
      })
    if (pkg.supplement) parts.push('', pkg.supplement.trimEnd())
  }
  return parts.join('\n') + '\n'
}

export const NOTICES_FILE = 'THIRD_PARTY_NOTICES'

// The bundle's packages, for the vulnerability audit (phase-11 §11.6, #116):
// written beside the build, outside the bundle, relative to the web root.
export const BUNDLED_PACKAGES_FILE = 'node_modules/.cache/backup-vault/bundled-packages.json'

export interface BundledPackageRef {
  name: string
  version: string
  // True when none of the package's own files are in the bundle, only code a
  // build tool of it generated (vite's preload helper, Tailwind's output).
  generatedOnly: boolean
}

export function bundledPackageRefs(packages: readonly BundledPackage[]): BundledPackageRef[] {
  return packages
    .map((pkg) => ({
      name: pkg.name,
      version: pkg.version,
      generatedOnly: pkg.modules.length === 0
    }))
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
}

// bundledModuleIds lists the modules of every chunk in the output.
function bundledModuleIds(bundle: Record<string, unknown>): Set<string> {
  const ids = new Set<string>()
  for (const output of Object.values(bundle)) {
    const chunk = output as { type?: string; moduleIds?: readonly string[] }
    if (chunk.type === 'chunk') chunk.moduleIds?.forEach((id) => ids.add(id))
  }
  return ids
}

// collectPackages maps the bundle's modules to the directories of the
// packages they came from, each with its files in the bundle; problems
// lists what it could not attribute.
export function collectPackages(
  ids: Iterable<string>,
  root: string
): { packages: Map<string, Set<string>>; problems: string[] } {
  const packages = new Map<string, Set<string>>()
  const problems: string[] = []
  const add = (dir: string, module?: string) => {
    const modules = packages.get(dir) ?? new Set<string>()
    if (module !== undefined) modules.add(module)
    packages.set(dir, modules)
  }
  for (const id of ids) {
    for (const origin of originsOf(id, root)) {
      switch (origin.kind) {
        case 'own':
          break
        case 'package':
          add(origin.dir, origin.module)
          break
        case 'generated': {
          const dir = resolveAttribution(origin.attribution, root)
          if (dir) add(dir)
          else problems.push(`${JSON.stringify(id)}: ${origin.attribution.name} not found`)
          break
        }
        case 'unknown':
          problems.push(`${JSON.stringify(id)}: not traced to our sources or a package`)
      }
    }
  }
  return { packages, problems }
}

export function thirdPartyLicenses(): Plugin {
  let root = ''
  return {
    name: 'third-party-licenses',
    apply: 'build',
    configResolved(config) {
      root = config.root
    },
    generateBundle(_options, bundle) {
      const { packages, problems } = collectPackages(bundledModuleIds(bundle), root)
      const supplementsDir = join(root, 'build', 'notices')
      const bundled = [...packages].map(([dir, modules]) =>
        readPackage(dir, [...modules], supplementsDir)
      )
      problems.push(...licenseProblems(bundled))
      if (problems.length > 0) {
        this.error(`third-party licenses:\n  ${problems.join('\n  ')}`)
      }
      this.emitFile({ type: 'asset', fileName: NOTICES_FILE, source: renderNotices(bundled) })
      const packagesFile = join(root, BUNDLED_PACKAGES_FILE)
      mkdirSync(dirname(packagesFile), { recursive: true })
      writeFileSync(packagesFile, JSON.stringify(bundledPackageRefs(bundled), null, 2) + '\n')
    }
  }
}
