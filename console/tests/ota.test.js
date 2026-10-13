import { describe, expect, it } from "vitest"
import { assignBody, deviceOTALine, namespaceOTALabel, otaStatusLabel, otaSwitchOn } from "@/ota"

describe("ota contract", () => {
  it("shows reported status", () => {
    expect(otaStatusLabel(null)).toBe("—")
    expect(otaStatusLabel({ reported: { data: { ota: { status: "downloading" } } } })).toBe("downloading")
    const struct = { toJson: () => ({ ota: { status: "installed" } }) }
    expect(otaStatusLabel({ reported: { data: struct } })).toBe("installed")
  })

  it("shows whether the namespace has OTA on and the desired target", () => {
    expect(namespaceOTALabel(null)).toBe("OTA off")
    expect(namespaceOTALabel({ enabled: true })).toBe("OTA on")
    const state = {
      desired: { data: { ota: { version: "1.4.0" } } },
      reported: { data: { toJson: () => ({ ota: { status: "downloading" } }) } },
    }
    expect(deviceOTALine({ enabled: true }, state)).toBe("OTA on · target 1.4.0 · downloading")
    expect(deviceOTALine({ enabled: false }, { desired: { data: { ota: { version: "1.4.0" } } } })).toBe("OTA off · target 1.4.0")
  })

  it("turns the switch on only when an addon is registered and OTA is enabled", () => {
    expect(otaSwitchOn({ enabled: true })).toBe(false)
    expect(otaSwitchOn({ enabled: true, addonRegistered: true })).toBe(true)
    expect(otaSwitchOn({ addonRegistered: true })).toBe(false)
  })

  it("builds an assign for a namespace, a tag, or a device list", () => {
    expect(assignBody({ namespace: "plant", version: "1.4.0" })).toEqual({ namespace: "plant", version: "1.4.0" })
    expect(assignBody({ namespace: "all", tag: "line-a", version: "1.4.0" })).toEqual({ tag: "line-a", version: "1.4.0" })
    expect(assignBody({ devices: ["dev-1"], version: "1.4.0" })).toEqual({ devices: ["dev-1"], version: "1.4.0" })
  })
})
