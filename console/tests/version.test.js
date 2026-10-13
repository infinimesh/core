import { describe, expect, it } from "vitest"
import { versionTag } from "@/version"

describe("version tag", () => {
  it("is the stamped version, not the placeholder name", () => {
    expect(versionTag).not.toBe("INFINIMESH_VERSION_TAG")
    expect(versionTag).toBe("development")
  })
})
