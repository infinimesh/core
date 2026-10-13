<template>
  <n-spin :show="loading">
    <n-h1 prefix="bar">Rules and Alarms</n-h1>
    <n-space>
      <n-select v-model:value="namespace" :options="namespaceOptions" placeholder="Namespace" style="width: 280px" />
      <n-button @click="load" :disabled="!namespace">Refresh</n-button>
    </n-space>
    <n-h2>Rule</n-h2>
    <n-form>
      <n-form-item label="Name"><n-input v-model:value="form.name" /></n-form-item>
      <n-form-item label="When"><n-input v-model:value="form.when" placeholder='reported.t == "hot"' /></n-form-item>
      <n-form-item label="Clear"><n-input v-model:value="form.clear" /></n-form-item>
      <n-form-item label="Cooldown"><n-input v-model:value="form.cooldown" placeholder="10m" /></n-form-item>
      <n-form-item label="Webhook"><n-input v-model:value="form.webhook" /></n-form-item>
      <n-button type="primary" @click="save" :disabled="!namespace || !form.name || !form.when">Save</n-button>
    </n-form>
    <n-table style="margin-top: 16px">
      <n-thead>
        <n-tr><th>Name</th><th>When</th><th></th></n-tr>
      </n-thead>
      <tbody>
        <n-tr v-for="rule in rules" :key="rule.uuid">
          <td>{{ rule.name }}</td>
          <td>{{ rule.when }}</td>
          <td>
            <n-button size="small" @click="edit(rule)">Edit</n-button>
            <n-button size="small" @click="remove(rule)">Delete</n-button>
          </td>
        </n-tr>
      </tbody>
    </n-table>
    <n-h2>Alarms</n-h2>
    <n-table>
      <n-thead>
        <n-tr><th>Rule</th><th>Device</th><th>State</th><th></th></n-tr>
      </n-thead>
      <tbody>
        <n-tr v-for="alarm in alarms" :key="alarm.rule + alarm.device">
          <td>{{ alarm.title || alarm.rule }}</td>
          <td>{{ alarm.device }}</td>
          <td>{{ alarmState(alarm.state) }}</td>
          <td>
            <n-button size="small" @click="act(alarm, 'ack')">Ack</n-button>
            <n-button size="small" @click="act(alarm, 'clear')">Clear</n-button>
          </td>
        </n-tr>
      </tbody>
    </n-table>
  </n-spin>
</template>

<script setup>
import { computed, ref } from "vue"
import { NSpin, NH1, NH2, NSpace, NSelect, NButton, NForm, NFormItem, NInput, NTable, NThead, NTr, useMessage } from "naive-ui"
import { storeToRefs } from "pinia"
import { useAppStore } from "@/store/app"
import { useNSStore } from "@/store/namespaces"

const app = useAppStore()
const ns = useNSStore()
const { namespaces_list } = storeToRefs(ns)
const message = useMessage()
const loading = ref(false)
const namespace = ref("")
const rules = ref([])
const alarms = ref([])
const form = ref({ uuid: "", name: "", when: "", clear: "", cooldown: "", webhook: "" })

const namespaceOptions = computed(() => (namespaces_list.value || []).map((item) => ({
  label: item.title || item.uuid,
  value: item.uuid,
})))

function alarmState(state) {
  return ["raised", "acked", "cleared"][state] || "raised"
}

function edit(rule) {
  form.value = {
    uuid: rule.uuid,
    name: rule.name,
    when: rule.when,
    clear: rule.clear || "",
    cooldown: rule.cooldown || "",
    webhook: rule.actions && rule.actions[0] && rule.actions[0].webhook ? rule.actions[0].webhook.url : "",
  }
}

async function load() {
  if (!namespace.value) return
  loading.value = true
  try {
    const listed = await app.http.get("/v1/rules", { params: { namespace: namespace.value } })
    rules.value = listed.data.rules || []
    const raised = await app.http.get("/v1/alarms", { params: { namespace: namespace.value, any_state: true } })
    alarms.value = raised.data.alarms || []
  } catch (err) {
    message.error(err.message)
  }
  loading.value = false
}

async function save() {
  const body = {
    uuid: form.value.uuid || undefined,
    namespace: namespace.value,
    name: form.value.name,
    when: form.value.when,
    clear: form.value.clear,
    cooldown: form.value.cooldown,
    actions: form.value.webhook
      ? [{ webhook: { url: form.value.webhook } }]
      : [{ log: { message: "alarm" } }],
  }
  try {
    if (body.uuid) {
      await app.http.patch(`/v1/rules/${body.uuid}`, body)
    } else {
      await app.http.post("/v1/rules", body)
    }
    message.success("Rule saved")
    await load()
  } catch (err) {
    message.error(err.message)
  }
}

async function remove(rule) {
  try {
    await app.http.delete(`/v1/rules/${encodeURIComponent(rule.uuid)}`)
    if (form.value.uuid === rule.uuid) {
      form.value = { uuid: "", name: "", when: "", clear: "", cooldown: "", webhook: "" }
    }
    message.success("Rule deleted")
    await load()
  } catch (err) {
    message.error(err.message)
  }
}

async function act(alarm, kind) {
  try {
    await app.http.post(`/v1/alarms/${encodeURIComponent(alarm.rule)}/${encodeURIComponent(alarm.device)}/${kind}`, {})
    await load()
  } catch (err) {
    message.error(err.message)
  }
}

ns.fetchNamespaces()
</script>
