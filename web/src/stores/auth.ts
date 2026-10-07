import {defineStore} from 'pinia'
import {ref} from 'vue'
import {api, ApiError, clearToken, getToken, setToken} from '../api/client'

export const useAuthStore = defineStore('auth', () => {
    const token = ref(getToken())
    const username = ref('')
    const ready = ref(false)

    async function restore() {
        const restoringToken = token.value
        if (!token.value) {
            ready.value = true
            return
        }
        try {
            const me = await api.me()
            if (token.value === restoringToken) username.value = me.username
        } catch (err) {
            if (err instanceof ApiError && err.status === 401 && token.value === restoringToken) {
                token.value = ''
                username.value = ''
                clearToken()
            }
        } finally {
            ready.value = true
        }
    }

    async function signIn(user: string, password: string) {
        const result = await api.login(user, password)
        token.value = result.token
        username.value = result.username
        setToken(result.token)
        ready.value = true
    }

    async function signOut() {
        try {
            await api.logout()
        } catch {
            // Signing out locally is enough even if the request fails.
        }
        token.value = ''
        username.value = ''
        clearToken()
    }

    return {token, username, ready, restore, signIn, signOut}
})
