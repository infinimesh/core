import { beforeEach, describe, expect, test } from "vitest"
import { createPinia, setActivePinia } from "pinia"

describe("rules store", () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  test("client is the v1 RulesService", async () => {
    const { useAppStore } = await import("@/store/app")
    useAppStore()
    const { useRulesStore } = await import("@/store/rules")
    const { RulesService } = await import("infinimesh-proto/build/es/node/node_connect")
    const store = useRulesStore()
    expect(RulesService.typeName).toBe("infinimesh.node.v1.RulesService")
    expect(typeof store.client.create).toBe("function")
    expect(typeof store.client.update).toBe("function")
    expect(typeof store.client.list).toBe("function")
    expect(typeof store.client.listAlarms).toBe("function")
    expect(typeof store.client.ackAlarm).toBe("function")
    expect(typeof store.client.clearAlarm).toBe("function")
    expect(typeof store.client.delete).toBe("function")
  })
})
