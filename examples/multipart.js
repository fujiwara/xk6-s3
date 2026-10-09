// Multipart upload benchmark, comparable to `warp multipart`.
// Each VU uploads objects of SIZE (default: 100MiB) with multipart uploads
// of PART_SIZE (default: 5MiB) parts, PART_CONCURRENCY (default: 5) parts at
// a time, as the aws-sdk-go-v2 upload manager does.
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, prefix, setupOptions } from "./common.js";

const size = env("SIZE", "100MiB");
const partSize = env("PART_SIZE", "5MiB");
const concurrency = intEnv("PART_CONCURRENCY", 5);

export const options = {
  ...setupOptions,
  scenarios: {
    multipart: { executor: "constant-vus", vus: intEnv("VUS", 4), duration: env("DURATION", "1m") },
  },
  thresholds: {
    "s3_op_errors{scenario:multipart}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
}

export default function () {
  const r = client.putObjectMultipart(bucket, `${prefix("multipart")}${__VU}-${__ITER}`, size, {
    partSize,
    concurrency,
  });
  check(r, { "multipart ok": (r) => r.ok });
}

export function teardown() {
  cleanup("multipart");
}
