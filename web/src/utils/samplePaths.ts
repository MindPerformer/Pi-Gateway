import type {RuleField} from '../api/rules'
import {pointerPart} from './ruleEditor'

export interface SamplePathOption {
    path: string
    label: string
}

export interface SamplePathOptions {
    maxDepth?: number
    maxPaths?: number
    maxNodes?: number
}

/** Build a bounded, non-persistent JSON Pointer catalogue for the current debug sample. */
export function samplePathOptions(value: unknown, options: SamplePathOptions = {}): SamplePathOption[] {
    const maxDepth = Math.max(0, options.maxDepth ?? 8)
    const maxPaths = Math.max(1, options.maxPaths ?? 256)
    const maxNodes = Math.max(1, options.maxNodes ?? 1024)
    const result: SamplePathOption[] = [{path: '', label: '/'}]
    let visited = 0

    function walk(current: unknown, path: string, depth: number) {
        if (result.length >= maxPaths || visited >= maxNodes || depth >= maxDepth || current === null || typeof current !== 'object') return
        visited++
        if (Array.isArray(current)) {
            for (let index = 0; index < current.length && result.length < maxPaths && visited < maxNodes; index++) {
                const childPath = `${path}/${index}`
                result.push({path: childPath, label: childPath})
                walk(current[index], childPath, depth + 1)
            }
            return
        }
        for (const key of Object.keys(current as Record<string, unknown>)) {
            if (result.length >= maxPaths || visited >= maxNodes) break
            const childPath = `${path}/${pointerPart(key)}`
            result.push({path: childPath, label: childPath})
            walk((current as Record<string, unknown>)[key], childPath, depth + 1)
        }
    }

    walk(value, '', 0)
    return result
}

export function samplePathOption(value: unknown, path: string, options?: SamplePathOptions): SamplePathOption | undefined {
    return samplePathOptions(value, options).find(option => option.path === path)
}

export function currentSamplePathField(field: RuleField, source: unknown): boolean {
    return field.type === 'string' && ['path', 'source_path', 'target_path'].includes(field.name) && (source === undefined || source === 'current')
}
