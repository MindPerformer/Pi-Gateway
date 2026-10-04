<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterView } from 'vue-router'
import { useAuthStore } from './stores/auth'
import ToastHost from './components/ToastHost.vue'

const auth = useAuthStore()

onMounted(() => {
	// Validate a persisted token before the shell renders protected routes.
	void auth.restore()
})
</script>

<template>
	<RouterView />
	<ToastHost />
</template>
