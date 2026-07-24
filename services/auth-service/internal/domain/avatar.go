package domain

import "errors"

// Avatar processing limits. Kept in the domain so both the HTTP handler (body
// cap) and the service (decode/re-encode) agree on the same numbers.
const (
	// AvatarMaxUploadBytes bounds the raw multipart body the handler will read.
	// It is deliberately generous relative to the re-encoded output, since a
	// valid but poorly compressed JPEG can be several times larger than the PNG
	// we store. The megapixel cap below is the real guard against memory bl:
	// a JPEG bomb small on disk still fails to allocate an oversized canvas.
	AvatarMaxUploadBytes = 5 << 20 // 5 MiB

	// AvatarMaxPixels caps the decoded canvas (width*height) before allocation
	// blows up. 25 MP (e.g. 5000x5000) covers any real profile photo while
	// rejecting decompression bombs whose header advertises a huge canvas.
	AvatarMaxPixels = 25 * 1000 * 1000

	// AvatarOutputSize is the square edge the stored avatar is resized to. A
	// fixed small size normalises wildly different uploads, strips EXIF (we
	// re-encode from decoded pixels), and keeps the served file tiny.
	AvatarOutputSize = 256

	// AvatarMinEdge rejects 1x1 tracking-pixel style uploads.
	AvatarMinEdge = 16
)

// Avatar-specific errors. They map to distinct HTTP statuses in the handler and
// never carry any of the uploaded bytes.
var (
	// ErrAvatarTooLarge is the body/limit breach → 413.
	ErrAvatarTooLarge = errors.New("avatar too large")
	// ErrAvatarUnsupported covers an unreadable, empty, wrong-type, or
	// out-of-bounds image → 415.
	ErrAvatarUnsupported = errors.New("unsupported avatar image")
)

// AvatarContentType is the canonical type of the stored, re-encoded avatar.
// Everything is normalised to PNG: the Go standard library encodes it without a
// third-party dependency, it is lossless for the small square we keep, and it
// carries no scripting surface (unlike SVG, which is rejected outright).
const AvatarContentType = "image/png"

// AvatarFileExtension is the extension used for stored and served files.
const AvatarFileExtension = ".png"
