import { readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, test } from "vitest"

function files(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) {
      files(path, out)
    } else if (name.endsWith(".js") || name.endsWith(".vue") || name.endsWith(".mjs")) {
      out.push(path)
    }
  }
  return out
}

describe("console connect services", () => {
  test("every imported service is proto v1", async () => {
    const specs = new Set()
    const re = /from ["'](infinimesh-proto\/build\/es\/[^"']+_connect)["']/g
    for (const path of files("src")) {
      const text = readFileSync(path, "utf8")
      for (const match of text.matchAll(re)) {
        specs.add(match[1])
      }
    }
    expect([...specs].sort()).toEqual([
      "infinimesh-proto/build/es/node/node_connect",
      "infinimesh-proto/build/es/plugins/plugins_connect",
    ])
    for (const spec of specs) {
      const mod = await import(spec)
      const services = Object.values(mod).filter((value) => value && typeof value.typeName === "string")
      expect(services.length).toBeGreaterThan(0)
      for (const service of services) {
        expect(service.typeName).toMatch(/\.v1\.[A-Za-z]+Service$/)
      }
    }
    const { AccountsService } = await import("infinimesh-proto/build/es/node/node_connect")
    expect(AccountsService.typeName).toBe("infinimesh.node.v1.AccountsService")
    expect(AccountsService.methods.token.name).toBe("Token")
  })
})
