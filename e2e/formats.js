// E2E test of all request formats against a real S3-compatible server.
// Every combination of scheme, checksum, checksumAlgorithm and payloadSigning
// uploads objects with putObject and putObjectMultipart and deletes them with
// deleteObjects, which the server must accept with valid signatures and
// checksums.
//
//   S3_ENDPOINT_HTTP / S3_ENDPOINT_HTTPS  endpoints of the same server
//   S3_BUCKET                             bucket (created if missing)
import s3 from "k6/x/s3";
import { check } from "k6";

const bucket = __ENV.S3_BUCKET || "xk6-s3";
const endpoints = { http: __ENV.S3_ENDPOINT_HTTP, https: __ENV.S3_ENDPOINT_HTTPS };

const clients = [];
for (const [scheme, endpoint] of Object.entries(endpoints)) {
  if (!endpoint) continue;
  for (const checksum of ["when_supported", "when_required"]) {
    const algorithms = checksum === "when_supported" ? [undefined, "CRC32", "CRC32C", "CRC64NVME", "SHA1", "SHA256"] : [undefined];
    for (const checksumAlgorithm of algorithms) {
      for (const payloadSigning of ["auto", "unsigned", "signed"]) {
        const config = { endpoint, checksum, payloadSigning };
        if (checksumAlgorithm) config.checksumAlgorithm = checksumAlgorithm;
        clients.push({ name: `${scheme}/${checksum}/${checksumAlgorithm || "default"}/${payloadSigning}`, client: new s3.Client(config) });
      }
    }
  }
}
if (clients.length === 0) {
  throw new Error("set S3_ENDPOINT_HTTP and/or S3_ENDPOINT_HTTPS");
}

export const options = {
  insecureSkipTLSVerify: true,
  iterations: 1,
  thresholds: {
    checks: ["rate==1"],
    s3_op_errors: ["rate==0"],
  },
};

export function setup() {
  const r = clients[0].client.createBucket(bucket);
  if (!r.ok && r.errorCode !== "BucketAlreadyOwnedByYou" && r.errorCode !== "BucketAlreadyExists") {
    throw new Error(`createBucket: ${r.error}`);
  }
}

export default function () {
  const prefix = `${s3.runId()}/formats/`;
  for (const { name, client } of clients) {
    const key = `${prefix}${name}`;
    // larger than the 64MiB shared buffer, so that the body wraps around
    const put = client.putObject(bucket, key, "70MiB");
    const get = client.getObject(bucket, key);
    const mp = client.putObjectMultipart(bucket, `${key}.mp`, "12MiB", { partSize: "5MiB", concurrency: 3 });
    const getMp = client.getObject(bucket, `${key}.mp`);
    // DeleteObjects requires a checksum, which depends on the checksum setting.
    const del = client.deleteObjects(bucket, [key, `${key}.mp`]);
    check(null, {
      [`${name} put`]: () => put.ok,
      [`${name} get`]: () => get.ok && get.bytes === 70 * 1024 * 1024,
      [`${name} multipart`]: () => mp.ok,
      [`${name} get multipart`]: () => getMp.ok && getMp.bytes === 12 * 1024 * 1024,
      [`${name} deleteObjects`]: () => del.ok && del.count === 2,
    });
    for (const r of [put, get, mp, getMp, del].filter((r) => !r.ok)) {
      console.error(`${name}: ${r.error}`);
    }
  }
}

export function teardown() {
  const r = clients[0].client.deletePrefix(bucket, `${s3.runId()}/formats/`);
  if (!r.ok) {
    throw new Error(`deletePrefix: ${r.error}`);
  }
}
