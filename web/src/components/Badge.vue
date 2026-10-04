<script setup lang="ts">
// Keep the public tone API; all fills/on-container colors map to upstream tokens.
withDefaults(defineProps<{
	tone?: 'neutral' | 'success' | 'danger' | 'warn' | 'info' | 'accent'
	mono?: boolean
}>(), { tone: 'neutral', mono: false })
</script>

<template>
	<span class="badge" :class="[`badge-${tone}`, mono && 'font-mono']"><slot /></span>
</template>
