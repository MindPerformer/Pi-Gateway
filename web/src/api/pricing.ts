import { request } from './client'
import type { BillingDetails } from './types'

export interface DecimalRates { input: string; cached_input: string; cache_write: string; output: string }
export type PriceTier = 'standard' | 'flex' | 'fast' | 'long_standard' | 'long_flex' | 'long_fast'
export interface ModelPrice {
    model: string; name?: string; aliases?: string[]
    standard: DecimalRates | null; flex: DecimalRates | null; fast: DecimalRates | null
    long_standard: DecimalRates | null; long_flex: DecimalRates | null; long_fast: DecimalRates | null
    context_mode: 'same' | 'custom' | 'unpriced'; context_threshold: number; context_comparison: 'gt' | 'gte'
    disabled: boolean; source?: string; source_id?: string; source_url?: string; note?: string
}
export interface ModelView {
    model: string; name?: string; effective: ModelPrice | null; builtin: ModelPrice | null; manual: ModelPrice | null; external: ModelPrice | null
    following_source_id?: string; legacy_multiplier?: number; status: 'priced' | 'unpriced' | 'blocked'; source: string
}
export interface PriceSource {
    id: string; name: string; url: string; format: 'standard' | 'models_dev'; enabled: boolean; interval_hours: number; auto_apply: boolean
    proxy_id?: number; trusted_hosts?: string[]; last_attempt_at?: number; last_success_at?: number; last_error?: string; candidate_count?: number
}
export interface SyncItem { model: string; before: ModelPrice | null; after: ModelPrice; warnings: string[]; locked: boolean; selected?: boolean }
export interface SyncRun { id: string; source_id: string; created_at: number; status: string; error?: string; base_revision: number; hash: string; items: SyncItem[] }
export interface HistoryEntry { id: string; created_at: number; actor: string; action: string; model?: string; before?: ModelPrice | null; after?: ModelPrice | null; revision: number }
export interface StandardPriceFile { schema_version: 1; currency: 'USD'; unit: 'per_million_tokens'; models: ModelPrice[] }
export interface RecalculateQuery { expected_revision: number; model?: string; account_id?: number; start_at?: number; end_at?: number; limit?: number; after_id?: number; preview_token?: string }
export interface RecalculatePreview {
    revision: number; preview_token: string; matched: number; eligible: number; already_repriced: number; missing_usage: number; unpriced: number; cost_micros: number
    has_more: boolean; next_after_id: number; items: Array<{ id: number; model: string; account_id: number; cost_micros: number; price_version: string }>
}
export interface RecalculateResult { revision: number; updated: number; skipped: number; cost_micros: number; has_more: boolean; next_after_id: number }
const post = <T>(path: string, body: unknown) => request<T>(`/api/pricing${path}`, { method: 'POST', body: JSON.stringify(body) })
const key = encodeURIComponent
export const pricing = {
    models: () => request<{revision: number; items: ModelView[]}>('/api/pricing/models'),
    save: (expected_revision: number, price: ModelPrice) => post<{revision: number}>('/models', {expected_revision, price}),
    reset: (expected_revision: number, model: string) => post<{revision: number}>('/models/reset', {expected_revision, model}),
    block: (expected_revision: number, model: string) => post<{revision: number}>('/models/block', {expected_revision, model}),
    follow: (expected_revision: number, model: string, source_id: string) => post<{revision: number}>('/models/follow', {expected_revision, model, source_id}),
    sources: () => request<{revision: number; items: PriceSource[]}>('/api/pricing/sources'),
    saveSource: (expected_revision: number, source: PriceSource) => post<{revision: number}>('/sources', {expected_revision, source}),
    deleteSource: (expected_revision: number, id: string) => post<{revision: number}>(`/sources/${key(id)}/delete`, {expected_revision}),
    testSource: (id: string) => post<{ok: boolean; error?: string; count?: number}>(`/sources/${key(id)}/test`, {}),
    syncSource: (id: string) => post<{revision: number; run: SyncRun}>(`/sources/${key(id)}/sync`, {}),
    runs: () => request<{revision: number; items: SyncRun[]}>('/api/pricing/sync-runs'),
    apply: (expected_revision: number, id: string, models: string[]) => post<{revision: number}>(`/sync-runs/${key(id)}/apply`, {expected_revision, models}),
    export: () => request<StandardPriceFile>('/api/pricing/export'),
    import: (expected_revision: number, document: StandardPriceFile) => post<{revision: number; run: SyncRun}>('/import', {expected_revision, document}),
    preview: (body: {price?: ModelPrice; model: string; tier: string; input_tokens: number; cached_tokens: number; cache_write_tokens: number; output_tokens: number}) => post<{billing: BillingDetails}>('/preview', body),
    history: () => request<{revision: number; items: HistoryEntry[]}>('/api/pricing/history'),
    restore: (expected_revision: number, id: string) => post<{revision: number}>(`/history/${key(id)}/restore`, {expected_revision}),
    recalculatePreview: (body: RecalculateQuery) => post<RecalculatePreview>('/recalculate/preview', body),
    recalculate: (body: RecalculateQuery) => post<RecalculateResult>('/recalculate', body),
}
export const rateFields = ['input', 'cached_input', 'cache_write', 'output'] as const
export const priceTiers: PriceTier[] = ['standard', 'flex', 'fast', 'long_standard', 'long_flex', 'long_fast']
export function emptyRates(): DecimalRates { return {input: '', cached_input: '', cache_write: '', output: ''} }
export function emptyPrice(model = ''): ModelPrice {
    return {model, standard: emptyRates(), flex: null, fast: null, long_standard: null, long_flex: null, long_fast: null, context_mode: 'same', context_threshold: 0, context_comparison: 'gt', disabled: false}
}
