import { describe, expect, it } from "vitest"
import { parseBatchCreate } from "./batchCreate"

describe("batch SK5 parsing", () => {
  it("defaults an omitted nickname to the SK5 IP and preserves spaced nicknames", () => {
    const result = parseBatchCreate(" 192.0.2.10:1080:user:secret \r\n\n 客户 一\t192.0.2.11:1081:user:secret\r\n客户二 192.0.2.12:1082:user:secret")
    expect(result.issues).toEqual([])
    expect(result.rows.map(({ name, host, port, line }) => ({ name, host, port, line }))).toEqual([
      { name: "192.0.2.10", host: "192.0.2.10", port: 1080, line: 1 },
      { name: "客户 一", host: "192.0.2.11", port: 1081, line: 3 },
      { name: "客户二", host: "192.0.2.12", port: 1082, line: 4 },
    ])
  })
  it("reports original line numbers without exposing credentials", () => {
    const result = parseBatchCreate("\n昵称 192.0.2.10:70000:user:private-password\n\n192.0.2.11:1080:user:secret")
    expect(result.issues.map(issue => issue.line)).toEqual([2])
    expect(JSON.stringify(result.issues)).not.toContain("private-password")
    expect(result.rows[0]?.name).toBe("192.0.2.11")
    expect(parseBatchCreate("a".repeat(129) + " 192.0.2.10:1080:user:secret").issues).toHaveLength(1)
  })
  it("accepts 500 rows and blocks the 501st", () => {
    expect(parseBatchCreate(Array(500).fill("192.0.2.10:1080:user:secret").join("\n")).issues).toEqual([])
    const result = parseBatchCreate(Array(501).fill("192.0.2.10:1080:user:secret").join("\n"))
    expect(result.issues).toEqual([{ line: 501, message: expect.stringContaining("500") }])
  })
})
