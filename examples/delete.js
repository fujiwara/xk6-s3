// Delete benchmark, comparable to `warp delete`.
// setup() uploads OBJECTS (default: 25000) objects of SIZE (default: 1KiB),
// then the VUs delete them with DeleteObjects in batches of BATCH (default:
// 100) objects until all objects are deleted or DURATION passes.
import { check } from "k6";
import exec from "k6/execution";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const objects = intEnv("OBJECTS", 25000);
const size = env("SIZE", "1KiB");
const batch = intEnv("BATCH", 100);
const iterations = Math.ceil(objects / batch);

export const options = {
  ...setupOptions,
  scenarios: {
    delete: {
      executor: "shared-iterations",
      // shared-iterations requires at least as many iterations as VUs
      vus: Math.min(intEnv("VUS", 20), iterations),
      iterations,
      maxDuration: env("DURATION", "1m"),
    },
  },
  thresholds: {
    "s3_op_errors{scenario:delete}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
  preload("delete", objects, size);
}

export default function () {
  const first = exec.scenario.iterationInTest * batch;
  const keys = [];
  for (let i = first; i < Math.min(first + batch, objects); i++) {
    keys.push(`${prefix("delete")}${i}`);
  }
  const r = client.deleteObjects(bucket, keys);
  check(r, { "delete ok": (r) => r.ok && r.count === keys.length });
}

export function teardown() {
  // objects left when DURATION passed
  cleanup("delete");
}
