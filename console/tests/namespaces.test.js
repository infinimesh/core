import { describe, expect, it } from "vitest"
import { indexNamespaces } from "@/namespaces"

describe("namespace list", () => {
  it("keeps a namespace from a later page", () => {
    const indexed = indexNamespaces([
      { uuid: "old", title: "Old" },
      { uuid: "new", title: "New" },
    ])
    expect(Object.keys(indexed)).toEqual(["old", "new"])
    expect(indexed.new.title).toBe("New")
  })
})
