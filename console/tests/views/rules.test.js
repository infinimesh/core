import { describe, expect, test, vi } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createPinia } from "pinia"
import { ref } from "vue"
import Rules from "@/views/dashboard/Rules.vue"

const calls = []

vi.mock("naive-ui", async () => {
  const actual = await vi.importActual("naive-ui")
  return {
    ...actual,
    useMessage: () => ({ error() {}, success() {} }),
  }
})

vi.mock("@/store/namespaces", () => ({
  useNSStore: () => ({
    namespaces_list: ref([{ uuid: "ns-1", title: "One" }]),
    fetchNamespaces() {},
  }),
}))

function http() {
  return {
    get(url, cfg) {
      calls.push({ method: "get", url, params: cfg && cfg.params })
      if (url === "/v1/rules") {
        return Promise.resolve({ data: { rules: [{ uuid: "rule-1", name: "Hot", when: "true" }] } })
      }
      return Promise.resolve({ data: { alarms: [] } })
    },
    delete(url) {
      calls.push({ method: "delete", url })
      return Promise.resolve({})
    },
    post() {
      return Promise.resolve({})
    },
    patch() {
      return Promise.resolve({})
    },
    interceptors: { response: { use() {} } },
  }
}

describe("Rules page", () => {
  test("loads alarms with any_state and deletes a rule", async () => {
    calls.length = 0
    const wrapper = mount(Rules, {
      global: {
        plugins: [createPinia()],
        provide: {
          axios: { create: () => http() },
        },
      },
    })
    await wrapper.vm.$.appContext.app.runWithContext(async () => {
      wrapper.vm.namespace = "ns-1"
      await wrapper.vm.load()
    })
    await flushPromises()

    expect(calls).toContainEqual({
      method: "get",
      url: "/v1/alarms",
      params: { namespace: "ns-1", any_state: true },
    })

    const del = wrapper.findAll("button").find((button) => button.text() === "Delete")
    expect(del).toBeTruthy()
    await wrapper.vm.$.appContext.app.runWithContext(() => wrapper.vm.remove(wrapper.vm.rules[0]))
    await flushPromises()
    expect(calls).toContainEqual({ method: "delete", url: "/v1/rules/rule-1" })
  })
})
