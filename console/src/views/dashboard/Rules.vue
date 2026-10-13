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
            <n-popconfirm @positive-click="remove(rule)">
              <template #trigger>
                <n-button size="small">Delete</n-button>
              </template>
              Delete this rule?
            </n-popconfirm>
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
import { computed, ref, watch } from "vue"
import { NSpin, NH1, NH2, NSpace, NSelect, NButton, NForm, NFormItem, NInput, NTable, NThead, NTr, NPopconfirm, useMessage } from "naive-ui"
import { storeToRefs } from "pinia"
import { useNSStore } from "@/store/namespaces"
import { useRulesStore } from "@/store/rules"

const ns = useNSStore()
const rulesApi = useRulesStore()
const { namespaces_list, selected } = storeToRefs(ns)
const message = useMessage()
const loading = ref(false)

function headerNamespace(value) {
  if (!value || value === "all") return ""
  return value
}

const namespace = ref(headerNamespace(selected.value))
watch(selected, (value) => {
  if (namespace.value) return
  namespace.value = headerNamespace(value)
})
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
    const listed = await rulesApi.client.list({ namespace: namespace.value })
    rules.value = listed.rules || []
    const raised = await rulesApi.client.listAlarms({ namespace: namespace.value, anyState: true })
    alarms.value = raised.alarms || []
  } catch (err) {
    message.error(err.message)
  }
  loading.value = false
}

watch(namespace, () => {
  load()
}, { immediate: true })

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
      await rulesApi.client.update(body)
    } else {
      await rulesApi.client.create(body)
    }
    message.success("Rule saved")
    await load()
  } catch (err) {
    message.error(err.message)
  }
}

async function remove(rule) {
  try {
    await rulesApi.client.delete({ uuid: rule.uuid })
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
    const ref = { rule: alarm.rule, device: alarm.device }
    if (kind === "ack") {
      await rulesApi.client.ackAlarm(ref)
    } else {
      await rulesApi.client.clearAlarm(ref)
    }
    await load()
  } catch (err) {
    message.error(err.message)
  }
}

ns.fetchNamespaces()
</script>
