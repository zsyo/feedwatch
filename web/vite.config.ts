import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The output directory is supplied by web/build.sh (--outDir). Keeping it out of
// the config lets the same build feed both the local embedded/ directory and the
// Docker Node stage's /web/out staging area.
export default defineConfig({
  plugins: [vue()],
  build: {
    target: 'es2022',
    assetsDir: 'assets',
  },
})
