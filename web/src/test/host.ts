// A stand-in for the OpenCloud host, for component tests.
//
// In production the host provides three things this extension uses without
// importing: the gettext instance, the design-system components (`oc-*`, used
// as globals so a second copy is not bundled) and the router's `router-link`.
// Component tests provide the same three here, once, so every spec mounts a
// view the same way.
//
// The stubs render plain, semantic HTML with the props that matter exposed as
// attributes. They are deliberately dumb: a test asserts what *our* component
// decided to render, not how the design system draws it.

import { mount, type ComponentMountingOptions } from '@vue/test-utils'
import { createGettext } from 'vue3-gettext'
import { defineComponent, h, type Component } from 'vue'

const button = defineComponent({
  name: 'OcButtonStub',
  props: { disabled: Boolean, appearance: String, showSpinner: Boolean, submit: String },
  emits: ['click'],
  setup(props, { slots, emit }) {
    return () =>
      h(
        'button',
        {
          type: props.submit === 'submit' ? 'submit' : 'button',
          disabled: props.disabled,
          onClick: () => emit('click')
        },
        slots.default?.()
      )
  }
})

const textInput = defineComponent({
  name: 'OcTextInputStub',
  props: {
    modelValue: String,
    label: String,
    type: String,
    errorMessage: String,
    descriptionMessage: String,
    disabled: Boolean
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () =>
      h('label', [
        props.label,
        h('input', {
          type: props.type ?? 'text',
          value: props.modelValue,
          disabled: props.disabled,
          onInput: (e: Event) => emit('update:modelValue', (e.target as HTMLInputElement).value)
        }),
        props.descriptionMessage
          ? h('small', { class: 'description' }, props.descriptionMessage)
          : null,
        props.errorMessage ? h('small', { class: 'error' }, props.errorMessage) : null
      ])
  }
})

const progress = defineComponent({
  name: 'OcProgressStub',
  props: { indeterminate: Boolean, value: Number, max: Number },
  setup(props) {
    return () =>
      h('progress', {
        'data-indeterminate': String(props.indeterminate),
        ...(props.indeterminate ? {} : { value: props.value, max: props.max })
      })
  }
})

const routerLink = defineComponent({
  name: 'RouterLinkStub',
  props: { to: { type: [String, Object], required: true } },
  setup(props, { slots }) {
    return () => h('a', { 'data-to': JSON.stringify(props.to) }, slots.default?.())
  }
})

/** hostStubs are the host-provided globals, as test doubles. */
export const hostStubs: Record<string, Component> = {
  'oc-button': button,
  'oc-text-input': textInput,
  'oc-progress': progress,
  'oc-spinner': defineComponent({ setup: () => () => h('span', { class: 'spinner' }) }),
  'router-link': routerLink
}

/**
 * mountWithHost mounts a component the way the host would, in English.
 * Pass `language: 'de'` plus translations to assert a German rendering.
 */
export function mountWithHost<T extends Component>(
  component: T,
  options: ComponentMountingOptions<T> & {
    language?: string
    translations?: Record<string, Record<string, string>>
  } = {}
) {
  const { language, translations, global, ...rest } = options
  const gettext = createGettext({
    defaultLanguage: language ?? 'en',
    translations: translations ?? {},
    silent: true
  })
  return mount(component, {
    ...rest,
    global: {
      ...global,
      plugins: [...(global?.plugins ?? []), gettext],
      stubs: { ...hostStubs, ...(global?.stubs ?? {}) }
    }
  } as ComponentMountingOptions<T>)
}
