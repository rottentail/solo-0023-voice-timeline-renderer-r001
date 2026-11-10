# Voice Timeline Renderer

Voice Timeline Renderer is a self-hosted Go service for turning validated PCM
WAV recordings into a crossfaded, peak-normalized mixdown. The service exposes
an idempotent HTTP surface, keeps media and metadata under one data directory,
and records every render transition.

The implementation covers:

- 16-bit and 24-bit integer PCM, plus 32-bit IEEE float WAV validation and
  signal measurement (including `WAVE_FORMAT_EXTENSIBLE` containers).
- Mixed-precision timelines whose sources may use any accepted source format.
- Render output selectable as 16-bit or 24-bit integer PCM per timeline, with
  16-bit remaining the default for backward compatibility.
- Ordered timeline spans with source-frame selection, gain, and crossfade.
- Linear sample-rate conversion and mono/stereo channel mapping.
- Equal-power neighboring-span crossfades.
- Peak normalization into a requested dBFS target.
- Durable render states, receipts, and atomically committed output folders.
- Source precision facts (`sample_format`, `bits_per_sample`) carried through
  source metadata, per-span receipt records, the output WAV header, and
  verified again before any output download.

Automated tests are intentionally deferred. A later task stage will add unit,
integration, and end-to-end verification around the production interfaces.

## Requirements

- Go 1.23 or newer.
- A writable local directory for source media, timeline metadata, render jobs,
  output WAV files, and receipts.

No network service, database server, or third-party Go module is required.

## Build

```bash
go build ./...
```

## Run

```bash
go run ./cmd/voiceservice \
  -addr :8093 \
  -data ./var/voice-timeline
```

The health endpoint is `GET http://127.0.0.1:8093/healthz`.

Runtime settings are supplied through arguments:

- `-addr`: HTTP listen address, default `:8093`.
- `-data`: persistent data root, default `./var/voice-timeline`.

No environment variable is required. Do not put credentials in the repository;
the service does not consume any remote credential.

## HTTP interface

### Register a source

`PUT /v1/sources/{source_id}`

```json
{
  "name": "opening take",
  "wav_base64": "UklGR..."
}
```

The source ID is chosen by the caller and must be 3-64 characters using
letters, digits, underscore, or hyphen. Repeating the same ID with exactly the
same name and bytes is idempotent. Reusing it with different content returns
the `source_conflict` error.

Response:

```json
{
  "id": "opening_take_01",
  "name": "opening take",
  "digest": "b7c0...",
  "byte_count": 96044,
  "sample_rate": 48000,
  "channels": 1,
  "bits_per_sample": 16,
  "sample_format": "pcm_integer",
  "frame_count": 48000,
  "duration_ms": 1000,
  "peak_dbfs": -8.0,
  "rms_dbfs": -11.0,
  "created_at": "2026-11-09T12:00:00Z"
}
```

Accepted source payloads are mono or stereo RIFF/WAVE files using one of:

- 16-bit integer PCM (`"sample_format": "pcm_integer"`, `bits_per_sample: 16`)
- 24-bit integer PCM (`"sample_format": "pcm_integer"`, `bits_per_sample: 24`)
- 32-bit IEEE float (`"sample_format": "ieee_float"`, `bits_per_sample: 32`)

Floating point samples must be finite; NaN and infinite values are rejected.
Any other width or coding returns the `invalid_wav` error and leaves no visible
source.

### Compose a timeline

`PUT /v1/timelines/{timeline_id}`

```json
{
  "name": "two part narration",
  "sample_rate": 48000,
  "output_channels": 2,
  "output_bits_per_sample": 24,
  "target_peak_dbfs": -1,
  "spans": [
    {
      "source_id": "opening_take_01",
      "source_start_frame": 0,
      "source_end_frame": 48000,
      "gain_db": -2,
      "crossfade_frames": 0
    },
    {
      "source_id": "closing_take_01",
      "source_start_frame": 0,
      "source_end_frame": 44100,
      "gain_db": 0,
      "crossfade_frames": 4800
    }
  ]
}
```

