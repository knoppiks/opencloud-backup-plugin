// The confirmation gate: proof that a newly shown Recovery Key was saved.
//
// Shared by setup (wizard/machine.ts) and replacement (recoverykey/rotation.ts),
// which both show a key once and must not send anything until the person has
// typed part of it back (8d decision 4). Two random groups out of seven: one
// could be read off the screen before it is hidden, the whole key is friction a
// family audience would work around.

//
// Groups are counted after the `ocbk1-` prefix: group 1 is the first block of
// five after it (#55). The saved key has eight dash-separated parts, so the
// form says "after ocbk1-" and draws where the asked group sits (gateHint)
// rather than relying on a number alone.

import {
  normalizeRecoveryKeyInput,
  recoveryKeyGroups,
  RK_GROUP_COUNT,
  RK_GROUP_SIZE,
  RK_PREFIX,
  RK_SYMBOL_COUNT
} from '../crypto'

/** GATE_GROUPS is how many groups of the key the confirmation gate asks for. */
export const GATE_GROUPS = 2

/** GateHintPart is one dash-separated part of the key's shape. */
export interface GateHintPart {
  text: string
  /** asked marks the group the person is asked to type. */
  asked: boolean
}

const HIDDEN_SYMBOL = '•'
const ASKED_SYMBOL = '_'

/**
 * gateHint draws the shape of a saved key with one group marked, so "which
 * group" reads the same however a person counts: the prefix as text, every
 * other group as dots, the asked one as blanks. It carries no key material.
 */
export function gateHint(group: number): GateHintPart[] {
  const groups = Array.from({ length: RK_GROUP_COUNT }, (_, i) => {
    const length = Math.min(RK_GROUP_SIZE, RK_SYMBOL_COUNT - i * RK_GROUP_SIZE)
    const asked = i === group
    return { text: (asked ? ASKED_SYMBOL : HIDDEN_SYMBOL).repeat(length), asked }
  })
  return [{ text: RK_PREFIX, asked: false }, ...groups]
}

/**
 * pickGateGroups chooses which groups the gate asks for: GATE_GROUPS distinct
 * indices out of seven, ascending so the form reads in key order.
 */
export function pickGateGroups(randomInt: (bound: number) => number): number[] {
  const pool = Array.from({ length: RK_GROUP_COUNT }, (_, i) => i)
  const picked: number[] = []
  while (picked.length < GATE_GROUPS) {
    const [index] = pool.splice(randomInt(pool.length), 1)
    picked.push(index as number)
  }
  return picked.sort((a, b) => a - b)
}

/**
 * gateMatches reports whether every answer matches its group, with the same
 * tolerance decodeRecoveryKey gives a whole key.
 */
export function gateMatches(recoveryKey: string, groups: number[], answers: string[]): boolean {
  const expected = recoveryKeyGroups(recoveryKey)
  if (answers.length !== groups.length) {
    return false
  }
  return groups.every((group, i) => normalizeRecoveryKeyInput(answers[i] ?? '') === expected[group])
}

/** cryptoRandomInt is the production randomInt. */
export function cryptoRandomInt(bound: number): number {
  const [value] = globalThis.crypto.getRandomValues(new Uint32Array(1))
  return (value as number) % bound
}

/**
 * nextFrame lets the page paint "this takes a moment" before Argon2id holds
 * the main thread (8d.2 decision 4): it resolves after the browser has had a
 * chance to render.
 */
export function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    if (typeof globalThis.requestAnimationFrame === 'function') {
      globalThis.requestAnimationFrame(() => setTimeout(resolve, 0))
    } else {
      setTimeout(resolve, 0)
    }
  })
}
