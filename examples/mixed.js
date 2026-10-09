// Mixed benchmark, comparable to `warp mixed`.
// setup() uploads OBJECTS (default: 2500) objects of SIZE (default: 10MiB),
// then each VU runs GET, HEAD, PUT and DELETE with the weights of the warp
// defaults (45:30:15:10). GET and HEAD read the preloaded objects. PUT
// uploads new objects, which DELETE removes.
import { check } from "k6";
import { bucket, client, cleanup, ensureBucket, env, intEnv, preload, prefix, setupOptions } from "./common.js";

const objects = intEnv("OBJECTS", 2500);
const size = env("SIZE", "10MiB");
const weights = {
  get: intEnv("GET_WEIGHT", 45),
  head: intEnv("HEAD_WEIGHT", 30),
  put: intEnv("PUT_WEIGHT", 15),
  delete: intEnv("DELETE_WEIGHT", 10),
};
const total = Object.values(weights).reduce((a, b) => a + b, 0);

export const options = {
  ...setupOptions,
  scenarios: {
    mixed: { executor: "constant-vus", vus: intEnv("VUS", 20), duration: env("DURATION", "1m") },
  },
  thresholds: {
    "s3_op_errors{scenario:mixed}": ["rate<0.01"],
  },
};

export function setup() {
  ensureBucket();
  preload("mixed", objects, size);
}

// Objects uploaded by this VU and not deleted yet.
const uploaded = [];

function pick() {
  let x = Math.random() * total;
  for (const [op, w] of Object.entries(weights)) {
    if (x < w) return op;
    x -= w;
  }
  return "get";
}

export default function () {
  const existing = `${prefix("mixed")}${Math.floor(Math.random() * objects)}`;
  let op = pick();
  if (op === "delete" && uploaded.length === 0) {
    op = "put";
  }
  let r;
  switch (op) {
    case "get":
      r = client.getObject(bucket, existing);
      break;
    case "head":
      r = client.headObject(bucket, existing);
      break;
    case "put": {
      const key = `${prefix("mixed")}new/${__VU}-${__ITER}`;
      r = client.putObject(bucket, key, size);
      if (r.ok) uploaded.push(key);
      break;
    }
    case "delete":
      r = client.deleteObject(bucket, uploaded.shift());
      break;
  }
  check(r, { [`${op} ok`]: (r) => r.ok });
}

export function teardown() {
  cleanup("mixed");
}
