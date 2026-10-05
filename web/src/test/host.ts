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

// `type="router-link"` renders a link like the router-link stub, as the real
// button then renders the host's router-link.
const button = defineComponent({
  name: 'OcButtonStub',
  props: {
    disabled: Boolean,
    appearance: String,
    showSpinner: Boolean,
    submit: String,
    type: String,
    to: [String, Object]
  },
  emits: ['click'],
  setup(props, { slots, emit }) {
    return () =>
      props.type === 'router-link'
        ? h('a', { 'data-to': JSON.stringify(props.to) }, slots.default?.())
        : h(
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

// Like the real OcTextInput (measured on the fixture, 8e), attributes such as
// `data-testid` and `autocomplete` land on the <input> itself, not on a wrapper.
const textInput = defineComponent({
  name: 'OcTextInputStub',
  inheritAttrs: false,
  props: {
    modelValue: String,
    label: String,
    type: String,
    errorMessage: String,
    descriptionMessage: String,
    disabled: Boolean
  },
  emits: ['update:modelValue'],
  setup(props, { emit, attrs }) {
    return () =>
      h('label', [
        props.label,
        h('input', {
          ...attrs,
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

// The trail as a list: a linked crumb renders like the router-link stub, the
// current page as a span with aria-current, as the real one marks it.
const breadcrumb = defineComponent({
  name: 'OcBreadcrumbStub',
  props: { items: { type: Array as () => { text: string; to?: unknown }[], required: true } },
  setup(props) {
    return () =>
      h('nav', { 'data-testid': 'breadcrumb' }, [
        h(
          'ol',
          props.items.map((item, i) =>
            h('li', [
              item.to
                ? h('a', { 'data-to': JSON.stringify(item.to) }, item.text)
                : h(
                    'span',
                    { 'aria-current': i === props.items.length - 1 ? 'page' : null },
                    item.text
                  )
            ])
          )
        )
      ])
  }
})

/** TableField is the part of the design system's column type the stub reads. */
interface TableField {
  name: string
  title?: string
  type?: string
}

// A plain table: one row per item with `data-item-id`, one cell per field
// with the real cell's class, a `slot` field rendered through the slot of
// that name with `{ item }`, and the footer slot in a tfoot.
const table = defineComponent({
  name: 'OcTableStub',
  props: {
    data: { type: Array as () => Record<string, unknown>[], required: true },
    fields: { type: Array as () => TableField[], required: true },
    idKey: { type: String, default: 'id' }
  },
  setup(props, { slots }) {
    return () =>
      h('table', [
        h('thead', [
          h(
            'tr',
            props.fields.map((f) => h('th', f.title ?? f.name))
          )
        ]),
        h(
          'tbody',
          props.data.map((item) =>
            h(
              'tr',
              { 'data-item-id': String(item[props.idKey]) },
              props.fields.map((f) =>
                h(
                  'td',
                  { class: `oc-table-data-cell-${f.name}` },
                  f.type === 'slot' ? slots[f.name]?.({ item }) : String(item[f.name] ?? '')
                )
              )
            )
          )
        ),
        slots.footer ? h('tfoot', [h('tr', [h('td', slots.footer())])]) : null
      ])
  }
})

/** hostStubs are the host-provided globals, as test doubles. */
export const hostStubs: Record<string, Component> = {
  'oc-breadcrumb': breadcrumb,
  'oc-button': button,
  'oc-icon': defineComponent({
    props: { name: String },
    setup: (props) => () => h('i', { 'data-icon': props.name })
  }),
  'oc-table': table,
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
