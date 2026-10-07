import type {Rule, RuleAction, RuleCondition, RuleFieldError, RuleSchema} from '../api/rules'
import {editorId} from './editorId'
import {newAction, ruleSchema} from './ruleSchema'
import {defaultCondition, RuleInputError, validateRule} from './ruleEditor'

export type RuleGraphNode = {
    id: string; kind: 'rule' | 'condition' | 'action'; path: string;
    position: { x: number; y: number }; data: Rule | RuleCondition | RuleAction;
}

export interface RuleGraphEdge {
    id: string;
    source: string;
    target: string;
    sourceHandle: 'boolean-out' | 'action-out';
    targetHandle: string;
    order: number;
}

export interface RuleGraph {
    nodes: RuleGraphNode[];
    edges: RuleGraphEdge[];
    viewport: { x: number; y: number; zoom: number };
    selected: string[];
    layoutRestored?: boolean;
}

export interface RuleGraphAddOptions {
    kind: 'condition' | 'action'
    type: string
    targetId?: string
    position?: { x: number; y: number }
    mode?: 'auto' | 'detached' | 'wrap'
}

export interface RuleGraphLayout {
    signature: string
    nodes: Record<string, { x: number; y: number }>
    viewport: { x: number; y: number; zoom: number }
}

const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value))
const layoutStoragePrefix = 'pi-rule-layout:'
// Leave room for inline value/reference editors; saved or explicitly placed positions stay exact.
const defaultRowSpacing = 600
const memoryLayouts = new Map<string, RuleGraphLayout>()
const validCoordinate = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && Math.abs(value) <= 10_000_000
const validViewport = (value: RuleGraph['viewport'] | undefined) => !!value && validCoordinate(value.x) && validCoordinate(value.y) && Number.isFinite(value.zoom) && value.zoom >= 0.15 && value.zoom <= 2.5

export function graphLayoutNodeKey(node: RuleGraphNode, graph: RuleGraph, paths = graphPathMap(graph)): string {
    if (node.kind === 'rule') return 'rule'
    if (node.kind === 'action') return `action:${(node.data as RuleAction).id}`
    const path = paths[node.id]
    if (path === undefined) throw new Error('Only connected nodes have stable layout identities')
    const action = graph.nodes.find(n => n.kind === 'action' && path.startsWith(`${paths[n.id]}/params/`))
    return action ? `action:${(action.data as RuleAction).id}${path.slice(paths[action.id]!.length)}` : `condition:${path}`
}

export function graphLayoutSignature(graph: RuleGraph): string {
    if (graph.nodes.length > 2048) throw new Error('Layout node budget exceeded')
    const paths = graphPathMap(graph)
    const keys = new Map(graph.nodes.map(node => [node.id, graphLayoutNodeKey(node, graph, paths)]))
    const nodes = graph.nodes.map(node => ({
        key: keys.get(node.id)!,
        kind: node.kind,
        type: node.kind === 'condition' ? (node.data as RuleCondition).op : node.kind === 'action' ? (node.data as RuleAction).type : 'rule'
    })).sort((a, b) => a.key.localeCompare(b.key))
    const edges = graph.edges.map(edge => ({
        source: keys.get(edge.source),
        target: keys.get(edge.target),
        sourceHandle: edge.sourceHandle,
        targetHandle: edge.targetHandle,
        order: edge.order
    })).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)))
    // Hash topology only: no name, parameter value, payload field path or sample is stored.
    let hash = 2166136261
    for (const char of JSON.stringify({nodes, edges})) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619)
    return `v1-${(hash >>> 0).toString(16)}`
}

export function graphLayoutSnapshot(graph: RuleGraph): RuleGraphLayout {
    if (graph.nodes.length > 2048) throw new Error('Layout node budget exceeded')
    const paths = graphPathMap(graph)
    if (Object.keys(paths).length !== graph.nodes.length) throw new Error('Invalid layout topology')
    const nodes: RuleGraphLayout['nodes'] = Object.create(null)
    for (const node of graph.nodes) {
        if (!validCoordinate(node.position.x) || !validCoordinate(node.position.y)) throw new Error('Invalid layout coordinate')
        nodes[graphLayoutNodeKey(node, graph, paths)] = {x: node.position.x, y: node.position.y}
    }
    const viewport = validViewport(graph.viewport) ? {
        x: graph.viewport.x,
        y: graph.viewport.y,
        zoom: graph.viewport.zoom
    } : {x: 0, y: 0, zoom: 1}
    return {signature: graphLayoutSignature(graph), nodes, viewport}
}

