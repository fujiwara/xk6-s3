// GET benchmark, comparable to `warp get`.
// setup() uploads OBJECTS (default: 2500) objects of SIZE (default: 10MiB),
// then each VU downloads random objects.
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const objects = intEnv("OBJECTS", 2500);
const size = env("SIZE", "10MiB");

export const options = {
  ...setupOptions,
  scenarios: {
    get: { executor: "constant-vus", vus: intEnv("VUS", 20), duration: env("DURATION", "1m") },
  },
  thresholds: {
    "s3_op_errors{scenario:get}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
  preload("get", objects, size);
}

export default function () {
  const r = client.getObject(bucket, `${prefix("get")}${Math.floor(Math.random() * objects)}`);
  check(r, { "get ok": (r) => r.ok });
}

export function teardown() {
  cleanup("get");
}
