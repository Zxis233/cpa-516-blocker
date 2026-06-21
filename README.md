# cpa-516-blocker

CPA native plugin that forces a retry when `reasoning_tokens` is exactly `516`.

It handles two paths:

- request routing: if the normalized request body already contains `reasoning_tokens: 516`, CPA routes the execution to this plugin and the plugin returns a retryable 429 error.
- WebSocket/stream response interception: if a streamed response chunk contains `reasoning_tokens: 516`, the plugin rewrites that chunk to an OpenAI-style `type: "error"` event with status 429. CPA forwards this to WebSocket clients as an error event.

Build:

```sh
make test
make build
```

Install:

```sh
mkdir -p /path/to/cpa/plugins
cp dist/cpa-516-blocker.so /path/to/cpa/plugins/
```

CPA config:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-516-blocker:
      enabled: true
      priority: 100
      target_reasoning_tokens: 516
      retry_status: 429
      error_code: "RETRY_REQUIRED_REASONING_516"
      error_message: "[RETRY_REQUIRED_REASONING_516] reasoning_tokens=516; discard this response and resend the request."
      match_requests: true
      match_stream_responses: true
```

Most clients display `error.message`, so put the user-facing text in `error_message`. The `error_code` value is also included as the JSON error code for clients that inspect structured errors.

Use `retry_status: 503` if your client retries server errors more reliably than 429.

GitHub Actions:

Use the `Build native plugins` workflow from the GitHub Actions tab. It is manually triggered and builds:

- `cpa-516-blocker-linux-amd64.tar.gz`
- `cpa-516-blocker-linux-arm64.tar.gz`
- `cpa-516-blocker-windows-amd64.zip`

Set `publish_release` to `true` to upload the artifacts and `SHA256SUMS` to a GitHub Release.
