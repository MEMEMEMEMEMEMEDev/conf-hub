import { defineConfig } from "astro/config";
import react from "@astrojs/react";

// Estático: la plataforma sirve este build en `/` y rutea `/api` al hub
// (contrato de conf, D-02). El front siempre le pide a su propio origen y
// nunca sabe dónde vive el hub.
export default defineConfig({
  site: "https://conf.aaroidev.com",
  output: "static",
  trailingSlash: "ignore",
  integrations: [react()],
  vite: {
    server: {
      // SOLO en desarrollo: el hub corre aparte (go run -tags dev, :18080).
      // `ws: true` porque /api/salas/{id}/audio es WebSocket.
      proxy: {
        "/api": { target: process.env.HUB_DEV ?? "http://localhost:18080", changeOrigin: false, ws: true },
      },
    },
  },
});
