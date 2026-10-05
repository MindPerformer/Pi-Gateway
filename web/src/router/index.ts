import {createRouter, createWebHistory} from 'vue-router'
import {getToken} from '../api/client'

const router = createRouter({
    history: createWebHistory(),
    routes: [
        {
            path: '/login',
            name: 'login',
            component: () => import('../views/LoginView.vue'),
            meta: {public: true},
        },
        {
            path: '/',
            component: () => import('../layout/AppLayout.vue'),
            children: [
                {path: '', redirect: '/dashboard'},
                {path: 'dashboard', name: 'dashboard', component: () => import('../views/DashboardView.vue')},
                {path: 'accounts', name: 'accounts', component: () => import('../views/AccountsView.vue')},
                {path: 'proxies', name: 'proxies', component: () => import('../views/ProxiesView.vue')},
                {path: 'keys', name: 'keys', component: () => import('../views/KeysView.vue')},
                {path: 'stats', name: 'stats', component: () => import('../views/StatsView.vue')},
                {path: 'usage', name: 'usage', component: () => import('../views/UsageView.vue')},
                {path: 'captures', name: 'captures', component: () => import('../views/CapturesView.vue')},
                {
                    path: 'captures/:id',
                    name: 'capture-detail',
                    component: () => import('../views/CaptureDetailView.vue')
                },
                {path: 'rules', name: 'rules', component: () => import('../views/MiddlewaresView.vue')},
                {path: 'middlewares', redirect: '/rules'},
                {path: 'settings', name: 'settings', component: () => import('../views/SettingsView.vue')},
            ],
        },
        {path: '/:pathMatch(.*)*', redirect: '/dashboard'},
    ],
})

router.beforeEach((to) => {
    if (to.meta.public) {
        if (getToken() && to.name === 'login') return {name: 'dashboard'}
        return true
    }
    if (!getToken()) return {name: 'login', query: {redirect: to.fullPath}}
    return true
})

export default router
