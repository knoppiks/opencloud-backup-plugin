// Keeps the catalogue honest without an extraction pipeline.
//
// There is no `.pot`/`.po` toolchain yet (8d adds one when there is enough text
// to justify it), so these two assertions stand in for it: a new English string
// with no German fails, and a German entry whose English original was reworded
// fails. Either one, left unnoticed, produces a UI that is silently half
// translated — which looks fine to whoever wrote it.

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { translations } from './translations'

const SRC = join(import.meta.dirname, '..')

/** sourceFiles lists the .ts and .vue files that can contain msgids. */
function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      return sourceFiles(full)
    }
    if (entry.endsWith('.spec.ts')) {
      return []
    }
    return entry.endsWith('.ts') || entry.endsWith('.vue') ? [full] : []
  })
}

/**
 * extractMsgids finds `$gettext('…')` arguments.
 *
 * Single and double quoted, across line breaks, because prettier wraps long
 * calls. Template literals are not matched and must not be used with $gettext:
 * an interpolated msgid cannot be translated at all.
 */
function extractMsgids(source: string): string[] {
  const ids: string[] = []
  const pattern = /\$gettext\(\s*(['"])((?:\\.|(?!\1)[\s\S])*?)\1/g
  for (const match of source.matchAll(pattern)) {
    // Collapse the whitespace prettier may have introduced inside a wrapped
    // string concatenation, and unescape what the source escaped.
    ids.push(match[2]!.replace(/\\'/g, "'").replace(/\\"/g, '"').replace(/\s+/g, ' ').trim())
  }
  return ids
}

const msgids = new Set(
  sourceFiles(SRC).flatMap((file) => extractMsgids(readFileSync(file, 'utf8')))
)

describe('translation catalogue', () => {
  it('finds the msgids at all', () => {
    // Guards the guard: a regex that matched nothing would make both
    // assertions below vacuously true.
    expect(msgids.size).toBeGreaterThan(10)
    expect(msgids).toContain('Backup Vault')
  })

  it('ships German for the languages it claims', () => {
    expect(Object.keys(translations)).toEqual(['de'])
  })

  it('translates every string the UI shows', () => {
    const de = translations.de ?? {}
    const untranslated = [...msgids].filter((id) => de[id] === undefined).sort()
    expect(untranslated).toEqual([])
  })

  it('has no entry whose English original has gone', () => {
    const orphaned = Object.keys(translations.de ?? {})
      .filter((id) => !msgids.has(id))
      .sort()
    expect(orphaned).toEqual([])
  })

  it('translates to something other than the msgid', () => {
    // A copy-pasted English value is the failure mode this catches: it passes
    // the completeness check above while translating nothing.
    const de = translations.de ?? {}
    const copied = Object.entries(de)
      .filter(([id, value]) => id === value)
      .map(([id]) => id)
    expect(copied).toEqual([])
  })
})
