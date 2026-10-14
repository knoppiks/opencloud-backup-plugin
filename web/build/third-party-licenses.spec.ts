import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  type BundledPackage,
  collectPackages,
  exceptionFor,
  isHostProvided,
  isLicenseAllowed,
  licenseProblems,
  npmTarballURL,
  originsOf,
  packageDirOf,
  readPackage,
  renderNotices
} from './third-party-licenses'

const ROOT = '/repo/web'
const PNPM = `${ROOT}/node_modules/.pnpm`
const SDK_DIR = `${PNPM}/@opencloud-eu+extension-sdk@8.1.0/node_modules/@opencloud-eu/extension-sdk`

function pkg(overrides: Partial<BundledPackage>): BundledPackage {
  return {
    name: 'some-package',
    version: '1.0.0',
    license: 'MIT',
    modules: ['index.js'],
    files: { LICENSE: 'MIT License text' },
    ...overrides
  }
}

describe('packageDirOf', () => {
  it('finds an unscoped package under pnpm', () => {
    expect(packageDirOf(`${PNPM}/hash-wasm@4.12.0/node_modules/hash-wasm/dist/index.esm.js`)).toBe(
      `${PNPM}/hash-wasm@4.12.0/node_modules/hash-wasm`
    )
  })

  it('finds a scoped package', () => {
    expect(packageDirOf(`${PNPM}/@noble+hashes@2.4.0/node_modules/@noble/hashes/sha2.js`)).toBe(
      `${PNPM}/@noble+hashes@2.4.0/node_modules/@noble/hashes`
    )
  })

  it('is undefined outside node_modules and for a bare package directory', () => {
    expect(packageDirOf(`${ROOT}/src/index.ts`)).toBeUndefined()
    expect(packageDirOf(`${ROOT}/node_modules/@noble`)).toBeUndefined()
  })
})

describe('originsOf', () => {
  it('treats our sources as our own', () => {
    expect(originsOf(`${ROOT}/src/views/Overview.vue?vue&type=script`, ROOT)).toEqual([
      { kind: 'own' }
    ])
  })

  it('traces a package module to its directory and file', () => {
    expect(
      originsOf(`${PNPM}/@noble+hashes@2.4.0/node_modules/@noble/hashes/sha2.js`, ROOT)
    ).toEqual([
      {
        kind: 'package',
        dir: `${PNPM}/@noble+hashes@2.4.0/node_modules/@noble/hashes`,
        module: 'sha2.js'
      }
    ])
  })

  it.each([
    ['virtual:mf-exposes:backup-vault__remoteEntry-_hash__mjs', '@module-federation/vite'],
    [
      `${ROOT}/node_modules/__mf__virtual/x__loadShare__vue__loadShare__.mjs`,
      '@module-federation/vite'
    ],
    [`${ROOT}/.__mf__temp/backup_mf_2_vault/localSharedImportMap.js`, '@module-federation/vite'],
    ['\0vite/preload-helper.js', 'vite']
  ])('attributes generated module %j to %s', (id, name) => {
    const [origin] = originsOf(id, ROOT)
    expect(origin).toMatchObject({ kind: 'generated', attribution: { name } })
  })

  it('attributes the SDK stylesheet to the SDK and to Tailwind, which compiles it', () => {
    expect(originsOf(`${SDK_DIR}/dist/tailwind.css`, ROOT)).toEqual([
      { kind: 'package', dir: SDK_DIR, module: 'dist/tailwind.css' },
      {
        kind: 'generated',
        attribution: {
          name: 'tailwindcss',
          via: ['@opencloud-eu/extension-sdk', '@tailwindcss/vite']
        }
      }
    ])
  })

  it('does not trace a module from elsewhere or an unknown virtual module', () => {
    expect(originsOf('/elsewhere/file.js', ROOT)).toEqual([{ kind: 'unknown' }])
    expect(originsOf('\0some-plugin/helper.js', ROOT)).toEqual([{ kind: 'unknown' }])
  })
})

describe('isHostProvided', () => {
  it.each(['vue', '@vue/runtime-core', 'pinia', 'luxon', 'vue3-gettext', 'yjs'])(
    '%s is provided by the host',
    (name) => expect(isHostProvided(name)).toBe(true)
  )

  it.each(['@opencloud-eu/web-pkg', '@opencloud-eu/web-client'])(
    '%s is provided by the host',
    (name) => expect(isHostProvided(name)).toBe(true)
  )

  it.each(['@opencloud-eu/extension-sdk', '@noble/hashes', 'vue-router', 'vuex'])(
    '%s is not',
    (name) => expect(isHostProvided(name)).toBe(false)
  )
})

describe('isLicenseAllowed', () => {
  it.each([
    ['MIT', true],
    ['Apache-2.0', true],
    ['CC0-1.0', true],
    ['(MIT OR GPL-3.0)', true],
    ['MIT AND GPL-3.0', false],
    ['GPL-3.0', false],
    ['AGPL-3.0', false],
    ['MPL-2.0', false],
    ['', false]
  ])('%j -> %s', (expression, allowed) => {
    expect(isLicenseAllowed(expression)).toBe(allowed)
  })
})

