import { defineConfig } from "vite";

// The Tauri runtime imports (@tauri-apps/*) resolve inside the app; keep them
// external so a plain `vite build` for the web bundle never fails on them.
export default defineConfig({
  root: "src",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    rollupOptions: {
      external: [/^@tauri-apps\//],
    },
  },
  server: { port: 5173, strictPort: true },
  clearScreen: false,
});
