export const otaProviders = [
  { label: "None", value: "none" },
  { label: "hawkBit", value: "hawkbit" },
  { label: "Mender", value: "mender" },
  { label: "Webhook", value: "webhook" },
]

export function plainData(data) {
  if (!data) return {}
  if (typeof data.toJson === "function") {
    const json = data.toJson()
    if (json && typeof json === "object") return json
  }
  return data
}

export function otaRecord(state, side) {
  const section = state && state[side]
  const data = plainData(section && section.data)
  return (data && data.ota) || null
}

export function otaStatusLabel(state) {
  const record = otaRecord(state, "reported")
  return (record && record.status) || "—"
}

export function namespaceOTALabel(ota) {
  return ota && ota.enabled ? "OTA on" : "OTA off"
}

export function deviceOTALine(namespaceOta, state) {
  const parts = [namespaceOTALabel(namespaceOta)]
  const desired = otaRecord(state, "desired")
  const reported = otaRecord(state, "reported")
  if (desired && desired.version) parts.push("target " + desired.version)
  if (reported && reported.status) parts.push(reported.status)
  return parts.join(" · ")
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
