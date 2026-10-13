import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import path from "path";
import svgLoader from "vite-svg-loader";

// https://vitejs.dev/config/
export default defineConfig(() => {
  let conf = {
    plugins: [vue(), svgLoader()],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    define: {
      STATE_MAX_ROWS: "10",
      "import.meta.env.VITE_INFINIMESH_VERSION_TAG": JSON.stringify(process.env.INFINIMESH_VERSION_TAG || "development"),
    },
    build: {
      chunkSizeWarningLimit: 2000,
    },
  };

  return conf;
});
