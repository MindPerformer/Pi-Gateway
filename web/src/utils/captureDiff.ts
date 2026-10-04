export interface DiffLine {
    kind: 'context' | 'added' | 'removed'
    text: string
    beforeLine?: number
    afterLine?: number
}

export interface DiffResult {
    equal: boolean
    lines: DiffLine[]
    added: number
    removed: number
    limited: boolean
}

// Bounds apply before allocating the LCS table or building the displayed diff.
const MAX_CHARACTERS = 65_536
const MAX_LINES = 1_600
const MAX_LINE_CHARACTERS = 2_000
const MAX_LCS_CELLS = 300_000
const MAX_DEPTH = 64
const MAX_VALUES = 12_000
const TRUNCATED = ' … [truncated] … '
const OMITTED = '… [content omitted]'

interface PreparedPayload {
    text: string
    kind: string
    complete: boolean
}

interface DisplayLine {
    text: string
    number?: number
}

interface DisplayPayload {
    lines: DisplayLine[]
    limited: boolean
}

/** Header names and ordering are insignificant; duplicate values remain significant. */
export function normalizeHeaders(headers: Array<{ name: string; value: string }>): unknown {
    return headers
        .map(({name, value}) => ({name: name.toLowerCase(), value}))
        .sort((left, right) => compareText(left.name, right.name) || compareText(left.value, right.value))
}

function compareText(left: string, right: string): number {
    return left < right ? -1 : left > right ? 1 : 0
}

function crop(text: string, limit: number): string {
    if (text.length <= limit) return text
    const available = limit - TRUNCATED.length
    const start = Math.ceil(available / 2)
    return text.slice(0, start) + TRUNCATED + text.slice(text.length - (available - start))
}

/**
 * Serialize without constructing keyed objects, invoking toJSON/getters, or mutating inputs.
 * Incomplete output is only a preview, never evidence of equality.
 */
function preparePayload(input: unknown): PreparedPayload {
    let value = input
    if (typeof value === 'string') {
        if (value.length > MAX_CHARACTERS) {
            return {text: crop(value, MAX_CHARACTERS), kind: 'string', complete: false}
        }
        try {
            value = JSON.parse(value)
        } catch {
            // Non-JSON strings are compared and displayed as text, including empty strings.
        }
    }

    const kind = value === null ? 'null' : typeof value
    const parts: string[] = []
    const ancestors = new Set<object>()
    const halt = Symbol('bounded serialization')
    let characters = 0
    let visited = 0
    let complete = true
    let reason = OMITTED

    function stop(message = OMITTED): never {
        reason = message
        throw halt
    }

    function append(text: string): void {
        const remaining = MAX_CHARACTERS - characters
        if (text.length > remaining) {
            parts.push(text.slice(0, remaining))
            stop()
        }
        parts.push(text)
        characters += text.length
    }

    function quoted(text: string): void {
        // Escape at most one bounded string, even if an API field contains megabytes.
        if (text.length > MAX_CHARACTERS - characters) {
            append(JSON.stringify(text.slice(0, MAX_CHARACTERS - characters)))
            stop()
        }
        append(JSON.stringify(text))
    }

    function ownValue(object: object, key: string): unknown {
        const descriptor = Object.getOwnPropertyDescriptor(object, key)
        if (!descriptor || !('value' in descriptor)) stop('… [unreadable property]')
        return descriptor.value
    }

    function visit(current: unknown, depth: number): void {
        if (++visited > MAX_VALUES || depth > MAX_DEPTH) stop()
        if (current === null) {
            append('null')
            return
        }
        switch (typeof current) {
            case 'string':
                if (depth === 0) append(current)
                else quoted(current)
                return
            case 'undefined':
                append('undefined')
                return
            case 'boolean':
            case 'number':
                append(String(current))
                return
            case 'object':
                break
            default:
                stop('… [unsupported value]')
        }

        if (ancestors.has(current)) stop('… [circular value]')
        const array = Array.isArray(current)
        if (!array) {
            const prototype = Object.getPrototypeOf(current)
            if (prototype !== null && prototype !== Object.prototype) stop('… [unsupported object]')
        }
        ancestors.add(current)
        const keys = array ? undefined : Object.keys(current)
        const count = array ? ownValue(current, 'length') as number : keys!.length
        if (count > MAX_VALUES - visited) stop()
        keys?.sort()
        append(array ? '[' : '{')
        for (let index = 0; index < count; index++) {
            append(index === 0 ? '\n' : ',\n')
            append('  '.repeat(depth + 1))
            const key = array ? String(index) : keys![index]!
            if (!array) {
                quoted(key)
                append(': ')
            }
            visit(ownValue(current, key), depth + 1)
        }
        if (count > 0) append('\n' + '  '.repeat(depth))
        append(array ? ']' : '}')
        ancestors.delete(current)
    }

    try {
        visit(value, 0)
    } catch (error) {
        complete = false
        if (error !== halt) reason = '… [unreadable value]'
    }
    let text = parts.join('')
    if (!complete) text = text.slice(0, MAX_CHARACTERS - reason.length - 1) + '\n' + reason
    return {text, kind, complete}
}

function preparedEqual(before: PreparedPayload, after: PreparedPayload): boolean {
    return before.complete && after.complete && before.kind === after.kind && before.text === after.text
}

/**
 * Availability is deliberately not inferred: null, undefined and empty text are values.
 * Beyond the safety limits, only exact input identity can establish equality.
 */
