<script setup lang="ts">
// Layout mirrors the deployed @codex-proxy/ui BasePageHeader.
// Retain gateway subtitle/actions/default compatibility.
defineProps<{
	title: string
	subtitle?: string
}>()
</script>

<template>
	<header class="page-header">
		<div class="page-heading">
			<h1 class="page-title">{{ title }}</h1>
			<p v-if="subtitle" class="page-description">{{ subtitle }}</p>
		</div>
		<div v-if="$slots.actions || $slots.default" class="page-actions">
			<slot name="actions" />
			<slot />
		</div>
	</header>
</template>
