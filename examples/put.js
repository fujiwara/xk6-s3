// PUT benchmark, comparable to `warp put`.
// Each VU uploads objects of SIZE (default: 10MiB) with PutObject.
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, prefix, setupOptions } from "./common.js";

const size = env("SIZE", "10MiB");

export const options = {
  ...setupOptions,
  scenarios: {
    put: { executor: "constant-vus", vus: intEnv("VUS", 20), duration: env("DURATION", "1m") },
  },
  thresholds: {
    "s3_op_errors{scenario:put}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
}

export default function () {
  const r = client.putObject(bucket, `${prefix("put")}${__VU}-${__ITER}`, size);
  check(r, { "put ok": (r) => r.ok });
}

export function teardown() {
  cleanup("put");
}