export function applyGraphLayout(graph: RuleGraph, layout: RuleGraphLayout | null | undefined): RuleGraph {
    if (!layout || layout.signature !== graphLayoutSignature(graph) || !layout.nodes || !validViewport(layout.viewport) || Object.keys(layout.nodes).length !== graph.nodes.length) return graph
    const paths = graphPathMap(graph)
    const nodes = graph.nodes.map(node => {
        const position = layout.nodes[graphLayoutNodeKey(node, graph, paths)]
        return position && validCoordinate(position.x) && validCoordinate(position.y) ? {
            ...node,
            position: {x: position.x, y: position.y}
        } : null
    })
    if (nodes.some(node => node === null)) return graph
    return {
        ...graph,
        nodes: nodes as RuleGraphNode[],
        viewport: {x: layout.viewport.x, y: layout.viewport.y, zoom: layout.viewport.zoom},
        layoutRestored: true
    }
}

function layoutStorage(): Storage | undefined {
    try {
        return globalThis.localStorage
    } catch {
        return undefined
    }
}

export function restoreGraphLayout(graph: RuleGraph): RuleGraph {
    const ruleId = (graph.nodes.find(node => node.kind === 'rule')?.data as Rule | undefined)?.id
    if (!ruleId) return graph
    const key = `${layoutStoragePrefix}${encodeURIComponent(ruleId)}`
    let layout = memoryLayouts.get(key)
    try {
        const raw = layoutStorage()?.getItem(key)
        if (raw && raw.length <= 524288) layout = JSON.parse(raw) as RuleGraphLayout
    } catch { /* unavailable or malformed storage: use the bounded memory fallback */
    }
    try {
        return applyGraphLayout(graph, layout)
    } catch {
        return graph
    }
}

export function persistGraphLayout(graph: RuleGraph): void {
    const ruleId = (graph.nodes.find(node => node.kind === 'rule')?.data as Rule | undefined)?.id
    if (!ruleId) return
    try {
        const key = `${layoutStoragePrefix}${encodeURIComponent(ruleId)}`, layout = graphLayoutSnapshot(graph)
        memoryLayouts.delete(key);
        memoryLayouts.set(key, layout)
        if (memoryLayouts.size > 50) memoryLayouts.delete(memoryLayouts.keys().next().value!)
        layoutStorage()?.setItem(key, JSON.stringify(layout))
    } catch { /* invalid graph, private mode or quota: memory draft remains usable */
    }
}

const group = (node: RuleGraphNode) => node.kind === 'condition' && ['all', 'any', 'not'].includes((node.data as RuleCondition).op)
export const graphEdge = (source: string, target: string, targetHandle: string, order = 0): RuleGraphEdge => ({
    id: editorId(),
    source,
    target,
    sourceHandle: targetHandle === 'action-in' ? 'action-out' : 'boolean-out',
    targetHandle,
    order
})

