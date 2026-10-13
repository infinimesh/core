import { defineStore } from "pinia"
import { createPromiseClient } from "@connectrpc/connect"
import { createConnectTransport } from "@connectrpc/connect-web"
import { RulesService } from "infinimesh-proto/build/es/node/node_connect"
import { useAppStore } from "@/store/app"

const app = useAppStore()

export const useRulesStore = defineStore("rules", {
  state: () => ({
    client: createPromiseClient(
      RulesService,
      createConnectTransport(app.transport_options)
    ),
  }),
})
