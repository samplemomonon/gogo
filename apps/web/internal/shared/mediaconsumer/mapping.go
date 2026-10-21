package mediaconsumer

import (
	"strconv"
	"time"

	mediav1 "gogo/contracts/media/v1"
)

// This file is every wire↔neutral conversion in the kit, in one place.
//
// The conversion rules follow the productconsumer kit's D-018 conventions
// because the shapes cross the same boundary: media ids arrive as int64
// (media-service's key) and are carried as decimal strings, so they match the
// product ids gogo's own modules already carry (a product's primary_media_id is
// read back as a string next to the product's own string id). Decimal
// stringification is exact and round-trips.

// numericIDs converts gogo's decimal-string media ids to the wire int64 keys. An
// empty or non-numeric id becomes 0, which the contract reads as "unset" — so a
// malformed id degrades to a no-op filter rather than to a wrong row.
func numericIDs(ids []string) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		out = append(out, numericID(id))
	}
	return out
}

// numericID converts one decimal-string id to the wire int64 key.
func numericID(id string) int64 {
	if id == "" {
		return 0
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// formatID converts an int64 key to gogo's decimal-string id.
func formatID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// mediaFromWire maps the wire Media into the neutral Media.
func mediaFromWire(m *mediav1.Media) Media {
	if m == nil {
		return Media{}
	}
	return Media{
		ID:          formatID(m.GetId()),
		OwnerID:     m.GetOwnerId(),
		Filename:    m.GetFilename(),
		ContentType: m.GetContentType(),
		SizeBytes:   m.GetSizeBytes(),
		Kind:        kindFromWire(m.GetKind()),
		Status:      statusFromWire(m.GetStatus()),
		CreatedAt:   asTime(m.GetCreatedAt()),
		UpdatedAt:   asTime(m.GetUpdatedAt()),
	}
}

// uploadURLFromWire maps the wire UploadURL into the neutral UploadURL.
func uploadURLFromWire(u *mediav1.UploadURL) UploadURL {
	if u == nil {
		return UploadURL{}
	}
	return UploadURL{
		URL:       u.GetUrl(),
		Method:    u.GetMethod(),
		Headers:   u.GetHeaders(),
		ExpiresAt: asTime(u.GetExpiresAt()),
	}
}

// wireKind converts a neutral kind into the contract's enum. An empty kind is
// sent as UNSPECIFIED so media-service can infer it from the content type.
func wireKind(k Kind) mediav1.MediaKind {
	switch k {
	case KindImage:
		return mediav1.MediaKind_MEDIA_KIND_IMAGE
	case KindVideo:
		return mediav1.MediaKind_MEDIA_KIND_VIDEO
	case KindAudio:
		return mediav1.MediaKind_MEDIA_KIND_AUDIO
	case KindDocument:
		return mediav1.MediaKind_MEDIA_KIND_DOCUMENT
	case KindOther:
		return mediav1.MediaKind_MEDIA_KIND_OTHER
	default:
		return mediav1.MediaKind_MEDIA_KIND_UNSPECIFIED
	}
}

// kindFromWire maps the contract's MediaKind enum onto the neutral vocabulary. An
// enum this version does not understand is reported as KindOther rather than an
// empty string, so a caller rendering "no kind" never confuses "unset" with
// "one of the known kinds".
func kindFromWire(k mediav1.MediaKind) Kind {
	switch k {
	case mediav1.MediaKind_MEDIA_KIND_IMAGE:
		return KindImage
	case mediav1.MediaKind_MEDIA_KIND_VIDEO:
		return KindVideo
	case mediav1.MediaKind_MEDIA_KIND_AUDIO:
		return KindAudio
	case mediav1.MediaKind_MEDIA_KIND_DOCUMENT:
		return KindDocument
	case mediav1.MediaKind_MEDIA_KIND_OTHER:
		return KindOther
	default:
		return KindOther
	}
}

// statusFromWire maps the contract's MediaStatus enum onto the neutral
// vocabulary.
func statusFromWire(s mediav1.MediaStatus) Status {
	switch s {
	case mediav1.MediaStatus_MEDIA_STATUS_PENDING:
		return StatusPending
	case mediav1.MediaStatus_MEDIA_STATUS_READY:
		return StatusReady
	default:
		return StatusPending
	}
}

// asTime converts an optional protobuf Timestamp to a time.Time. A nil
// timestamp is the zero time, not the epoch: it means "the contract carried no
// timestamp", a different fact from "1970-01-01".
func asTime(ts interface{ AsTime() time.Time }) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
