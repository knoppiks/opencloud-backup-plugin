// Lint configuration for the Backup Vault extension.
//
// Deferred from sub-phase 8a ("a Vue/TS lint configuration earns its keep
// alongside the components") and configured here, where there are components.
//
// Prettier owns formatting; ESLint owns everything else. `eslint-config-prettier`
// is last so the two never argue about a line break.
//
// The rule that earned the tool on its first run is the `src/crypto` import ban
// at the bottom. Read its comment before relaxing it.
import eslint from '@eslint/js'
import tseslint from 'typescript-eslint'
import pluginVue from 'eslint-plugin-vue'
import prettier from 'eslint-config-prettier'

export default tseslint.config(
  {
    ignores: ['dist/**', 'coverage/**', 'node_modules/**', '.__mf__temp/**', 'testdata/**']
  },

  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/recommended'],

  {
    // Vue SFCs are parsed by vue-eslint-parser, which delegates <script lang="ts">
    // to the TypeScript parser.
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: { parser: tseslint.parser }
    }
  },

  {
    rules: {
      // An unused import in this codebase is usually a half-finished edit, not
      // a deliberate placeholder. Underscore-prefixed names stay allowed so a
      // required-but-ignored callback parameter can say so.
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_' }
      ],
      // The API client parses untrusted JSON, which is where `any` would
      // otherwise spread from.
      '@typescript-eslint/no-explicit-any': 'error',
      // `console` in a bundle the OpenCloud SPA loads eagerly is somebody
      // else's noise. More to the point, this project must never log key
      // material, and the cheapest way to keep that true is to not log.
      'no-console': 'error',
      // Multi-word component names are an upstream Vue convention this project
      // has no reason to fight, but the views are routed pages rather than
      // reusable components, so the rule is scoped off below for them.
      'vue/multi-word-component-names': 'error'
    }
  },

  {
    // Routed views are named by their route, not by a component namespace.
    files: ['src/views/**/*.vue'],
    rules: { 'vue/multi-word-component-names': 'off' }
  },

  {
    // The host stand-ins for component tests: several tiny stubs in one file,
    // with optional props that mirror the design system's own (which have no
    // defaults either). Test doubles, not components anyone renders.
    files: ['src/test/**/*.ts'],
    rules: { 'vue/one-component-per-file': 'off', 'vue/require-default-prop': 'off' }
  },

  {
    // `src/crypto` has no Vue, no HTTP and no OpenCloud dependency, and 8a's
    // outcome records that as the reason the interop vectors could be written
    // before any UI existed. It has been true by care alone; this makes it
    // true by tooling.
    //
    // The point is not tidiness. The moment a ceremony helper reaches for a
    // composable or sends a request itself, the module stops being reviewable
    // on its own — and it is the module that decides whether a user's Recovery
    // Key opens anything. Anything here that needs the network belongs in
    // `src/api`, which calls into this directory rather than the reverse.
    files: ['src/crypto/**/*.ts'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          paths: [
            { name: 'vue', message: 'src/crypto must stay free of Vue: see eslint.config.ts.' },
            {
              name: 'vue-router',
              message: 'src/crypto must stay free of Vue: see eslint.config.ts.'
            },
            {
              name: 'vue3-gettext',
              message: 'src/crypto must stay free of Vue: see eslint.config.ts.'
            },
            {
              name: 'axios',
              message: 'src/crypto must not speak HTTP: shape the request and let src/api send it.'
            }
          ],
          patterns: [
            {
              group: ['@opencloud-eu/*'],
              message:
                'src/crypto must stay independent of OpenCloud so it can be reviewed and tested on its own.'
            },
            {
              group: ['../api', '../api/*', './api/*'],
              message: 'src/api depends on src/crypto, never the other way round.'
            }
          ]
        }
      ],
      'no-restricted-globals': [
        'error',
        {
          name: 'fetch',
          message: 'src/crypto must not speak HTTP: shape the request and let src/api send it.'
        },
        {
          name: 'XMLHttpRequest',
          message: 'src/crypto must not speak HTTP: shape the request and let src/api send it.'
        }
      ]
    }
  },

  {
    // The setup wizard's machine holds the Recovery Key between the ceremony
    // and the setup POST. It is plain TypeScript so it can be tested without
    // Vue, and it must never persist anything: a key in browser storage
    // outlives the tab, the session and the person's intention (8d decision 3).
    // SetupWizard.vue renders that key, so it is held to the storage ban too.
    files: ['src/wizard/**/*.ts', 'src/views/SetupWizard.vue'],
    rules: {
      'no-restricted-globals': [
        'error',
        { name: 'localStorage', message: 'The setup wizard must not persist anything.' },
        { name: 'sessionStorage', message: 'The setup wizard must not persist anything.' },
        { name: 'indexedDB', message: 'The setup wizard must not persist anything.' }
      ],
      'no-restricted-properties': [
        'error',
        { property: 'localStorage', message: 'The setup wizard must not persist anything.' },
        { property: 'sessionStorage', message: 'The setup wizard must not persist anything.' },
        { property: 'indexedDB', message: 'The setup wizard must not persist anything.' }
      ]
    }
  },

  {
    files: ['src/wizard/**/*.ts'],
    ignores: ['src/wizard/**/*.spec.ts'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          paths: [
            { name: 'vue', message: 'src/wizard is the machine, not the view: keep Vue out.' },
            { name: 'vue3-gettext', message: 'src/wizard is the machine, not the view.' },
            { name: 'vue-router', message: 'src/wizard is the machine, not the view.' }
          ],
          patterns: [
            {
              group: ['@opencloud-eu/*'],
              message: 'src/wizard is the machine, not the view.'
            }
          ]
        }
      ]
    }
  },

  prettier
)
