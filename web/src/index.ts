// Backup Vault — OpenCloud Web extension entry point.
//
// The host imports this module through Module Federation and reads what
// `defineWebApplication` returns: the app metadata, the routes to mount under
// /backup-vault/, and the extensions to register (here, the app-menu entry that
// makes the vault reachable at all).
//
// The views themselves land in sub-phase 8c/8d; what exists today is the
// client-side crypto (src/crypto), which is the part that has to be right
// before anything can be built on top of it.
import {
  defineWebApplication,
  type AppMenuItemExtension,
  type Extension
} from '@opencloud-eu/web-pkg'
import { urlJoin } from '@opencloud-eu/web-client'
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import type { RouteRecordRaw } from 'vue-router'

const appId = 'backup-vault'

export default defineWebApplication({
  setup() {
    const { $gettext } = useGettext()

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

    return { appInfo, routes, extensions }
  }
})
