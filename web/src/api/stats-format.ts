import {currentLocale} from '../i18n'

// Keep unknown metrics distinct from a measured zero, including in table cells.
export function metric(value: number | null | undefined, digits = 0): string {
    if (value == null || !Number.isFinite(value)) return '—'
    return new Intl.NumberFormat(currentLocale(), {maximumFractionDigits: digits}).format(value)
}

export function money(value: number | null | undefined): string {
    if (value == null || !Number.isFinite(value)) return '—'
    return new Intl.NumberFormat(currentLocale(), {
        style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 6,
    }).format(value)
}

export function percent(value: number | null | undefined): string {
    return value == null || !Number.isFinite(value) ? '—' : `${metric(value * 100, 2)}%`
}

export function rate(count: number, total: number): number | null {
    return total > 0 ? count / total : null
}
