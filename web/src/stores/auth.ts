import {defineStore} from 'pinia'
import {ref} from 'vue'
import {api, clearToken, getToken, setToken} from '../api/client'

export const useAuthStore = defineStore('auth', () => {
    const token = ref(getToken())
    const username = ref('')
    const ready = ref(false)

    async function restore() {
        if (!token.value) {
            ready.value = true
            return
        }
        try {
            const me = await api.me()
            username.value = me.username
        } catch {
            signOut()
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