export function equalPayloads(before: unknown, after: unknown): boolean {
    // Full raw string equality is conclusive even when rendering will be truncated.
    if (before === after) return true
    return preparedEqual(preparePayload(before), preparePayload(after))
}

function displayLines(payload: PreparedPayload, labelType: boolean): DisplayPayload {
    const text = labelType ? `[${payload.kind}]\n${payload.text}` : payload.text
    const lines: DisplayLine[] = []
    let limited = !payload.complete
    let position = 0
    let number = 1
    while (true) {
        const newline = text.indexOf('\n', position)
        const end = newline === -1 ? text.length : newline
        const line = text.slice(position, end)
        if (line.length > MAX_LINE_CHARACTERS) limited = true
        lines.push({text: crop(line, MAX_LINE_CHARACTERS), number: number++})
        if (newline === -1) break
        if (lines.length === MAX_LINES) {
            lines[MAX_LINES - 1] = {text: OMITTED}
            limited = true
            break
        }
        position = newline + 1
    }
    return {lines, limited}
}

function contextLine(before: DisplayLine, after: DisplayLine): DiffLine {
    return {kind: 'context', text: before.text, beforeLine: before.number, afterLine: after.number}
}

function result(equal: boolean, lines: DiffLine[], limited: boolean): DiffResult {
    return {
        equal,
        lines,
        added: lines.filter((line) => line.kind === 'added').length,
        removed: lines.filter((line) => line.kind === 'removed').length,
        limited,
    }
}

function abbreviated(lines: DisplayLine[], budget: number): DisplayLine[] {
    if (lines.length <= budget) return lines
    const start = Math.ceil((budget - 1) / 2)
    const end = budget - start - 1
    return [...lines.slice(0, start), {text: OMITTED}, ...lines.slice(lines.length - end)]
}

/** Counts describe the returned rows; limited=true makes no claim about omitted edits. */
function replacement(before: DisplayLine[], after: DisplayLine[]): DiffResult {
    const left = abbreviated(before, MAX_LINES / 2)
    const right = abbreviated(after, MAX_LINES / 2)
    return result(false, [
        ...left.map((line): DiffLine => ({kind: 'removed', text: line.text, beforeLine: line.number})),
        ...right.map((line): DiffLine => ({kind: 'added', text: line.text, afterLine: line.number})),
    ], true)
}

/** A unified, line-numbered diff of canonical JSON or literal non-JSON text. */
export function diffPayloads(before: unknown, after: unknown): DiffResult {
    const identical = before === after
    const first = preparePayload(before)
    const second = identical ? first : preparePayload(after)
    const equal = identical || preparedEqual(first, second)
    // For example, JSON string "null" and JSON null need visibly distinct types.
    const labelType = !equal && first.kind !== second.kind && first.text === second.text
    const left = displayLines(first, labelType)
    const right = displayLines(second, labelType)
    const limited = left.limited || right.limited
    if (equal) {
        return result(true, left.lines.map((line, index) => contextLine(line, right.lines[index] ?? line)), limited)
    }
    if (limited) return replacement(left.lines, right.lines)

    const a = left.lines
    const b = right.lines
    let prefix = 0
    while (prefix < a.length && prefix < b.length && a[prefix]!.text === b[prefix]!.text) prefix++
    let suffix = 0
    while (
        suffix < a.length - prefix && suffix < b.length - prefix
        && a[a.length - suffix - 1]!.text === b[b.length - suffix - 1]!.text
        ) suffix++

    const rows = a.length - prefix - suffix
    const columns = b.length - prefix - suffix
    const width = columns + 1
    const cells = (rows + 1) * width
    if (cells > MAX_LCS_CELLS) return replacement(a, b)
    const table = new Uint16Array(cells)
    for (let row = rows - 1; row >= 0; row--) {
        for (let column = columns - 1; column >= 0; column--) {
            table[row * width + column] = a[prefix + row]!.text === b[prefix + column]!.text
                ? table[(row + 1) * width + column + 1]! + 1
                : Math.max(table[(row + 1) * width + column]!, table[row * width + column + 1]!)
        }
    }
    if (prefix + suffix + rows + columns - table[0]! > MAX_LINES) return replacement(a, b)

    const lines: DiffLine[] = []
    for (let index = 0; index < prefix; index++) lines.push(contextLine(a[index]!, b[index]!))
    let row = 0
    let column = 0
    while (row < rows || column < columns) {
        const beforeLine = a[prefix + row]
        const afterLine = b[prefix + column]
        if (row < rows && column < columns && beforeLine!.text === afterLine!.text) {
            lines.push(contextLine(beforeLine!, afterLine!))
            row++
            column++
        } else if (row < rows && (column === columns || table[(row + 1) * width + column]! >= table[row * width + column + 1]!)) {
            lines.push({kind: 'removed', text: beforeLine!.text, beforeLine: beforeLine!.number})
            row++
        } else {
            lines.push({kind: 'added', text: afterLine!.text, afterLine: afterLine!.number})
            column++
        }
    }
    for (let index = suffix; index > 0; index--) lines.push(contextLine(a[a.length - index]!, b[b.length - index]!))
    // Defensive: identical previews must never turn an unproven comparison into equality.
    if (!lines.some((line) => line.kind !== 'context')) return replacement(a, b)
    return result(false, lines, false)
}
