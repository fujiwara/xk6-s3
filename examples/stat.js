// Stat benchmark, comparable to `warp stat`.
// setup() uploads OBJECTS (default: 10000) objects of SIZE (default: 1KiB),
// then each VU runs HeadObject on random objects.
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const objects = intEnv("OBJECTS", 10000);
const size = env("SIZE", "1KiB");

export const options = {
  ...setupOptions,
  scenarios: {
    stat: { executor: "constant-vus", vus: intEnv("VUS", 20), duration: env("DURATION", "1m") },
  },
  thresholds: {
    "s3_op_errors{scenario:stat}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
  preload("stat", objects, size);
}

export default function () {
  const r = client.headObject(bucket, `${prefix("stat")}${Math.floor(Math.random() * objects)}`);
  check(r, { "head ok": (r) => r.ok });
}

export function teardown() {
  cleanup("stat");
}
