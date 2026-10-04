import type {Account} from '../../api/types'

// These are observed account states, not a reconstruction of the scheduler's
// private cooldown or model-specific eligibility rules.
export function quotaLimited(account: Account): boolean {
    return account.status === 'quota_exhausted'
        || (account.codex_linked && Boolean(account.quota?.report?.limit_reached
            || account.quota?.windows?.some(window => window.limit_reached)))
}

export function accountCategory(account: Account): 'normal' | 'limited' | 'disabled' | 'error' {
    if (!account.enabled) return 'disabled'
    if (quotaLimited(account)) return 'limited'
    return account.status === 'ready' ? 'normal' : 'error'
}

export function accountSummary(accounts: Account[]) {
    const counts = {total: accounts.length, normal: 0, limited: 0, disabled: 0, error: 0}
    for (const account of accounts) counts[accountCategory(account)]++
    return counts
}
