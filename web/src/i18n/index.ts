import {computed, ref} from 'vue'
import {detectLocale, type Locale, translate} from './messages'

// A single module-level locale ref keeps every view and the plain formatter
// helpers in sync without threading props through the tree.
const current = ref<Locale>(detectLocale())
document.documentElement.lang = current.value

export function setLocale(next: Locale) {
    current.value = next
    localStorage.setItem('pi-gateway-locale', next)
    document.documentElement.lang = next
}

/** Translate outside a component setup (used by the formatters). */
export function translateNow(key: string, params?: Record<string, string | number>): string {
    return translate(current.value, key, params)
}

export function currentLocale(): Locale {
    return current.value
}

export function useI18n() {
    return {
        locale: computed(() => current.value),
        t: translateNow,
        setLocale,
    }
}

/** Format a number using the active locale. */
export function formatNumber(value: number): string {
    return new Intl.NumberFormat(current.value).format(value)
}

/** Format an absolute timestamp using the active locale. */
export function formatDateTime(ms: number): string {
    if (!ms) return '—'
    return new Intl.DateTimeFormat(current.value, {
        dateStyle: 'medium',
        timeStyle: 'medium',
    }).format(new Date(ms))
}
