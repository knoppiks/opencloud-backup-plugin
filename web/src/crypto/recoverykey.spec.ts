// Properties of the Recovery Key that the shared vectors cannot express,
// because they are about every key rather than a handful of fixed ones.
//
// The display shape matters more than it looks: the wizard lays the key out in
// a fixed grid, the confirmation gate asks for a specific group, and the
// decrypt CLI prompts for it. All three were written against documentation that
// said seven groups of five until a test finally measured it.
import { describe, expect, it } from 'vitest'
import { bytesToHex, equalBytes } from './bytes'
import {
  decodeRecoveryKey,
  encodeRecoveryKey,
  generateRecoveryKey,
  RK_ENTROPY_BYTES,
  RK_PREFIX
} from './recoverykey'

const GROUP_SIZES = [5, 5, 5, 5, 5, 5, 4]

describe('recovery key shape', () => {
  it('is a prefix and seven groups, the last one short', () => {
    const { display } = generateRecoveryKey()
    const groups = display.split('-')

    expect(groups[0]).toBe(RK_PREFIX)
    expect(groups.slice(1).map((group) => group.length)).toEqual(GROUP_SIZES)
    expect(display).toHaveLength(46)
  })

  it('carries 160 bits of entropy', () => {
    const { secret } = generateRecoveryKey()
    expect(secret).toHaveLength(RK_ENTROPY_BYTES)
  })

  it('uses only characters that survive handwriting', () => {
    const { display } = generateRecoveryKey()
    const payload = display.slice(RK_PREFIX.length).replaceAll('-', '')

    expect(payload).toMatch(/^[0-9ABCDEFGHJKMNPQRSTVWXYZ]+$/)
    // Crockford drops these four precisely because a human cannot tell them
    // apart from digits on paper.
    expect(payload).not.toMatch(/[ILOU]/)
  })

  it('is different every time', () => {
    const keys = new Set(Array.from({ length: 32 }, () => generateRecoveryKey().display))
    expect(keys.size).toBe(32)
  })
})

describe('recovery key round trip', () => {
  it('decodes what it encoded', () => {
    for (let i = 0; i < 32; i++) {
      const { display, secret } = generateRecoveryKey()
      expect(equalBytes(decodeRecoveryKey(display), secret)).toBe(true)
    }
  })

  it('accepts the shapes a user actually types', () => {
    const { display, secret } = generateRecoveryKey()
    const expected = bytesToHex(secret)

    const variants = {
      'as shown': display,
      lowercase: display.toLowerCase(),
      'no dashes': display.replaceAll('-', ''),
      'spaces instead of dashes': display.replaceAll('-', ' '),
      'pasted with surrounding whitespace': `  ${display}\n`,
      'without the prefix': display.slice(RK_PREFIX.length + 1),
      'wrapped over lines': display.replace('-', '-\n')
    }

    for (const [name, variant] of Object.entries(variants)) {
      expect(bytesToHex(decodeRecoveryKey(variant)), name).toBe(expected)
    }
  })

  it('reads a handwritten key back with the look-alikes mapped', () => {
    const { display, secret } = generateRecoveryKey()
    const [prefix, ...groups] = display.split('-')
    const handwritten = [
      prefix,
      ...groups.map((group) => group.replaceAll('0', 'O').replaceAll('1', 'I'))
    ].join('-')

    expect(equalBytes(decodeRecoveryKey(handwritten), secret)).toBe(true)
  })
})

describe('recovery key rejection', () => {
  it('refuses a key from a future format version', () => {
    const { display } = generateRecoveryKey()

    expect(() => decodeRecoveryKey(display.replace(RK_PREFIX, 'ocbk2'))).toThrowError(
      expect.objectContaining({ reason: 'version' })
    )
  })

  it('catches single-character typos, and never mistakes one for the real key', () => {
    // The checksum is eight bits, so roughly one typo in 256 slips through by
    // chance. That is what it is for — telling a user "mistyped" instead of
    // "wrong key" — and asserting it catches *every* typo would be a test that
    // fails once a fortnight for no reason. What must hold without exception is
    // the second half: a typo never decodes to the entropy that was typed
    // wrong, because that would open a Space with a key nobody holds.
    const alphabet = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
    let typos = 0
    let caught = 0

    for (let key = 0; key < 20; key++) {
      const { display, secret } = generateRecoveryKey()
      const index = RK_PREFIX.length + 3

      for (const replacement of alphabet) {
        if (display[index] === replacement) {
          continue
        }
        const typo = display.slice(0, index) + replacement + display.slice(index + 1)
        typos++

        let decoded: Uint8Array | undefined
        try {
          decoded = decodeRecoveryKey(typo)
        } catch (error) {
          expect((error as { reason: string }).reason).toBe('checksum')
          caught++
          continue
        }
        expect(equalBytes(decoded, secret)).toBe(false)
      }
    }

    expect(caught / typos).toBeGreaterThan(0.95)
  })

  it('never echoes the attempted key', () => {
    // A rejected key is still a key: it may be a user's real one, mistyped by a
    // character, and it must not reach a log or an error reporter.
    const { display } = generateRecoveryKey()
    const mistyped = display.slice(0, -1) + '$'

    try {
      decodeRecoveryKey(mistyped)
      expect.unreachable('expected a rejection')
    } catch (error) {
      const text = `${(error as Error).name}: ${(error as Error).message}`
      expect(text).not.toContain(mistyped)
      expect(text).not.toContain(display.slice(RK_PREFIX.length + 1, RK_PREFIX.length + 6))
    }
  })

  it('rejects entropy of the wrong length rather than padding it', () => {
    expect(() => encodeRecoveryKey(new Uint8Array(RK_ENTROPY_BYTES - 1))).toThrowError(
      expect.objectContaining({ reason: 'length' })
    )
  })
})
