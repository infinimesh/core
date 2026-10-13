export const otaProviders = [
  { label: "None", value: "none" },
  { label: "hawkBit", value: "hawkbit" },
  { label: "Mender", value: "mender" },
  { label: "Webhook", value: "webhook" },
]

export function otaStatusLabel(state) {
  const status = state && state.reported && state.reported.data && state.reported.data.ota && state.reported.data.ota.status
  return status || "—"
}

export function otaSwitchOn(ota) {
  return !!(ota && ota.enabled && ota.addonRegistered)
}

export function assignBody({ namespace, tag, devices, version }) {
  const body = { version }
  if (namespace && namespace !== "all") body.namespace = namespace
  if (tag) body.tag = tag
  if (devices && devices.length) body.devices = devices
  return body
}
