// Backup Vault — OpenCloud Web extension entry point.
//
// The host imports this module through Module Federation and reads what
// `defineWebApplication` returns: the app metadata, the routes to mount under
// /backup-vault/, the translations to merge into its gettext instance, and the
// extensions to register (here, the app-menu entry that makes the vault
// reachable at all).
//
// `setup()` is also the only place the host hands over `applicationConfig`, so
// it is where the API base URL is resolved. See appconfig.ts for why that is
// captured rather than injected, and api/baseurl.ts for why the only thing
// configurable is a path.
//
// The SDK's Tailwind setup generates the `ext:`-prefixed utilities the views
// use. Without this import the build emits no CSS and every `ext:` class is
// inert (found through #55, where the Recovery Key's layout depends on it).
import '@opencloud-eu/extension-sdk/tailwind.css'
import {
  defineWebApplication,
  type AppMenuItemExtension,
  type ApplicationSetupOptions,
  type Extension
} from '@opencloud-eu/web-pkg'
import { urlJoin } from '@opencloud-eu/web-client'
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import type { RouteRecordRaw } from 'vue-router'
import { configureApi, type BackupVaultConfig } from './appconfig'
import { translations } from './l10n/translations'

const appId = 'backup-vault'

export default defineWebApplication({
  setup({ applicationConfig }: ApplicationSetupOptions) {
    const { $gettext } = useGettext()

    // Deliberately not guarded: a misconfigured apiPath throws here, at app
    // setup, which is the earliest and loudest place an operator can be told.
    // The alternative is an extension that loads and then fails every request.
    configureApi((applicationConfig ?? {}) as BackupVaultConfig, globalThis.location?.origin ?? '')

    const appInfo = {
      id: appId,
      name: $gettext('Backup Vault'),
      icon: 'archive',
      color: '#1b5e5a'
    }

    const routes: RouteRecordRaw[] = [
      { path: '/', redirect: urlJoin(appId, 'overview') },
      {
        path: '/overview',
        name: 'backup-vault-overview',
        component: () => import('./views/Overview.vue'),
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        // The Space id travels as a route param and reaches the view as a
        // prop, so the view never needs the router (which this remote must
        // not import; see composables/useBackupApi.ts).
        path: '/space/:spaceId',
        name: 'backup-vault-space',
        component: () => import('./views/SpaceStatus.vue'),
        props: true,
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        // The wizard resumes from the server's state, so this route carries
        // the Space id and nothing else: no step, and never key material.
        path: '/space/:spaceId/setup',
        name: 'backup-vault-setup',
        component: () => import('./views/SetupWizard.vue'),
        props: true,
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        // Same shape as the wizard: the Space id and nothing else, so a reload
        // lands on the picker, or on the restore already running.
        path: '/space/:spaceId/restore',
        name: 'backup-vault-restore',
        component: () => import('./views/RestoreView.vue'),
        props: true,
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        // "Check my Recovery Key" and the key file, for any member. Like every
        // route here it carries the Space id and nothing else: a Recovery Key
        // never goes into a URL.
        path: '/space/:spaceId/recovery-key',
        name: 'backup-vault-recovery-key',
        component: () => import('./views/RecoveryKeyView.vue'),
        props: true,
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        // Replacement, for managers. A reload starts over at the current key.
        path: '/space/:spaceId/recovery-key/replace',
        name: 'backup-vault-recovery-key-replace',
        component: () => import('./views/ReplaceRecoveryKey.vue'),
        props: true,
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      // Admin: backup destinations and who may use them. Registered for
      // everyone, like every route: each view checks the admin ability before
      // it calls anything, and the server's 403 is what actually decides.
      // Credentials never go into these URLs; a target id is not a secret.
      {
        path: '/admin/targets',
        name: 'backup-vault-admin-targets',
        component: () => import('./views/AdminTargets.vue'),
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        // vue-router ranks this static path above `:targetId`, so "new" is
        // never read as an id.
        path: '/admin/targets/new',
        name: 'backup-vault-admin-target-new',
        component: () => import('./views/AdminTargetNew.vue'),
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      },
      {
        path: '/admin/targets/:targetId',
        name: 'backup-vault-admin-target',
        component: () => import('./views/AdminTargetEdit.vue'),
        props: true,
        meta: { authContext: 'user', title: $gettext('Backup Vault') }
      }
    ]

    const extensions = computed<Extension[]>(() => {
      const menuItem: AppMenuItemExtension = {
        id: `app.${appId}.menuItem`,
        type: 'appMenuItem',
        label: () => appInfo.name,
        color: appInfo.color,
        icon: appInfo.icon,
        path: urlJoin(appId)
      }
      return [menuItem]
    })

    return { appInfo, routes, extensions, translations }
  }
})
