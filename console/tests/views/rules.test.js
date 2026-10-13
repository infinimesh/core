import { describe, expect, test, vi } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createPinia } from "pinia"
import { ref } from "vue"
import Rules from "@/views/dashboard/Rules.vue"

const selected = ref("ns-1")

const calls = []

const client = {
  list() {
    calls.push({ method: "list" })
    return Promise.resolve({ rules: [{ uuid: "rule-1", name: "Hot", when: "true" }] })
  },
  listAlarms(req) {
    calls.push({ method: "listAlarms", req })
    return Promise.resolve({ alarms: [{ rule: "rule-uuid", title: "Hot", device: "dev-1", state: 0 }] })
  },
  create(body) {
    calls.push({ method: "create", body })
    return Promise.resolve({})
  },
  update(body) {
    calls.push({ method: "update", body })
    return Promise.resolve({})
  },
  delete(body) {
    calls.push({ method: "delete", body })
    return Promise.resolve({})
  },
  ackAlarm(body) {
    calls.push({ method: "ackAlarm", body })
    return Promise.resolve({})
  },
  clearAlarm(body) {
    calls.push({ method: "clearAlarm", body })
    return Promise.resolve({})
  },
}

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
    selected,
    fetchNamespaces() {},
  }),
}))

vi.mock("@/store/rules", () => ({
  useRulesStore: () => ({ client }),
}))

describe("Rules page", () => {
  test("loads rules and alarms without a refresh click", async () => {
    selected.value = "ns-1"
    calls.length = 0
    mount(Rules, { global: { plugins: [createPinia()] } })
    await flushPromises()
    expect(calls.map((call) => call.method)).toEqual(expect.arrayContaining(["list", "listAlarms"]))
  })

  test("starts on the header namespace", async () => {
    selected.value = "ns-1"
    const wrapper = mount(Rules, {
      global: { plugins: [createPinia()] },
    })
    expect(wrapper.vm.namespace).toBe("ns-1")
    selected.value = ""
    const empty = mount(Rules, {
      global: { plugins: [createPinia()] },
    })
    expect(empty.vm.namespace).toBe("")
    selected.value = "ns-1"
    await flushPromises()
    expect(empty.vm.namespace).toBe("ns-1")
    selected.value = "all"
    const all = mount(Rules, {
      global: { plugins: [createPinia()] },
    })
    expect(all.vm.namespace).toBe("")
  })

  test("lists, creates, and deletes through RulesService", async () => {
    selected.value = "ns-1"
    calls.length = 0
    const wrapper = mount(Rules, {
      global: { plugins: [createPinia()] },
    })
    await wrapper.vm.$.appContext.app.runWithContext(async () => {
      wrapper.vm.namespace = "ns-1"
      wrapper.vm.form = { uuid: "", name: "Hot", when: "reported.temp > 70", clear: "", cooldown: "", webhook: "" }
      await wrapper.vm.save()
    })
    await flushPromises()

    expect(calls).toContainEqual({
      method: "create",
      body: {
        uuid: undefined,
        namespace: "ns-1",
        name: "Hot",
        when: "reported.temp > 70",
        clear: "",
        cooldown: "",
        actions: [{ log: { message: "alarm" } }],
      },
    })
    expect(calls).toContainEqual({
      method: "listAlarms",
      req: { namespace: "ns-1", anyState: true },
    })

    const del = wrapper.findAll("button").find((button) => button.text() === "Delete")
    expect(del).toBeTruthy()
    const pop = wrapper.findComponent({ name: "Popconfirm" })
    expect(pop.exists()).toBe(true)
    expect(pop.vm.$slots.default()[0].children).toContain("Delete this rule?")
    await wrapper.vm.$.appContext.app.runWithContext(() => wrapper.vm.remove(wrapper.vm.rules[0]))
    await flushPromises()
    expect(calls).toContainEqual({ method: "delete", body: { uuid: "rule-1" } })
    expect(wrapper.text()).toContain("Hot")
  })
})