export function ruleToGraph(rule: Rule, previous?: RuleGraph): RuleGraph {
    const graph: RuleGraph = {
        nodes: [],
        edges: [],
        viewport: previous?.viewport ?? {x: 0, y: 0, zoom: 1},
        selected: previous?.selected ?? [],
        layoutRestored: !!previous
    }
    const used = new Set<string>()

    function node(kind: RuleGraphNode['kind'], data: RuleGraphNode['data'], path: string, x: number, y: number) {
        const old = previous?.nodes.find(n => !used.has(n.id) && n.kind === kind && (kind === 'action' ? (n.data as RuleAction).id === (data as RuleAction).id : n.path === path))
        const value: RuleGraphNode = {
            id: old?.id ?? editorId(),
            kind,
            data: copy(data),
            path,
            position: old?.position ?? {x, y}
        }
        used.add(value.id);
        graph.nodes.push(value);
        return value
    }

    function condition(value: RuleCondition, path: string, x: number, y: number): {
        node: RuleGraphNode;
        bottom: number
    } {
        const current = node('condition', value, path, x, y)
        let row = y
        if (Array.isArray(value.conditions) && ['all', 'any', 'not'].includes(value.op)) {
            delete (current.data as RuleCondition).conditions
            value.conditions.forEach((child, index) => {
                const next = condition(child, `${path}/conditions/${index}`, x - 360, row)
                graph.edges.push(graphEdge(next.node.id, current.id, 'boolean-in', index))
                row = next.bottom
            })
        }
        return {node: current, bottom: Math.max(y + defaultRowSpacing, row)}
    }

    const root = node('rule', rule, '', 560, 80)
    // Rule metadata is retained exactly; AST fields are rebuilt solely from validated edges.
    const when = condition(rule.when, '/when', 200, 80)
    graph.edges.push(graphEdge(when.node.id, root.id, 'boolean-in'))
    let prior = root, predicateRow = when.bottom
    rule.actions.forEach((action, index) => {
        const x = 920 + index * 360
        const current = node('action', action, `/actions/${index}`, x, 80)
        graph.edges.push(graphEdge(prior.id, current.id, 'action-in', index));
        prior = current
        for (const [key, value] of Object.entries(action.params)) {
            if (value && typeof value === 'object' && typeof (value as RuleCondition).op === 'string' && key === 'predicate' && action.type==='array_filter') {
                const child = condition(value as RuleCondition, `/actions/${index}/params/${key}`, x - 360, predicateRow)
                graph.edges.push(graphEdge(child.node.id, current.id, `predicate:${key}`))
                predicateRow = child.bottom
                delete (current.data as RuleAction).params[key]
            }
        }
    })
    return graph
}

export function graphStructureErrors(graph: RuleGraph, schema: RuleSchema = ruleSchema): RuleFieldError[] {
    const errors: RuleFieldError[] = []
    const nodes = new Map(graph.nodes.map(n => [n.id, n]))
    const error = (id: string, code: string) => errors.push({
        path: nodes.get(id)?.path ?? '',
        code: `graph.${code}`,
        message: code
    })
    if (nodes.size !== graph.nodes.length) error('', 'duplicate')
    if (graph.nodes.filter(n => n.kind === 'rule').length !== 1) error('', 'root')
    const pairs = new Set<string>()
    for (const edge of graph.edges) {
        const from = nodes.get(edge.source), to = nodes.get(edge.target)
        if (!from || !to) {
            error(edge.target, 'endpoint');
            continue
        }
        if (from.id === to.id) error(from.id, 'cycle')
        const booleanPort = edge.sourceHandle === 'boolean-out' && from.kind === 'condition' &&
            (edge.targetHandle === 'boolean-in' && (to.kind === 'rule' || group(to)) || edge.targetHandle === 'predicate:predicate' && to.kind === 'action' && (to.data as RuleAction).type === 'array_filter')
        const actionPort = edge.sourceHandle === 'action-out' && ['rule', 'action'].includes(from.kind) && edge.targetHandle === 'action-in' && to.kind === 'action'
        if (!booleanPort && !actionPort) error(to.id, 'port')
        const pair = `${edge.source}:${edge.target}:${edge.targetHandle}`
        if (pairs.has(pair)) error(to.id, 'duplicate');
        pairs.add(pair)
    }
    for (const node of graph.nodes) {
        const incoming = graph.edges.filter(e => e.target === node.id)
        const outgoing = graph.edges.filter(e => e.source === node.id)
        if (outgoing.length > 1) error(node.id, 'successor')
        if (node.kind === 'action' && incoming.filter(e => e.targetHandle === 'action-in').length > 1) error(node.id, 'predecessor')
        if (node.kind === 'rule' && incoming.length > 1 || node.kind === 'condition' && (node.data as RuleCondition).op === 'not' && incoming.length > 1 || node.kind === 'action' && incoming.filter(e => e.targetHandle.startsWith('predicate:')).length > 1) error(node.id, 'inputCount')
        if (node.kind === 'action') {
            const phase = (graph.nodes.find(n => n.kind === 'rule')?.data as Rule | undefined)?.phase
            const cap = schema.actions.find(c => c.id === (node.data as RuleAction).type)
            if (!cap || phase && cap.phases?.length && !cap.phases.includes(phase)) error(node.id, 'phase')
        }
    }
    const visiting = new Set<string>(), visited = new Set<string>()

    function visit(id: string, depth: number) {
        if (depth > 128) {
            error(id, 'budget');
            return
        }
        if (visiting.has(id)) {
            error(id, 'cycle');
            return
        }
        if (visited.has(id)) return
        visiting.add(id)
        for (const edge of graph.edges.filter(e => e.source === id)) visit(edge.target, depth + 1)
        visiting.delete(id);
        visited.add(id)
    }

    if (graph.nodes.length > 2048) error('', 'budget')
    else graph.nodes.forEach(n => visit(n.id, 0))
    return errors
}

