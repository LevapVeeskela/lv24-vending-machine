# Fixture sources

- `request.json` and `response.json`: request example and the bf16 CUDA
  response to `examples/request.json` with Drex v1.5, copied from
  `../ideas/decision-model-trading/drex-decision-models/docs/request-format.md`
  (the `# Request format` doc, response section).
- `response_cloud.json`: same answers as `response.json`, reshaped into the
  cloud API envelope with `model`, `request_id` and `evaluation_time_ms`
  (fields documented in `docs/spec/04-providers.md` §10).

License of the upstream file: Apache-2.0.
Upstream commit: `b932571`.
