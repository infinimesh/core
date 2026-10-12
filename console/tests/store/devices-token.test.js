import { describe, expect, test } from "vitest";
import { Level } from "infinimesh-proto/build/es/node/access/access_pb";
import { devicesTokenBody } from "@/store/device-token";

describe("devicesTokenBody", () => {
  test("read token stays at read and does not post", () => {
    expect(devicesTokenBody(["a"])).toEqual({
      devices: { a: Level.READ },
      post: false,
    });
  });

  test("post token may post and keeps manage for desired writes", () => {
    expect(devicesTokenBody(["a"], Level.MGMT, true)).toEqual({
      devices: { a: Level.MGMT },
      post: true,
    });
  });
});
