<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Rule, RuleSchema } from '../../api/rules'
import { useI18n } from '../../i18n'
import { localHelp } from '../../utils/ruleSchema'
const props = defineProps<{ schema: RuleSchema }>()
const emit = defineEmits<{ example: [rule: Rule] }>()
const { t, locale } = useI18n()
const search = ref('')
const entries = computed(() => [
  { id: 'rule', description: {en: 'Rule metadata, policy and execution order.', 'zh-CN': '规则元数据、策略与执行顺序。'}, fields: props.schema.rule_fields },
  ...props.schema.conditions, ...props.schema.actions, ...(props.schema.value_expressions ?? []),
  { id: 'context', description: { en: 'Available context facts. Request rules cannot read response-only fields.', 'zh-CN': '可用上下文事实；请求规则不能引用响应专用字段。' }, fields: props.schema.context_fields ?? [] },
].filter(c => !search.value || `${c.id} ${localHelp(c.description, locale.value)} ${c.fields.map(f => `${f.name} ${localHelp(f.description, locale.value)} ${f.enum?.join(' ')}`).join(' ')}`.toLowerCase().includes(search.value.toLowerCase())))
</script>
<template>
  <details class="rounded-lg border border-[color:var(--color-line)] p-3">
    <summary class="cursor-pointer text-sm font-medium">{{ t('rules.help') }}</summary>
    <div class="mt-3 space-y-3">
      <p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.safety') }}</p>
      <p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.pointerHelp') }}</p>
      <p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.regexHelp') }}</p>
      <p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.sourceHelp') }}</p>
      <p class="text-xs text-[color:var(--color-ink-muted)]">{{ t('rules.policyHelp') }}</p>
      <input v-model="search" class="input" :placeholder="t('rules.helpSearch')" :aria-label="t('rules.helpSearch')" />
      <div class="max-h-[32rem] space-y-2 overflow-auto">
        <details v-for="cap in entries" :key="cap.id" class="rounded border border-[color:var(--color-line)] p-2">
          <summary class="cursor-pointer font-mono text-sm">{{ cap.id }}</summary>
          <p class="my-2 text-xs">{{ localHelp(cap.description, locale) }}</p>
          <p v-if="'phases' in cap" class="text-xs">{{ t('rules.phases') }}: {{ cap.phases?.join(' / ') }}</p>
          <dl class="space-y-3 py-2">
            <div v-for="field in cap.fields" :key="field.name" class="border-t border-[color:var(--color-line)] pt-2 text-xs">
              <dt class="font-mono font-medium">{{ field.name }} · {{ field.type }} · {{ field.required ? t('rules.required') : t('rules.optional') }}</dt>
              <dd class="mt-1 text-[color:var(--color-ink-muted)]">{{ localHelp(field.description, locale) }}</dd>
              <dd>{{ t('rules.default') }}: <code>{{ field.default === undefined ? t('rules.none') : JSON.stringify(field.default) }}</code></dd>
              <dd v-if="field.enum">{{ t('rules.options') }}: {{ field.enum.join(', ') }}</dd>
              <dd v-if="field.min !== undefined">{{ t('rules.minimum') }}: {{ field.min }}</dd><dd v-if="field.max !== undefined">{{ t('rules.maximum') }}: {{ field.max }}</dd>
              <dd v-for="(example, i) in field.examples" :key="i">{{ t('rules.examples') }}: <code>{{ JSON.stringify(example) }}</code></dd>
            </div>
          </dl>
        </details>
      </div>
      <div v-if="schema.examples?.length" class="flex flex-wrap gap-2"><button v-for="(example, i) in schema.examples" :key="i" class="btn" type="button" @click="emit('example', example)">{{ t('rules.loadExample') }}: {{ example.name }}</button></div>
    </div>
  </details>
</template>
