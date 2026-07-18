import {
  defineWebApplication,
  ApplicationSetupOptions,
  Extension,
  AppMenuItemExtension
} from '@opencloud-eu/web-pkg'
import { urlJoin } from '@opencloud-eu/web-client'
import { RouteRecordRaw } from 'vue-router'
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'

// Minimal hello-world app: registers an app-switcher menu item and a single
// route/view. Enough to prove the extension is packaged and loaded by the
// OpenCloud Web UI (Spike 4). Not the real backup UI.
export default defineWebApplication({
  setup() {
    const { $gettext } = useGettext()

    const appInfo = {
      id: 'backup-spike',
      name: $gettext('Backup Spike'),
      icon: 'archive',
      color: '#1b5e5a'
    }

    const routes: RouteRecordRaw[] = [
      {
        path: '/',
        redirect: `/${appInfo.id}/hello`
      },
      {
        path: '/hello',
        name: 'hello',
        component: () => import('./views/Hello.vue'),
        meta: {
          authContext: 'user',
          title: $gettext('Backup Spike')
        }
      }
    ]

    const extensions = ({ applicationConfig }: ApplicationSetupOptions) => {
      return computed<Extension[]>(() => {
        const menuItems: AppMenuItemExtension[] = [
          {
            id: `app.${appInfo.id}.menuItem`,
            type: 'appMenuItem',
            label: () => appInfo.name,
            color: appInfo.color,
            icon: appInfo.icon,
            path: urlJoin(appInfo.id)
          }
        ]
        return [...menuItems]
      })
    }

    return {
      appInfo,
      routes,
      extensions: extensions({} as ApplicationSetupOptions)
    }
  }
})
