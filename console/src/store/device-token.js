import { Level } from "infinimesh-proto/build/es/node/access/access_pb";

export function devicesTokenBody(pool, level = Level.READ, post = false) {
  const devices = {};
  pool.forEach((uuid) => {
    devices[uuid] = level;
  });
  return { devices, post };
}