export function graphToRule(graph: RuleGraph, schema: RuleSchema = ruleSchema, validate = true): Rule {
    // Stage compatibility is semantic: retain the user's new phase in the editable
    // AST while strict conversion still prevents publishing incompatible actions.
    const errors = graphStructureErrors(graph, schema).filter(error => validate || error.code !== 'graph.phase')
    if (errors.length) throw new RuleInputError(errors)
    const root = graph.nodes.find(n => n.kind === 'rule')!
    const nodes = new Map(graph.nodes.map(n => [n.id, n]))
    const visited = new Set([root.id])
    const incoming = (id: string, port: string) => graph.edges.filter(e => e.target === id && e.targetHandle === port).sort((a, b) => a.order - b.order)

    function condition(node: RuleGraphNode, path: string): RuleCondition {
        visited.add(node.id)
        const data = copy(node.data) as RuleCondition
        if (group(node)) data.conditions = incoming(node.id, 'boolean-in').map((edge, i) => condition(nodes.get(edge.source)!, `${path}/conditions/${i}`))
        return data
    }

    const when = incoming(root.id, 'boolean-in')[0]
    if (!when) throw new RuleInputError([{path: '/when', code: 'graph.missingCondition', message: 'missingCondition'}])
    const rule = copy(root.data) as Rule
    rule.when = condition(nodes.get(when.source)!, '/when')
    rule.actions = []
    let current = root
    for (; ;) {
        const edge = graph.edges.find(e => e.source === current.id && e.sourceHandle === 'action-out')
        if (!edge) break
        current = nodes.get(edge.target)!;
        visited.add(current.id)
        const action = copy(current.data) as RuleAction
        for (const child of graph.edges.filter(e => e.target === current.id && e.targetHandle.startsWith('predicate:'))) action.params[child.targetHandle.slice(10)] = condition(nodes.get(child.source)!, `/actions/${rule.actions.length}/params/${child.targetHandle.slice(10)}`)
        rule.actions.push(action)
    }
    for (const node of graph.nodes) if (!visited.has(node.id)) errors.push({
        path: node.path,
        code: 'graph.orphan',
        message: 'orphan'
    })
    if (validate) errors.push(...validateRule(rule, schema))
    if (errors.length) throw new RuleInputError(errors)
    return rule
}

export function graphErrors(graph: RuleGraph, schema: RuleSchema = ruleSchema): RuleFieldError[] {
    try {
        graphToRule(graph, schema);
        return []
    } catch (error) {
        return error instanceof RuleInputError ? error.errors : [{
            path: '',
            code: 'graph.invalid',
            message: String(error)
        }]
    }
}

export function connectGraph(graph: RuleGraph, edge: Omit<RuleGraphEdge, 'id' | 'order'>, schema: RuleSchema = ruleSchema): RuleGraph {
    const next = copy(graph)
    next.edges.push({
        ...edge,
        id: editorId(),
        order: Math.max(-1, ...next.edges.filter(e => e.target === edge.target && e.targetHandle === edge.targetHandle).map(e => e.order)) + 1
    })
    const errors = graphStructureErrors(next, schema)
    if (errors.length) throw new RuleInputError(errors)
    return next
}

