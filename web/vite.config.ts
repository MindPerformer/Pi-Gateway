import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// The build output is embedded into the Go binary via internal/webui.
export default defineConfig({
    plugins: [vue(), tailwindcss()],
    build: {
        outDir: '../internal/webui/dist',
        emptyOutDir: true,
        chunkSizeWarningLimit: 1200,
    },
    server: {
        port: 5273,
        // During development, proxy the API to a locally running gateway.
        proxy: {
            '/api': {target: 'http://127.0.0.1:8317', changeOrigin: true},
            '/v1': {target: 'http://127.0.0.1:8317', changeOrigin: true},
            '/healthz': {target: 'http://127.0.0.1:8317', changeOrigin: true},
        },
    },
})
