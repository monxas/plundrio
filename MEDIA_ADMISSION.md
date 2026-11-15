# Local media admission

`PLDR_MEDIA_VALIDATION_ENABLED=true` requires local admission before publishing a
transfer as complete to ARR or removing its remote source. The default is false
for existing installations. State defaults to `/config/media-validation` and must
be durable and writable by the service user. Install ffprobe at `/usr/bin/ffprobe`
(the Dockerfile includes ffmpeg). Keep the service rootless.

Every video is checked for a readable video stream, finite positive duration,
exact local size and packets reaching its declared end. A fixed signature prefix
also catches common video formats renamed as auxiliary files. ZIP/RAR/7z remain
held until supported extraction is available. This is structural and duration
validation, not proof that every frame can decode. Invalid originals remain on
disk and do not retry for the same identity after restart. Unknown environment
failures remain pending and cannot become complete. RPC only reads journals and
file metadata; it never probes or downloads.

The helper accepts only local file protocols, clears service credentials and
limits output, wall time, allocations, CPU and child-process memory. A static
`cmd/media-validate` binary exposes the same policy to the scanner. Build with
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o media-validate ./cmd/media-validate`.

Legacy imports may be reconciled with a private `native/<transfer-id>.json`
receipt only after native ARR import history and actual imported destinations are
checked. `native_import_adopted` records that historical import; it does not claim
that a vanished download original was validated or that the library is monitored
continuously. `requested_episode_superseded` prevents redownload and hides the
obsolete request from RPC without reporting it as downloaded. Both decisions
survive restarts and preserve their evidence digest. Do not derive either from
an old Processed label.

`go test -race ./internal/mediavalidation ./internal/download ./internal/server
./cmd/plundrio` exercises generated healthy/truncated fixtures, renamed videos,
archive signatures, identity changes, restart/no-retry, legacy resolutions and
RPC completion gates without providers or production downloads.

## Reusable scanner build context

The `media-validator` Docker target exports `/media-validate` from the same source
commit as Plundrio, with a pinned Go SDK. The default runtime image also contains
the CLI and runs as UID/GID1000. It includes ffprobe; the scratch export is a build
context only.

On the Linux Docker host, use the versioned helper rather than copying a binary
between releases:

```sh
python3 scripts/build-media-validator.py plan --commit <full-clean-commit> --output-root /home/monxas/.local/share/homelab-build-contexts/media-validator
python3 scripts/build-media-validator.py build --commit <full-clean-commit> --output-root /home/monxas/.local/share/homelab-build-contexts/media-validator
```

Both commands require the exact clean checkout and return JSON. Build exports to
an immutable commit/platform/SDK directory outside Git, verifies real generated
healthy/truncated/renamed/archive fixtures and restart caching, and records the
source commit, SDK digest, build flags, recipe/script/module hashes and binary
SHA256 in `manifest.json`. Reuse verifies the manifest and binary; a mismatch
fails instead of overwriting an accepted artifact. It never deploys applications.

The returned `additional_contexts.mediavalidator` directory is the scanner build
input. The unified updater must build/accept the helper for the planned Go commit
before building the scanner, pass that exact directory, and retain its manifest
hash in the update plan. A new commit or SDK produces a new artifact directory.
Keep previous active contexts while their images/receipts still reference them.
