import { defineConfig } from "vitest/config";
import solid from "@solidjs/vite-plugin";
import wails from "@wailsio/runtime/plugins/vite";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [solid(), wails("./bindings")],
  // The Solid plugin asks for jsdom; what is tested here is arithmetic. CSS is
  // processed because the theme test reads styles.css as text, and without it
  // a `?raw` import of a stylesheet comes back empty.
  test: { environment: "node", css: true },
});
