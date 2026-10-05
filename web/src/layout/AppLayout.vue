<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { useUiStore } from '../stores/ui'
import { useI18n } from '../i18n'
import { LOCALES } from '../i18n/messages'
import {
	Activity, ChartNoAxesCombined, ListFilter, Braces, LayoutDashboard,
	KeyRound, Languages, LogOut, Menu, Moon, Network, PanelLeftClose, PanelLeftOpen,
	Settings as SettingsIcon, Sun, Users, X,
} from 'lucide-vue-next'

const auth = useAuthStore()
const ui = useUiStore()
const route = useRoute()
const router = useRouter()
const { t, locale, setLocale } = useI18n()
const mobileOpen = ref(false)
const main = ref<HTMLElement>()
const mobileButton = ref<HTMLButtonElement>()
const sidebar = ref<HTMLElement>()

const nav = computed(() => [
	{ name: 'dashboard', label: t('nav.dashboard'), to: '/dashboard', icon: LayoutDashboard },
	{ name: 'accounts', label: t('nav.accounts'), to: '/accounts', icon: Users },
	{ name: 'proxies', label: t('nav.proxies'), to: '/proxies', icon: Network },
	{ name: 'keys', label: t('nav.keys'), to: '/keys', icon: KeyRound },
	{ name: 'stats', label: t('nav.stats'), to: '/stats', icon: ChartNoAxesCombined },
	{ name: 'usage', label: t('nav.usage'), to: '/usage', icon: ListFilter },
	{ name: 'captures', label: t('nav.captures'), to: '/captures', icon: Activity },
	{ name: 'rules', label: t('rules.title'), to: '/rules', icon: Braces },
	{ name: 'settings', label: t('nav.settings'), to: '/settings', icon: SettingsIcon },
])
const activeName = computed(() => route.name === 'capture-detail' ? 'captures' : String(route.name ?? ''))
const themeLabel = computed(() => t(ui.theme === 'dark' ? 'appearance.light' : 'appearance.dark'))

async function closeMobile() {
	mobileOpen.value = false
	await nextTick()
	mobileButton.value?.focus()
}
async function openMobile() {
	mobileOpen.value = true
	await nextTick()
	sidebar.value?.querySelector<HTMLButtonElement>('.sidebar-mobile-close')?.focus()
}
function onKeydown(event: KeyboardEvent) {
	if (!mobileOpen.value) return
	if (event.key === 'Escape') { event.preventDefault(); void closeMobile() }
	if (event.key !== 'Tab') return
	const elements = Array.from(sidebar.value?.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), select') ?? []).filter(el => el.offsetParent !== null)
	const first = elements[0]
	const last = elements[elements.length - 1]
	if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
	else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
const desktop = window.matchMedia('(min-width: 961px)')
const closeOnDesktop = (event: MediaQueryListEvent) => { if (event.matches) mobileOpen.value = false }
window.addEventListener('keydown', onKeydown)
desktop.addEventListener('change', closeOnDesktop)
onBeforeUnmount(() => {
	window.removeEventListener('keydown', onKeydown)
	desktop.removeEventListener('change', closeOnDesktop)
})
watch(() => route.fullPath, async () => {
	mobileOpen.value = false
	await nextTick()
	main.value?.scrollTo({ top: 0 })
})
async function signOut() {
	await auth.signOut()
	void router.push({ name: 'login' })
}
</script>

<template>
	<div class="app-shell" :class="{ 'is-collapsed': ui.sidebarCollapsed, 'is-mobile-open': mobileOpen }">
		<button v-if="mobileOpen" class="sidebar-backdrop" :aria-label="t('nav.closeMenu')" tabindex="-1" @click="closeMobile" />
		<aside id="app-sidebar" ref="sidebar" class="app-sidebar" :aria-label="t('nav.navigation')">
			<div class="sidebar-brand">
				<RouterLink to="/dashboard" class="brand-mark" aria-label="Pi Gateway">π</RouterLink>
				<div class="sidebar-brand-label">
					<div class="brand-name">Pi Gateway</div>
					<div class="brand-caption">{{ t('nav.subtitle') }}</div>
				</div>
				<button class="btn btn-ghost btn-icon sidebar-mobile-close" :aria-label="t('nav.closeMenu')" @click="closeMobile"><X /></button>
			</div>
			<nav class="sidebar-nav" :aria-label="t('nav.navigation')">
				<RouterLink v-for="item in nav" :key="item.name" :to="item.to" class="sidebar-link" :class="{ 'is-active': activeName === item.name }" :title="ui.sidebarCollapsed ? item.label : undefined" :aria-current="activeName === item.name ? 'page' : undefined">
					<component :is="item.icon" aria-hidden="true" />
					<span class="sidebar-label">{{ item.label }}</span>
				</RouterLink>
			</nav>
			<div class="sidebar-footer">
				<div class="sidebar-language">
					<Languages class="h-4 w-4 shrink-0" aria-hidden="true" />
					<select :value="locale" class="input" :aria-label="t('common.language')" @change="setLocale(($event.target as HTMLSelectElement).value as 'en' | 'zh-CN')">
						<option v-for="option in LOCALES" :key="option.value" :value="option.value">{{ option.label }}</option>
					</select>
				</div>
				<div class="sidebar-user">
					<div class="sidebar-identity" :title="`${t('nav.signedInAs')} ${auth.username || 'admin'}`">
						<span class="h-1.5 w-1.5 shrink-0 rounded-full bg-[color:var(--color-success)]" />
						<span class="truncate">{{ auth.username || 'admin' }}</span>
					</div>
					<div class="sidebar-controls">
						<button class="btn btn-ghost btn-icon" :aria-label="themeLabel" :title="themeLabel" @click="ui.toggleTheme"><component :is="ui.theme === 'dark' ? Sun : Moon" /></button>
						<button class="btn btn-ghost btn-icon sidebar-signout" :aria-label="t('nav.signOut')" :title="t('nav.signOut')" @click="signOut"><LogOut /></button>
						<button class="btn btn-ghost btn-icon sidebar-toggle" :aria-label="t(ui.sidebarCollapsed ? 'nav.expand' : 'nav.collapse')" :title="t(ui.sidebarCollapsed ? 'nav.expand' : 'nav.collapse')" @click="ui.toggleSidebar"><component :is="ui.sidebarCollapsed ? PanelLeftOpen : PanelLeftClose" /></button>
					</div>
				</div>
			</div>
		</aside>
		<button ref="mobileButton" class="btn btn-icon mobile-menu-button" :aria-label="t('nav.openMenu')" aria-controls="app-sidebar" :aria-expanded="mobileOpen" :inert="mobileOpen" @click="openMobile"><Menu /></button>
		<main ref="main" class="app-main" :inert="mobileOpen"><RouterView /></main>
	</div>
</template>
