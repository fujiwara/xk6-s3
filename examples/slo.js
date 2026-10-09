// A scenario that warp cannot express: does the p99 latency of small reads
// stay under the SLO while large multipart uploads run in the background?
//
//   UPLOAD_VUS / UPLOAD_SIZE  background uploads (default: 8 VUs, 256MiB)
//   READ_RATE                 small reads per second (default: 500)
//   HOT_OBJECTS / HOT_SIZE    objects read by the reads (default: 1000, 64KiB)
//   SLO_P99                   p99 latency of the reads in ms (default: 50)
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const duration = env("DURATION", "5m");
const hotObjects = intEnv("HOT_OBJECTS", 1000);
const uploadSize = env("UPLOAD_SIZE", "256MiB");

export const options = {
  ...setupOptions,
  scenarios: {
    // large uploads in the background
    uploads: { executor: "constant-vus", vus: intEnv("UPLOAD_VUS", 8), duration, exec: "upload" },
    // small reads at a fixed rate
    reads: {
      executor: "constant-arrival-rate",
      rate: intEnv("READ_RATE", 500),
      timeUnit: "1s",
      duration,
      preAllocatedVUs: 50,
      exec: "read",
    },
  },
  thresholds: {
    "s3_op_duration{scenario:reads}": [`p(99)<${intEnv("SLO_P99", 50)}`],
    "s3_op_errors{scenario:reads}": ["rate<0.001"],
    "s3_op_errors{scenario:uploads}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
  preload("hot", hotObjects, env("HOT_SIZE", "64KiB"));
}

export function upload() {
  const r = client.putObjectMultipart(bucket, `${prefix("bulk")}${__VU}-${__ITER}`, uploadSize);
  check(r, { "upload ok": (r) => r.ok });
}

export function read() {
  const r = client.getObject(bucket, `${prefix("hot")}${Math.floor(Math.random() * hotObjects)}`);
  check(r, { "read ok": (r) => r.ok });
}

export function teardown() {
  cleanup("hot");
  cleanup("bulk");
}
