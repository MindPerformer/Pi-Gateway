<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'
import { useAuthStore } from './stores/auth'
import ToastHost from './components/ToastHost.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
watch(() => auth.token, token => {
	if (!token && !route.meta.public) void router.replace({name: 'login', query: {redirect: route.fullPath}})
})

onMounted(() => {
	// Validate a persisted token before the shell renders protected routes.
	void auth.restore()
})
</script>

<template>
	<RouterView />
	<ToastHost />
</template>
