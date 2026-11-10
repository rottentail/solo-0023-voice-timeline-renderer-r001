# Voice Timeline Renderer

## Service goal

Voice Timeline Renderer is a backend service for assembling narrated PCM audio
into a deterministic mixdown. It gives audio-platform engineers a small HTTP
surface for registering high-precision WAV sources (16-bit or 24-bit integer
PCM, and 32-bit IEEE float), composing ordered timeline spans, and rendering a
normalized 16-bit or 24-bit output along with a machine-readable render
receipt.

The project is intentionally production-only. Automated tests, fixtures, and a
test command are deferred to the downstream task stage, which will add focused
unit, integration, and end-to-end verification.

## Users

- Audio-platform engineers who need a self-hosted assembly stage.
- Audio editing tooling that submits WAV sources and timeline operations.
- Quality engineers who replay a render and compare the receipt with the output.

## Project shape and stack

- Shape: backend.
- Language: Go.
- Runtime: Go bundled HTTP packages.
- Persistence: a local data directory containing metadata JSON and media files.
- External services: none.

## Core entities

### Source asset

A source asset is an immutable uploaded PCM WAV. Its identity, SHA-256 digest,
frame count, sample rate, channel count, peak amplitude, RMS amplitude, and duration are
recorded after decoding. A source is never updated in place.

Invariants:

- The payload must be a RIFF/WAVE file using 16-bit integer PCM, 24-bit
  integer PCM, or 32-bit IEEE float samples (plain or
  `WAVE_FORMAT_EXTENSIBLE`); floating point samples must be finite.
- Only mono and stereo layouts are accepted.
- The digest and signal measurements describe the exact bytes stored.
- Decoded payloads are bounded by configured byte and duration limits.
- Source metadata records both the bit width and the sample coding
  (`sample_format`), and those facts remain visible on read-back.

### Assembly timeline

A timeline is an ordered set of spans over existing source assets. A span
selects a half-open source frame interval, a gain in decibels, and an optional
crossfade with the preceding span.

Invariants:

- A timeline has at least one span.
- Every referenced source exists.
- Source intervals are non-empty and remain inside the source frame count.
- The first span has no crossfade.
- A crossfade cannot exceed the shorter neighboring span.
- Output channel count is explicit and independent from an input source's
  channel count.
- Output sample width is explicit (16-bit or 24-bit integer PCM) and defaults
  to 16-bit when omitted, preserving legacy timeline behavior.
- Spans may reference sources of different sample formats and bit widths;
  each span keeps the referenced source's original precision facts.

### Render job

A render job is the durable record of one timeline execution.

State machine:

`queued -> rendering -> finalizing -> completed`

Any active state may transition to `failed`. `completed` and `failed` are
terminal. A failed job stores a stable error code and message. A completed job
stores the output file, output metadata, and immutable render receipt.

### Render receipt

A receipt records source identifiers and per-span source precision (sample
format and bit width), span placement, gain adjustments, peak and RMS
measurements, final normalization gain, output format and bit width, frame
count, output digest, and non-fatal warnings including precision down-mix
notices. It is written only after the output bytes have been atomically
committed and the encoded WAV has been re-decoded and verified against the
timeline's requested format.

## Workflows

### 1. Register audio source

Entry point: `PUT /v1/sources/{source_id}`.

1. The HTTP boundary enforces request size and base64 decoding.
2. The service validates identity fields and decodes the WAV structure.
3. The codec layer verifies PCM format, frame alignment, and signal bounds.
4. Signal measurements are computed from the decoded samples.
5. Storage writes the media bytes and metadata using an atomic commit.

Success returns source metadata. Failure returns a stable validation error and
leaves no visible partial source.

### 2. Compose assembly timeline

Entry point: `PUT /v1/timelines/{timeline_id}`.

1. The HTTP boundary decodes and sizes the span list.
2. Domain validation checks ranges, gain limits, crossfade limits, and temporal
   ordering.
3. The service resolves every source reference from storage.
4. A timeline record is persisted once all spans are valid.

Success returns a ready timeline. Failure reports the first stable validation
issue without persisting a partial timeline.

### 3. Render timeline mixdown

Entry point: `PUT /v1/renders/{render_id}` with a `timeline_id` body field.

1. The service creates a durable `queued` job.
2. The renderer moves to `rendering` and loads all referenced sources.
3. Each source interval is resampled to the timeline sample rate, mapped to the
   requested channel layout, gain-adjusted, faded, and placed on the timeline.
4. Overlapping neighboring spans are mixed with equal-power crossfade weights.
5. The finalizing stage applies peak normalization to the requested target,
   encodes the selected 16-bit or 24-bit integer PCM WAV, re-decodes it to
   verify every format fact against the timeline, and writes the WAV and
   receipt through temporary files, renaming them into place atomically.
6. The job moves to `completed`. Any error records a failed terminal state and
   removes unpublished temporary output. A format mismatch aborts before
   publication, so no success state ever carries a wrong-format download.

Success is observable through the job resource, the downloaded WAV, and the
receipt, all reporting identical format and precision facts. Failure is
observable through the terminal job error code. Output downloads additionally
re-verify the stored digest and WAV header against the receipt.

## HTTP interface

- `GET /healthz`
- `PUT /v1/sources/{source_id}`
- `GET /v1/sources/{id}`
- `PUT /v1/timelines/{timeline_id}`
- `GET /v1/timelines/{id}`
- `PUT /v1/renders/{render_id}`
- `GET /v1/renders/{id}`
- `GET /v1/renders/{id}/receipt`
- `GET /v1/renders/{id}/output`

Errors use a JSON envelope:

```json
{
  "error": {
    "code": "invalid_source_interval",
    "message": "source interval ends beyond the source frame count",
    "field": "spans[0].source_end_frame"
  }
}
```

## Modules

| Module | Responsibility |
|---|---|
| `httpapi` | HTTP routing, request limits, JSON encoding, media responses |
| `service` | Workflow orchestration and job lifecycle |
| `domain` | Entities, validation rules, stable errors, state transitions |
| `audiocodec` | WAV parsing and encoding for 16/24-bit integer PCM and 32-bit float, resampling, channel mapping, signal metrics |
| `render` | Span evaluation, crossfade mixing, normalization, receipt construction |
| `storage` | Atomic source, timeline, job, output, and receipt persistence |

## Engineering mechanisms

- State transitions: render jobs persist every active and terminal transition.
- Error propagation: domain errors retain stable codes through HTTP responses.
- Resource lifecycle: temporary files are cleaned after failed publication.
- Concurrency: storage guards indexes and registry updates; unique IDs and
  atomic renames prevent duplicate or half-visible records.
- Validation boundaries: request size, PCM layout, timeline bounds, and gain
  ranges are checked before allocation-heavy work.

## Validation plan

The runtime manifest exposes one bounded production smoke path per workflow:

- Register a generated WAV, read it back, and confirm a malformed payload fails.
- Compose a two-span timeline, read it back, and confirm an invalid crossfade
  fails.
- Render a crossfaded timeline, download the output, decode it, and match core
  receipt measurements.
- Register 24-bit PCM and 32-bit float sources, render a mixed-precision
  timeline to 24-bit and 16-bit outputs, confirm source/receipt/WAV precision
  facts agree, re-render deterministically, and reject an unsupported output
  width without leaving artifacts.

These checks use the real HTTP entry points and local storage. They are not a
replacement for the test suite that the later task stage will add.
