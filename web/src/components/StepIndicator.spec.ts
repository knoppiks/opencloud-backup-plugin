import { describe, expect, it } from 'vitest'
import { mountWithHost } from '../test/host'
import StepIndicator from './StepIndicator.vue'

const STEPS = ['Destination', 'Recovery Key', 'Schedule']

function states(current: number): (string | undefined)[] {
  const wrapper = mountWithHost(StepIndicator, { props: { steps: STEPS, current } })
  return wrapper.findAll('li').map((li) => li.attributes('data-state'))
}

describe('StepIndicator', () => {
  it('shows every step, numbered, and marks the current one', () => {
    const wrapper = mountWithHost(StepIndicator, { props: { steps: STEPS, current: 1 } })
    const items = wrapper.findAll('li')
    expect(items).toHaveLength(3)
    expect(items[1]?.attributes('aria-current')).toBe('step')
    expect(items[1]?.text()).toContain('Recovery Key')
    expect(items[1]?.text()).toContain('(step 2 of 3)')
    expect(items[2]?.text()).toContain('3')
    expect(wrapper.findAll('[aria-current]')).toHaveLength(1)
  })

  it('ticks finished steps and says so in words', () => {
    const wrapper = mountWithHost(StepIndicator, { props: { steps: STEPS, current: 2 } })
    const first = wrapper.findAll('li')[0]
    expect(first?.find('[data-icon="check"]').exists()).toBe(true)
    expect(first?.text()).toContain('(done)')
  })

  it('places the steps before, at and after the current one', () => {
    expect(states(0)).toEqual(['current', 'upcoming', 'upcoming'])
    expect(states(1)).toEqual(['done', 'current', 'upcoming'])
  })

  it('shows every step done once the flow is finished', () => {
    expect(states(3)).toEqual(['done', 'done', 'done'])
  })
})
