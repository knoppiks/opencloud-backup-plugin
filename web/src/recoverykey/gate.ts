// The confirmation gate: proof that a newly shown Recovery Key was saved.
//
// Shared by setup (wizard/machine.ts) and replacement (recoverykey/rotation.ts),
// which both show a key once and must not send anything until the person has
// typed part of it back (8d decision 4). Two random groups out of seven: one
// could be read off the screen before it is hidden, the whole key is friction a
// family audience would work around.

import { normalizeRecoveryKeyInput, recoveryKeyGroups, RK_GROUP_COUNT } from '../crypto'

/** GATE_GROUPS is how many groups of the key the confirmation gate asks for. */
export const GATE_GROUPS = 2

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
