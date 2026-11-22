import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  base: "./",
  plugins: [svelte()],
  build: {
    target: "es2022",
    assetsDir: "",
    sourcemap: false,
    cssCodeSplit: true,
    rollupOptions: {
      output: {
        entryFileNames: "app-[hash].js",
        chunkFileNames: "chunk-[name]-[hash].js",
        assetFileNames: "asset-[name]-[hash][extname]",
      },
    },
  },
});
