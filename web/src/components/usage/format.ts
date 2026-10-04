import {metric} from '../../api/stats-format'

// Match the reference cells' compact units without turning unknown values into zero.
export function duration(value: number | null | undefined): string {
    if (value == null || !Number.isFinite(value) || value < 0) return '—'
    if (value < 1000) return `${metric(value, 2)} ms`
    if (value < 60000) return `${metric(value / 1000, 2)} s`
    return `${metric(value / 60000, 2)} min`
}

export function compactMetric(value: number | null | undefined): string {
    if (value == null || !Number.isFinite(value)) return '—'
    const units = [[1e12, 'T'], [1e9, 'B'], [1e6, 'M'], [1e3, 'K']] as const
    const unit = units.find(([threshold]) => Math.abs(value) >= threshold)
    return unit ? `${metric(value / unit[0], 1)}${unit[1]}` : metric(value)
}
