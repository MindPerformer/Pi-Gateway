<script setup lang="ts">
import { useToastStore } from '../stores/ui'
import { CircleAlert, CircleCheck, Info, X } from 'lucide-vue-next'
import { useI18n } from '../i18n'
const { t } = useI18n()

const store = useToastStore()

const iconFor = (kind: string) => {
	if (kind === 'error') return CircleAlert
	if (kind === 'success') return CircleCheck
	return Info
}

const toneFor = (kind: string) => {
	if (kind === 'error') return 'bg-cp-error-container text-cp-error'
	if (kind === 'success') return 'bg-cp-success-container text-cp-success'
	return 'bg-cp-info-container text-cp-info'
}
</script>

<template>
	<div class="toast-host" aria-live="polite" aria-relevant="additions">
		<TransitionGroup name="toast">
			<div
				v-for="toast in store.toasts"
				:key="toast.id"
				class="toast-message"
				:role="toast.kind === 'error' ? 'alert' : 'status'"
			>
				<span class="inline-flex h-[34px] w-[34px] shrink-0 items-center justify-center rounded-cp" :class="toneFor(toast.kind)"><component :is="iconFor(toast.kind)" class="h-[18px] w-[18px]" /></span>
				<p class="min-w-0 flex-1 text-cp leading-snug font-semibold break-words">{{ toast.message }}</p>
				<button class="btn btn-ghost btn-icon btn-sm text-cp-text-quaternary" :aria-label="t('common.close')" @click="store.dismiss(toast.id)">
					<X class="h-4 w-4" />
				</button>
			</div>
		</TransitionGroup>
	</div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
	transition: all 180ms ease;
}
.toast-enter-from,
.toast-leave-to {
	opacity: 0;
	transform: translateY(6px);
}
</style>
