import { describe, expect, it } from "vitest"
import { assignBody, otaStatusLabel, otaSwitchOn } from "@/ota"

describe("ota contract", () => {
  it("shows reported status", () => {
    expect(otaStatusLabel(null)).toBe("—")
    expect(otaStatusLabel({ reported: { data: { ota: { status: "downloading" } } } })).toBe("downloading")
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