`output_bits_per_sample` selects the rendered WAV width and must be `16` or
`24`. Omitting it defaults to `16`, so timelines written against the original
service render exactly as before. An unsupported value (for example `32`)
returns `invalid_output_width` and no timeline or render is persisted. Spans
may freely mix 16-bit PCM, 24-bit PCM, and 32-bit float sources; each receipt
span records the source's original `source_sample_format` and
`source_bits_per_sample`, and down-mixing to a narrower output adds a
non-fatal precision warning.

Spans are sequential. A later span overlaps its predecessor by
`crossfade_frames`; the overlap is mixed with equal-power weights. A crossfade
must fit within both neighboring spans.

### Start a render

`PUT /v1/renders/{render_id}`

```json
{
  "timeline_id": "two_part_narration_01"
}
```

The render runs through the durable states
`queued -> rendering -> finalizing -> completed`. A failed render records a
terminal `failed` state with a stable error code and does not expose a partial
output folder. Before a render completes, the encoded WAV is decoded again and
its sample format, bit width, sample rate, channels, and frame count are
checked against the timeline; any mismatch aborts the render, so a successful
job can never point at a wrong-format downloadable file. Re-rendering the same
timeline produces byte-identical audio and the same format conclusions.

### Read resources

- `GET /v1/sources/{source_id}`
- `GET /v1/timelines/{timeline_id}`
- `GET /v1/renders/{render_id}`
- `GET /v1/renders/{render_id}/receipt`
- `GET /v1/renders/{render_id}/output`

The output route returns `audio/wav`. The receipt records source digests and
per-span source precision (`source_sample_format`, `source_bits_per_sample`),
span placement, channel mapping, measured peak and RMS amplitude,
normalization gain, output digest and output format facts, precision warnings,
and non-fatal warnings. On every download the service re-verifies the output
digest and WAV header against the receipt; a corrupted or mismatched file is
refused with `output_integrity_error` instead of being served.

## Data directory

```text
var/voice-timeline/
  sources/<source_id>/
    source.wav
    metadata.json
  timelines/<timeline_id>.json
  jobs/<render_id>.json
  renders/<render_id>/
    output.wav
    receipt.json
  tmp/
```

Source and render folders are assembled in `tmp/` and moved into place with one
directory rename. Metadata updates use same-directory temporary files followed
by rename.

## Source layout

```text
cmd/voiceservice/       service entry point
cmd/timelinecheck/      workflow execution entry point
internal/httpapi/       routing, request limits, JSON errors, media response
internal/service/       workflow orchestration and idempotency
internal/domain/        entities, validation, errors, and render transitions
internal/audiocodec/    WAV handling, transforms, and signal measurement
internal/render/        timeline preparation, mixing, normalization, receipt
internal/storage/       atomic local repository
internal/checkflow/     bounded production workflow checks
```

## Workflow checks

Run each declared production check from the repository root:

```bash
go run ./cmd/timelinecheck -workflow ingest-audio-source
go run ./cmd/timelinecheck -workflow compose-timeline
go run ./cmd/timelinecheck -workflow render-mixdown
go run ./cmd/timelinecheck -workflow render-high-precision
```

Each check creates an isolated temporary data directory, starts the real HTTP
handler, sends generated WAV inputs, and verifies read-back or rendered output.
The checks are bounded and do not require network access. The
`render-high-precision` check registers 16-bit PCM, 24-bit PCM, and 32-bit
float sources, renders mixed-precision timelines to both 24-bit and 16-bit
outputs, verifies source/receipt/WAV format consistency and deterministic
re-renders, and confirms unsupported widths are rejected without artifacts.

## Deferred test boundary

This baseline contains no unit tests, test fixtures, browser checks, or
`test_command`. The downstream task stage owns test creation and red/green
verification. It should extend coverage around WAV chunk edge cases, timeline
crossfade bounds, failed job cleanup, concurrent idempotency, and HTTP error
mapping.
