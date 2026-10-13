import { describe, expect, it } from "vitest"
import { resolveTheme } from "@/theme"

describe("theme", () => {
  it("uses the picked theme", () => {
    expect(resolveTheme("light", "dark")).toBe("light")
    expect(resolveTheme("dark", "light")).toBe("dark")
    expect(resolveTheme("system", "light")).toBe("light")
    expect(resolveTheme("system", "dark")).toBe("dark")
    expect(resolveTheme("system", undefined)).toBe("dark")
  })
})
