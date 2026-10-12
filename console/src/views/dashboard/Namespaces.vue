<template>
  <n-spin :show="loading">
    <n-grid item-responsive>
      <n-grid-item span="24 400:12 550:8 1000:4">
        <n-h1 prefix="bar" align-text type="info">
          <n-text type="info"> Namespaces </n-text>
        </n-h1>
      </n-grid-item>
      <n-grid-item span="0 1000:10 1400:12"> </n-grid-item>
      <n-grid-item span="8 400:12 550:6 1000:3 1400:2" align="end">
        <n-button strong secondary round type="info" @click="refresh">
          <template #icon>
            <n-icon>
              <refresh-outline />
            </n-icon>
          </template>
          Refresh
        </n-button>
      </n-grid-item>
      <n-grid-item span="16 400:24 550:10 1000:7 1400:6" align="end">
        <ns-create />
      </n-grid-item>
    </n-grid>
    <n-table :bordered="false" :single-line="true" style="margin-top: 10px">
      <n-thead>
        <n-tr>
          <th></th>
          <th>UUID</th>
          <th>Title</th>
          <th>Access</th>
          <th>Actions</th>
        </n-tr>
      </n-thead>
      <tbody>
        <template v-for="ns in pool.admin" :key="ns.uuid">
          <n-tr @click="toggleNS(ns)">
            <td>
              <n-icon>
                <chevron-down-outline v-if="expand.has(ns.uuid)" />
                <chevron-forward-outline v-else />
              </n-icon>
            </td>
            <td>
              <uuid-badge :uuid="ns.uuid" />
            </td>
            <td>
              <strong>
                {{ ns.title }}
              </strong>
            </td>
            <td>
              <access-badge :access="ns.access.level" namespace />
              <access-badge access="OWNER" v-if="access_role_conv(ns) == Role.OWNER" left="5px" namespace />
            </td>
            <td>
              <n-space>
                <config-edit-modal :o="ns" @submit="handleConfigUpdate"
                  v-if="access_lvl_conv(ns) > 3 || access_role_conv(ns) == Role.OWNER" />

                <n-button type="success" round secondary @click.stop.prevent="setNSAndGo(ns.uuid, 'Devices')">
                  Devices
                </n-button>
                <n-button type="info" round secondary @click.stop.prevent="setNSAndGo(ns.uuid, 'Accounts')">
                  Accounts
                </n-button>
                <ns-delete :o="ns" :deletables="() => showDeletables(ns.uuid)" @confirm="() => handleDelete(ns.uuid)" />
              </n-space>
            </td>
          </n-tr>
          <n-tr v-if="expand.has(ns.uuid)">
            <td colspan="5">
              <n-space>
                <n-switch :value="!!(otaOf(ns).enabled)" @update:value="(v) => saveOTA(ns, v)" />
                <span>Enable OTA</span>
                <n-select style="width: 140px" :value="otaOf(ns).backend || 'hawkBit'"
                  :options="[{ label: 'hawkBit', value: 'hawkBit' }, { label: 'Mender', value: 'mender' }]"
                  @update:value="(v) => saveOTA(ns, !!otaOf(ns).enabled, v)" />
                <n-input style="width: 280px" :default-value="otaOf(ns).backendUrl || ''" placeholder="Backend URL"
                  @blur="(e) => saveOTA(ns, !!otaOf(ns).enabled, otaOf(ns).backend || 'hawkBit', e.target.value)" />
              </n-space>
              <ns-joins :namespace="ns.uuid"
                :admin="access_role_conv(ns) == Role.OWNER || access_lvl_conv(ns) == Role.ROOT" />
            </td>
          </n-tr>
        </template>
        <n-tr v-if="pool.user && pool.user.length">
          <td colspan="5" align="center">
            <span>
              Namespaces below are those you don't have admin access to
            </span>
          </td>
        </n-tr>
        <n-tr v-for="ns in pool.user" :key="ns.uuid">
          <td></td>
          <td>
            <uuid-badge :uuid="ns.uuid" type="default" />
          </td>
          <td>
            <strong>
              {{ ns.title }}
            </strong>
          </td>
          <td>
            <access-badge :access="ns.access.level" namespace />
          </td>
          <td>
            <n-space>
              <n-button type="success" round secondary @click.stop.prevent="setNSAndGo(ns.uuid, 'Devices')">
                Devices
              </n-button>
              <n-button type="info" round secondary @click.stop.prevent="setNSAndGo(ns.uuid, 'Accounts')">
                Accounts
              </n-button>
            </n-space>
          </td>
        </n-tr>
      </tbody>
    </n-table>
  </n-spin>
