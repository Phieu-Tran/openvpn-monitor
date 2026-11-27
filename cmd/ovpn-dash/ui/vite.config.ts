import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "./", // relative URLs, so a reverse proxy may mount the page under a path
  // ../web is embedded into the Go binary. assetsInlineLimit 0: the CSP has no data: fonts.
  build: { outDir: "../web", emptyOutDir: true, assetsInlineLimit: 0, chunkSizeWarningLimit: 1000 },
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
});
