// E2E test of listObjects options against a real S3-compatible server:
// paging, the keys option, and the delimiter option, in which count and keys
// have only the objects and prefixes has the common prefixes.
//
//   AWS_ENDPOINT_URL_S3  endpoint
//   S3_BUCKET            bucket (created if missing)
import s3 from "k6/x/s3";
import { check } from "k6";

const bucket = __ENV.S3_BUCKET || "xk6-s3";
const client = new s3.Client({});
const prefix = `${s3.runId()}/list/`;
const objects = ["a", "b", "c", "dir1/d", "dir1/e", "dir2/f"].map((k) => prefix + k);

// createBucket in setup() fails when the bucket exists, so the thresholds are
// filtered by the scenario.
export const options = {
  scenarios: { list: { executor: "shared-iterations", iterations: 1 } },
  thresholds: {
    "checks{scenario:list}": ["rate==1"],
    "s3_op_errors{scenario:list}": ["rate==0"],
  },
};

export function setup() {
  const r = client.createBucket(bucket);
  if (!r.ok && r.errorCode !== "BucketAlreadyOwnedByYou" && r.errorCode !== "BucketAlreadyExists") {
    throw new Error(`createBucket: ${r.error}`);
  }
  for (const key of objects) {
    const put = client.putObject(bucket, key, 10);
    if (!put.ok) {
      throw new Error(`putObject ${key}: ${put.error}`);
    }
  }
}

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

export default function () {
  const all = client.listObjects(bucket, prefix);
  const paged = client.listObjects(bucket, prefix, { maxKeys: 2, maxPages: 0, keys: true });
  const delim = client.listObjects(bucket, prefix, { delimiter: "/", keys: true });
  const delimPaged = client.listObjects(bucket, prefix, { delimiter: "/", maxKeys: 2, maxPages: 0, keys: true });
  const sub = client.listObjects(bucket, `${prefix}dir1/`, { delimiter: "/", keys: true });
  check(null, {
    "no delimiter": () => all.ok && all.count === 6 && all.keys === null && all.prefixes === null,
    "keys over pages": () => paged.ok && paged.count === 6 && same(paged.keys, objects) && same(paged.prefixes, []),
    "delimiter": () =>
      delim.ok &&
      delim.count === 3 &&
      same(delim.keys, objects.slice(0, 3)) &&
      same(delim.prefixes, [`${prefix}dir1/`, `${prefix}dir2/`]),
    "delimiter over pages": () =>
      delimPaged.ok &&
      delimPaged.count === 3 &&
      same(delimPaged.keys, objects.slice(0, 3)) &&
      same(delimPaged.prefixes, [`${prefix}dir1/`, `${prefix}dir2/`]),
    "delimiter in a sub prefix": () => sub.ok && sub.count === 2 && same(sub.keys, objects.slice(3, 5)) && same(sub.prefixes, []),
  });
  for (const r of [all, paged, delim, delimPaged, sub].filter((r) => !r.ok)) {
    console.error(r.error);
  }
}

export function teardown() {
  const r = client.deletePrefix(bucket, prefix);
  if (!r.ok) {
    throw new Error(`deletePrefix: ${r.error}`);
  }
}