// Work on graph fragments, never rebuild the existing tree/chain from the root's stale AST.
export function addGraphModule(graph: RuleGraph, options: RuleGraphAddOptions, schema: RuleSchema = ruleSchema): RuleGraph {
    const errors = graphStructureErrors(graph, schema)
    if (errors.length) throw new RuleInputError(errors)
    const next = copy(graph), root = next.nodes.find(node => node.kind === 'rule')!
    const rule = root.data as Rule, mode = options.mode ?? 'auto'
    const fail = (code: string, node = root): never => {
        throw new RuleInputError([{path: node.path, code: `graph.${code}`, message: code}])
    }
    if (!['auto', 'detached', 'wrap'].includes(mode)) fail('mode')
    if (!['condition', 'action'].includes(options.kind)) fail('capability')
    const capability = (options.kind === 'action' ? schema.actions : schema.conditions).find(cap => cap.id === options.type)
    if (!capability) fail('capability')
    if (capability?.phases?.length && !capability.phases.includes(rule.phase)) fail('phase')
    if (options.position && (!validCoordinate(options.position.x) || !validCoordinate(options.position.y))) fail('position')
    if (mode === 'wrap' && (options.kind !== 'condition' || !['all', 'any', 'not'].includes(options.type))) fail('mode')
    const paths = graphPathMap(next)
    const explicitTarget = options.targetId === undefined ? undefined : next.nodes.find(node => node.id === options.targetId)
    if (options.targetId !== undefined && !explicitTarget) fail('target')
    // Detached creation has no connection target; auto/wrap may never attach to an orphan.
    if (mode !== 'detached' && explicitTarget && paths[explicitTarget.id] === undefined) fail('orphan', explicitTarget)
    const children = (node: RuleGraphNode, port = 'boolean-in') => next.edges.filter(edge => edge.target === node.id && edge.targetHandle === port).sort((a, b) => a.order - b.order)
    const parentEdge = (node: RuleGraphNode) => {
        const edges = next.edges.filter(edge => edge.source === node.id && edge.sourceHandle === 'boolean-out')
        if (edges.length !== 1) fail('target', node)
        return edges[0]!
    }
    const freePosition = (position: RuleGraphNode['position']) => {
        const result = {...position}
        while (next.nodes.some(node => Math.abs(node.position.x - result.x) < 360 && Math.abs(node.position.y - result.y) < 240)) result.y += 240
        return result
    }
    const positionFor = (position: RuleGraphNode['position']) => options.position ? {...options.position} : freePosition(position)
    const addFragment = (kind: RuleGraphAddOptions['kind'], value: RuleCondition | RuleAction, position: RuleGraphNode['position']) => {
        const fragment = ruleToGraph(kind === 'condition' ? {
            ...rule,
            when: value as RuleCondition,
            actions: []
        } : {...rule, when: {op: 'always'}, actions: [value as RuleAction]})
        const base = kind === 'condition' ? '/when' : '/actions/0'
        const included = fragment.nodes.filter(node => node.path === base || node.path.startsWith(`${base}/`))
        const ids = new Set(included.map(node => node.id)), main = included.find(node => node.path === base)!
        const edges = fragment.edges.filter(edge => ids.has(edge.source) && ids.has(edge.target))
        const arrange = (node: RuleGraphNode, x: number, y: number): number => {
            // Keep an explicit main position exact; generated descendants avoid existing cards too.
            node.position = node === main ? {x, y} : freePosition({x, y})
            let row = node.position.y + (node.kind === 'action' ? 240 : 0)
            for (const edge of edges.filter(edge => edge.target === node.id).sort((a, b) => a.order - b.order)) row = arrange(included.find(child => child.id === edge.source)!, x - 360, row)
            return Math.max(node.position.y + 240, row)
        }
        arrange(main, position.x, position.y)
        next.nodes.push(...included);
        next.edges.push(...edges)
        return main
    }
    const value = options.kind === 'action' ? newAction(options.type, schema) : defaultCondition(options.type, schema)
    const addCondition = (position: RuleGraphNode['position']) => addFragment('condition', value, positionFor(position))
    const finish = (node: RuleGraphNode) => {
        const errors = graphStructureErrors(next, schema)
        if (errors.length) throw new RuleInputError(errors)
        const paths = graphPathMap(next)
        for (const node of next.nodes) if (paths[node.id] !== undefined) node.path = paths[node.id]!
        next.selected = [node.id]
        return next
    }
    if (mode === 'detached') return finish(addFragment(options.kind, value, positionFor({
        x: root.position.x + (options.kind === 'action' ? 360 : -360),
        y: root.position.y + 240
    })))
    if (options.kind === 'action') {
        let target = explicitTarget ?? root
        if (target.kind !== 'rule' && target.kind !== 'action') fail('target', target)
        if (!explicitTarget) {
            // Multiple disconnected action chains have no unambiguous append destination.
            if (next.nodes.some(node => node.kind === 'action' && paths[node.id] === undefined)) fail('orphan')
            for (; ;) {
                const edge = next.edges.find(edge => edge.source === target.id && edge.sourceHandle === 'action-out')
                if (!edge) break
                target = next.nodes.find(node => node.id === edge.target)!
            }
        }
        const successor = next.edges.find(edge => edge.source === target.id && edge.sourceHandle === 'action-out')
        const node = addFragment('action', value, positionFor({x: target.position.x + 360, y: target.position.y}))
        const order = successor?.order ?? (children(target, 'action-in')[0]?.order ?? -1) + 1
        if (successor) successor.source = node.id
        next.edges.push(graphEdge(target.id, node.id, 'action-in', order))
        return finish(node)
    }
    let target = explicitTarget ?? root
    if (mode === 'wrap') {
        if (target.kind === 'rule') {
            const edge = children(target)[0]
            if (!edge) fail('missingCondition', target)
            target = next.nodes.find(node => node.id === edge.source)!
        }
        if (target.kind !== 'condition') fail('target', target)
        const parent = parentEdge(target)
        // A wrapper takes the existing subtree, not defaultCondition's placeholder child.
        const wrapper = addFragment('condition', {
            ...value,
            conditions: []
        } as RuleCondition, positionFor({x: target.position.x + 360, y: target.position.y}))
        parent.source = wrapper.id
        next.edges.push(graphEdge(target.id, wrapper.id, 'boolean-in'))
        return finish(wrapper)
    }
    if (target.kind === 'condition' && (['all', 'any'].includes((target.data as RuleCondition).op) || (target.data as RuleCondition).op === 'not' && !children(target).length)) {
        const incoming = children(target)
        const node = addCondition({x: target.position.x - 360, y: target.position.y + incoming.length * 240})
        next.edges.push(graphEdge(node.id, target.id, 'boolean-in', Math.max(-1, ...incoming.map(edge => edge.order)) + 1))
        return finish(node)
    }
    if (target.kind !== 'condition') {
        const port = target.kind === 'rule' ? 'boolean-in' : (target.data as RuleAction).type === 'array_filter' ? 'predicate:predicate' : fail('target', target)
        const existing = children(target, port)[0]
        if (!existing) {
            const node = addCondition({
                x: target.position.x - 360,
                y: target.position.y + (target.kind === 'action' ? 240 : 0)
            })
            next.edges.push(graphEdge(node.id, target.id, port))
            return finish(node)
        }
        target = next.nodes.find(node => node.id === existing.source)!
        if ((target.data as RuleCondition).op === 'always') {
            const node = addFragment('condition', value, options.position ? {...options.position} : {...target.position})
            const generatedId = node.id
            node.id = target.id
            for (const edge of next.edges) {
                if (edge.source === generatedId) edge.source = node.id
                if (edge.target === generatedId) edge.target = node.id
            }
            next.nodes = next.nodes.filter(old => old !== target)
            return finish(node)
        }
    }
    // Adding beside a leaf/not must not change its subtree or turn NOT(A) into NOT(A AND B).
    const parent = parentEdge(target)
    if (!schema.conditions.some(cap => cap.id === 'all')) fail('capability')
    const node = addCondition({x: target.position.x, y: target.position.y + 240})
    const wrapper = addFragment('condition', {
        ...defaultCondition('all', schema),
        conditions: []
    }, freePosition({x: target.position.x + 360, y: target.position.y}))
    parent.source = wrapper.id
    next.edges.push(graphEdge(target.id, wrapper.id, 'boolean-in', 0), graphEdge(node.id, wrapper.id, 'boolean-in', 1))
    return finish(node)
}

