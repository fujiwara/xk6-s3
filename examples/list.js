// List benchmark, comparable to `warp list`.
// setup() uploads OBJECTS (default: 10000) objects of SIZE (default: 1KiB),
// split into one prefix per VU. Each VU then lists all objects under its
// own prefix with ListObjectsV2. Each request (page of up to MAX_KEYS keys,
// default: server default) is measured as a list operation.
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const vus = intEnv("VUS", 20);
const objects = intEnv("OBJECTS", 10000);
const perVU = Math.ceil(objects / vus);
const size = env("SIZE", "1KiB");
const maxKeys = intEnv("MAX_KEYS", 0);

export const options = {
  ...setupOptions,
  scenarios: {
    list: { executor: "constant-vus", vus, duration: env("DURATION", "1m") },
  },
  thresholds: {
    "s3_op_errors{scenario:list}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
  for (let vu = 1; vu <= vus; vu++) {
    preload(`list/${vu}`, perVU, size);
  }
}

export default function () {
  const r = client.listObjects(bucket, prefix(`list/${__VU}`), { maxKeys, maxPages: 0 });
  check(r, { "list ok": (r) => r.ok && r.count === perVU });
}

export function teardown() {
  cleanup("list");
}
