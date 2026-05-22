import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// Dev: proxy /api/* to the natively-running brain (WEB_UI.md dev loop). SSE
// streams pass through the same proxy. In production Caddy does this routing.
const BRAIN = process.env.MALMO_BRAIN ?? "http://localhost:8080";

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: BRAIN,
        changeOrigin: true,
        // SSE: disable buffering so events arrive promptly.
        configure: (proxy) => {
          proxy.on("proxyReq", (proxyReq) => proxyReq.setHeader("X-Forwarded-By", "vite"));
        },
      },
    },
  },
});