// Coordinates, viewport, editor IDs and selection never affect wire identity or dirty state.
export function graphSignature(graph: RuleGraph): string {
    try {
        return JSON.stringify(graphToRule(graph, ruleSchema, false))
    } catch { /* retain invalid drafts, including all orphan nodes */
    }
    return JSON.stringify({
        nodes: graph.nodes.map(n => ({
            id: n.id,
            kind: n.kind,
            data: n.data
        })).sort((a, b) => a.id.localeCompare(b.id)),
        edges: graph.edges.map(({source, target, sourceHandle, targetHandle, order}) => ({
            source,
            target,
            sourceHandle,
            targetHandle,
            order
        })).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)))
    })
}

export function graphNodeValue(graph: RuleGraph, id: string, seen = new Set<string>()): RuleGraphNode['data'] {
    const node = graph.nodes.find(n => n.id === id)!
    const data = copy(node.data)
    if (seen.has(id)) return data
    seen.add(id)
    if (group(node)) (data as RuleCondition).conditions = graph.edges.filter(e => e.target === id && e.targetHandle === 'boolean-in').sort((a, b) => a.order - b.order).map(e => graphNodeValue(graph, e.source, new Set(seen)) as RuleCondition)
    if (node.kind === 'action') for (const edge of graph.edges.filter(e => e.target === id && e.targetHandle.startsWith('predicate:'))) (data as RuleAction).params[edge.targetHandle.slice(10)] = graphNodeValue(graph, edge.source, new Set(seen))
    return data
}