</template>

<script setup>
import { ref, computed, defineAsyncComponent } from "vue"
import { useRouter } from "vue-router"
import {
  NSpin, NTable, NThead, NTr,
  NButton, NIcon, NSpace, NGrid,
  NGridItem, NH1, NText, NSwitch, NSelect, NInput, useMessage
} from "naive-ui";

import { storeToRefs } from "pinia";
import { groupBy } from "lodash";
import { Role } from "infinimesh-proto/build/es/node/access/access_pb"

import { useNSStore } from "@/store/namespaces";
import { useAppStore } from "@/store/app";
import { access_lvl_conv, access_role_conv } from "@/utils/access";

const RefreshOutline = defineAsyncComponent(() => import("@vicons/ionicons5/RefreshOutline"))
const ChevronForwardOutline = defineAsyncComponent(() => import("@vicons/ionicons5/ChevronForwardOutline"))
const ChevronDownOutline = defineAsyncComponent(() => import("@vicons/ionicons5/ChevronDownOutline"))

const UuidBadge = defineAsyncComponent(() => import("@/components/core/uuid-badge.vue"))
const AccessBadge = defineAsyncComponent(() => import("@/components/core/access-badge"))

const NsCreate = defineAsyncComponent(() => import("@/components/namespaces/create-action-button.vue"))
const NsJoins = defineAsyncComponent(() => import("@/components/namespaces/joins.vue"))
const NsDelete = defineAsyncComponent(() => import("@/components/core/recursive-delete-modal.vue"))

const ConfigEditModal = defineAsyncComponent(() => import("@/components/core/config-edit-modal.vue"))

const store = useNSStore();
const { loading, namespaces_list: namespaces } = storeToRefs(store);

const pool = computed(() => groupBy(namespaces.value, (e) => {
  const role = access_role_conv((e))

  if (role == Role.OWNER || access_lvl_conv(e) >= 3) {
    return "admin"
  }
  return "user"
}))

const expand = ref(new Set())
const otaState = ref({})
function otaOf(ns) {
  return otaState.value[ns.uuid] || ns.ota || {}
}

const router = useRouter()
function setNSAndGo(ns, route) {
  store.selected = ns
  router.push({ name: route })
}

function refresh() {
  expand.value = new Set()
  store.fetchNamespaces(true)
}

refresh()

async function showDeletables(uuid) {
  return store.deletables(uuid)
}

const message = useMessage()
async function handleDelete(uuid) {
  loading.value = true
  try {
    await store.delete(uuid)
    message.success("Namespace successfuly deleted")
  } catch (e) {
    message.error("Failed to delete namespace: " + e.message)
  }
  refresh()
}

const app = useAppStore()
async function toggleNS(ns) {
  if (expand.value.has(ns.uuid)) {
    expand.value.delete(ns.uuid)
    return
  }
  expand.value.add(ns.uuid)
  try {
    const res = await app.http.get(`/v1/namespaces/${ns.uuid}`)
    otaState.value = { ...otaState.value, [ns.uuid]: res.data.ota || {} }
  } catch (e) {
    message.error("Failed to load OTA setting: " + e.message)
  }
}
async function saveOTA(ns, enabled, backend, backendUrl) {
  const current = otaOf(ns)
  try {
    await app.http.patch(`/v1/namespaces/${ns.uuid}`, {
      uuid: ns.uuid,
      ota: {
        enabled,
        backend: backend || current.backend || "hawkBit",
        backendUrl: backendUrl !== undefined ? backendUrl : (current.backendUrl || ""),
      },
    })
    otaState.value = {
      ...otaState.value,
      [ns.uuid]: {
        enabled,
        backend: backend || current.backend || "hawkBit",
        backendUrl: backendUrl !== undefined ? backendUrl : (current.backendUrl || ""),
      },
    }
    message.success("OTA setting saved")
  } catch (e) {
    message.error("Failed to save OTA setting: " + e.message)
  }
  refresh()
}

async function handleConfigUpdate(ns) {
  try {
    await store.update(ns)
    message.success("Namespace config updated")
  } catch (e) {
    message.error("Failed to update namespace config: " + e.message)
  }
  refresh()
}

</script>