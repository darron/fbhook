import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist', emptyOutDir: true,
    rollupOptions: {
      input: 'src/main.ts',
      output: { entryFileNames: 'app.js', chunkFileNames: 'chunks/[name]-[hash].js', assetFileNames: info => info.name?.endsWith('.css') ? 'app.css' : '[name]-[hash][extname]' }
    }
  }
});