export function replaceGraphNode(graph: RuleGraph, id: string, data: RuleGraphNode['data']): RuleGraph {
    const next = copy(graph), node = next.nodes.find(n => n.id === id)!
    if (node.kind === 'rule') {
        node.data = copy(data);
        return next
    }
    const removed = new Set<string>()
    const collect = (parent: string) => {
        for (const edge of next.edges.filter(e => e.target === parent && e.sourceHandle === 'boolean-out')) if (!removed.has(edge.source)) {
            removed.add(edge.source);
            collect(edge.source)
        }
    }
    collect(id)
    const rule = next.nodes.find(n => n.kind === 'rule')!.data as Rule
    const paths = graphPathMap(graph), basePath = paths[id] ?? node.path
    const fragmentPath = node.kind === 'condition' ? '/when' : '/actions/0'
    const previous = {
        ...graph,
        nodes: graph.nodes.filter(n => n.id === id || removed.has(n.id)).map(n => ({
            ...n,
            path: fragmentPath + (paths[n.id] ?? n.path).slice(basePath.length)
        }))
    }
    const fragment = ruleToGraph(node.kind === 'condition' ? {
        ...rule,
        when: data as RuleCondition,
        actions: []
    } : {...rule, when: {op: 'always'}, actions: [data as RuleAction]}, previous)
    const source = fragment.nodes.find(n => node.kind === 'condition' ? n.path === '/when' : n.kind === 'action')!
    const included = fragment.nodes.filter(n => node.kind === 'condition' ? n.path.startsWith('/when') : n.path.startsWith('/actions/0'))
    const allowed = new Set(included.map(n => n.id))
    const remap = new Map(included.map(n => [n.id, n.id === source.id ? id : next.nodes.some(old => old.id === n.id && !removed.has(old.id)) ? editorId() : n.id]))
    const replacements = new Map(included.map(n => {
        const replacement = {...n, id: remap.get(n.id)!, position: n.id === source.id ? node.position : n.position}
        return [replacement.id, replacement]
    }))
    // Reuse original array slots so editing a focused inline control does not move its DOM node.
    next.nodes = next.nodes.flatMap(old => {
        const replacement = replacements.get(old.id)
        if (replacement) {
            replacements.delete(old.id);
            return [replacement]
        }
        return old.id === id || removed.has(old.id) ? [] : [old]
    })
    next.nodes.push(...replacements.values())
    next.edges = next.edges.filter(e => !removed.has(e.source) && !removed.has(e.target))
    next.edges.push(...fragment.edges.filter(e => allowed.has(e.source) && allowed.has(e.target)).map(e => ({
        ...e,
        source: remap.get(e.source)!,
        target: remap.get(e.target)!
    })))
    return next
}

export function graphPathMap(graph: RuleGraph): Record<string, string> {
    const result: Record<string, string> = {}
    const root = graph.nodes.find(n => n.kind === 'rule')
    if (!root) return result
    const visited = new Set<string>()
    const mapCondition = (id: string, path: string) => {
        if (visited.has(id)) return
        visited.add(id);
        result[id] = path
        graph.edges.filter(e => e.target === id && e.targetHandle === 'boolean-in').sort((a, b) => a.order - b.order).forEach((e, i) => mapCondition(e.source, `${path}/conditions/${i}`))
    }
    result[root.id] = ''
    const when = graph.edges.find(e => e.target === root.id && e.targetHandle === 'boolean-in')
    if (when) mapCondition(when.source, '/when')
    let id = root.id, index = 0
    while (!visited.has(id)) {
        visited.add(id)
        const edge = graph.edges.find(e => e.source === id && e.sourceHandle === 'action-out')
        if (!edge) break
        id = edge.target;
        const path = `/actions/${index++}`;
        result[id] = path
        graph.edges.filter(e => e.target === id && e.targetHandle.startsWith('predicate:')).forEach(e => mapCondition(e.source, `${path}/params/${e.targetHandle.slice(10)}`))
    }
    return result
}
