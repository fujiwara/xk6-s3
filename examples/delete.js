// Delete benchmark, similar to `warp delete`.
// setup() uploads OBJECTS (default: 25000) objects of SIZE (default: 1KiB),
// then the VUs delete them one by one with DeleteObject until all objects
// are deleted or DURATION passes.
//
// Unlike warp, which deletes objects in batches of 100 with DeleteObjects,
// this deletes one object per request, because xk6-s3 does not support
// DeleteObjects yet.
import { check } from "k6";
import exec from "k6/execution";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const objects = intEnv("OBJECTS", 25000);
const size = env("SIZE", "1KiB");

export const options = {
  ...setupOptions,
  scenarios: {
    delete: {
      executor: "shared-iterations",
      vus: intEnv("VUS", 20),
      iterations: objects,
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
  const r = client.deleteObject(bucket, `${prefix("delete")}${exec.scenario.iterationInTest}`);
  check(r, { "delete ok": (r) => r.ok });
}

export function teardown() {
  // objects left when DURATION passed
  cleanup("delete");
}