describe('licenseProblems', () => {
  it('accepts an allowed license with a license file', () => {
    expect(licenseProblems([pkg({})])).toEqual([])
  })

  it('refuses a license outside the allow-list', () => {
    expect(licenseProblems([pkg({ license: 'GPL-3.0' })])).toEqual([
      expect.stringContaining('license "GPL-3.0" is not allowed')
    ])
  })

  it('refuses a package without a license file', () => {
    expect(licenseProblems([pkg({ files: {} })])).toEqual([
      expect.stringContaining('no license file')
    ])
  })

  it('refuses a host-provided package whatever its license', () => {
    expect(licenseProblems([pkg({ name: 'vue' })])).toEqual([
      expect.stringContaining('provided by the OpenCloud host')
    ])
  })

  it("admits the SDK's stylesheet under its named exception", () => {
    const sdk = pkg({
      name: '@opencloud-eu/extension-sdk',
      license: 'AGPL-3.0',
      modules: ['dist/tailwind.css']
    })
    expect(exceptionFor(sdk)).toBeDefined()
    expect(licenseProblems([sdk])).toEqual([])
  })

  it('refuses any other part of the SDK', () => {
    const sdk = pkg({
      name: '@opencloud-eu/extension-sdk',
      license: 'AGPL-3.0',
      modules: ['dist/extension-sdk.js', 'dist/tailwind.css']
    })
    expect(exceptionFor(sdk)).toBeUndefined()
    expect(licenseProblems([sdk])).toEqual([
      expect.stringContaining('license "AGPL-3.0" is not allowed')
    ])
  })

  it('refuses the SDK when its license is no longer the one the exception names', () => {
    const sdk = pkg({
      name: '@opencloud-eu/extension-sdk',
      license: 'GPL-3.0',
      modules: ['dist/tailwind.css']
    })
    expect(licenseProblems([sdk])).toHaveLength(1)
  })
})

describe('npmTarballURL', () => {
  it('names scoped and unscoped tarballs', () => {
    expect(npmTarballURL('@opencloud-eu/extension-sdk', '8.1.0')).toBe(
      'https://registry.npmjs.org/@opencloud-eu/extension-sdk/-/extension-sdk-8.1.0.tgz'
    )
    expect(npmTarballURL('hash-wasm', '4.12.0')).toBe(
      'https://registry.npmjs.org/hash-wasm/-/hash-wasm-4.12.0.tgz'
    )
  })
})

describe('renderNotices', () => {
  it('lists packages by name with their license and notice files', () => {
    const text = renderNotices([
      pkg({ name: 'zeta', files: { LICENSE: 'zeta license' } }),
      pkg({ name: 'alpha', files: { NOTICE: 'alpha notice', LICENSE: 'alpha license' } })
    ])
    expect(text.indexOf('alpha 1.0.0 (MIT)')).toBeLessThan(text.indexOf('zeta 1.0.0 (MIT)'))
    expect(text).toContain('-- LICENSE --\n\nalpha license\n\n-- NOTICE --\n\nalpha notice')
  })

  it('says where an excepted package is published, and adds supplements', () => {
    const text = renderNotices([
      pkg({
        name: '@opencloud-eu/extension-sdk',
        version: '8.1.0',
        license: 'AGPL-3.0',
        modules: ['dist/tailwind.css'],
        repository: 'https://github.com/opencloud-eu/web'
      }),
      pkg({ name: 'hash-wasm', supplement: 'embedded C notes' })
    ])
    expect(text).toContain('GNU Affero')
    expect(text).toContain(npmTarballURL('@opencloud-eu/extension-sdk', '8.1.0'))
    expect(text).toContain('and in https://github.com/opencloud-eu/web')
    expect(text).toContain('embedded C notes')
  })
})

describe('collectPackages and readPackage', () => {
  let root: string

  // write PATH CONTENT under root, creating its directory.
  function write(path: string, content: string) {
    const full = join(root, path)
    mkdirSync(join(full, '..'), { recursive: true })
    writeFileSync(full, content)
  }

  beforeEach(() => {
    root = realpathSync(mkdtempSync(join(tmpdir(), 'third-party-licenses-')))
  })

  afterEach(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('reads the bundled packages, their license files and notices', () => {
    const dir = 'node_modules/.pnpm/a@1.2.3/node_modules/a'
    write(`${dir}/package.json`, JSON.stringify({ name: 'a', version: '1.2.3', license: 'MIT' }))
    write(`${dir}/LICENSE`, 'a license')
    write(`${dir}/NOTICE.txt`, 'a notice')
    write(`${dir}/README.md`, 'not a license')
    write(`${dir}/index.js`, '')
    write('node_modules/vite/package.json', JSON.stringify({ name: 'vite', version: '8.3.0' }))

    const { packages, problems } = collectPackages(
      [`${root}/${dir}/index.js`, `${root}/src/index.ts`, '\0vite/preload-helper.js'],
      root
    )
    expect(problems).toEqual([])
    expect([...packages.keys()].sort()).toEqual([`${root}/${dir}`, `${root}/node_modules/vite`])

    const a = readPackage(`${root}/${dir}`, [...(packages.get(`${root}/${dir}`) ?? [])], root)
    expect(a).toEqual({
      name: 'a',
      version: '1.2.3',
      license: 'MIT',
      modules: ['index.js'],
      files: { LICENSE: 'a license', 'NOTICE.txt': 'a notice' }
    })
  })

  it('reports a generator it cannot find and a module it cannot trace', () => {
    const { problems } = collectPackages(['\0vite/preload-helper.js', '/elsewhere/x.js'], root)
    expect(problems).toEqual([
      expect.stringContaining('vite not found'),
      expect.stringContaining('not traced')
    ])
  })

  it('reads a supplement for a package that has one', () => {
    const dir = 'node_modules/hash-wasm'
    write(`${dir}/package.json`, JSON.stringify({ name: 'hash-wasm', license: 'MIT' }))
    write(`${dir}/LICENSE`, 'hash-wasm license')
    write('notices/hash-wasm.txt', 'supplement')
    expect(readPackage(`${root}/${dir}`, [], `${root}/notices`).supplement).toBe('supplement')
  })
})
