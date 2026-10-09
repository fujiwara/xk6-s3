// Shared settings of the examples. All values can be set by environment variables.
//
//   AWS_ENDPOINT_URL_S3  endpoint URL (or AWS_ENDPOINT_URL)
//   AWS_REGION    region (or AWS_DEFAULT_REGION, default: us-east-1)
//   S3_BUCKET     bucket, created in setup() if missing (default: xk6-s3)
//   AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY  credentials
//   VUS           number of VUs, i.e. concurrent clients
//   DURATION      test duration
//
// The client uses the aws-sdk-go-v2 defaults, which reproduce the requests of
// current AWS SDKs. Set S3_CHECKSUM=when_required and/or
// S3_PAYLOAD_SIGNING=unsigned|signed to reproduce other clients.
import s3 from "k6/x/s3";

export const bucket = __ENV.S3_BUCKET || "xk6-s3";

export const client = new s3.Client({
  checksum: __ENV.S3_CHECKSUM || "when_supported",
  payloadSigning: __ENV.S3_PAYLOAD_SIGNING || "auto",
});

export function env(name, def) {
  return __ENV[name] || def;
}

export function intEnv(name, def) {
  const v = __ENV[name];
  return v ? parseInt(v, 10) : def;
}

// prefix returns the key prefix of a dataset in this run.
export function prefix(dataset) {
  return `${s3.runId()}/${dataset}/`;
}

// ensureBucket creates the bucket unless it exists.
export function ensureBucket() {
  if (client.listObjects(bucket, "", { maxKeys: 1 }).ok) {
    return;
  }
  const r = client.createBucket(bucket);
  if (!r.ok && r.errorCode !== "BucketAlreadyOwnedByYou" && r.errorCode !== "BucketAlreadyExists") {
    throw new Error(`failed to create bucket ${bucket}: ${r.error}`);
  }
}

// preload uploads the dataset and fails the test if any upload fails.
export function preload(dataset, count, size) {
  const r = client.preload(bucket, prefix(dataset), count, size, { concurrency: intEnv("PRELOAD_CONCURRENCY", 32) });
  if (!r.ok) {
    throw new Error(`preload failed: ${r.failed} of ${count} objects: ${r.error}`);
  }
}

// cleanup deletes the dataset.
export function cleanup(dataset) {
  const r = client.deletePrefix(bucket, prefix(dataset), { concurrency: intEnv("PRELOAD_CONCURRENCY", 32) });
  if (!r.ok) {
    console.warn(`cleanup failed: ${r.failed} objects: ${r.error}`);
  }
}

export const setupOptions = {
  setupTimeout: env("SETUP_TIMEOUT", "30m"),
  teardownTimeout: env("TEARDOWN_TIMEOUT", "30m"),
};
