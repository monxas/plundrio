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
