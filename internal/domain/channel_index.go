package domain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// A channel index is a vendor's signed statement of which release a channel
// points at, and until when that statement holds (RFC 0037 §5.2). A channel
// that never moves looks the same as one an attacker froze; an index that has
// expired is how a machine tells them apart.

// ChannelIndexSchema is the one schema an index is written and read in.
const ChannelIndexSchema = 1

// ChannelIndexFile and ChannelIndexSignatureFile are the two files the index
// artifact carries, by the titles its layers have.
const (
	ChannelIndexFile          = "index.json"
	ChannelIndexSignatureFile = "index.json.minisig"
)

// ChannelIndexTagSuffix is what a channel's tag takes to name its index.
const ChannelIndexTagSuffix = ".index"

// ChannelIndexTag is the tag a channel's index is published under.
func ChannelIndexTag(channel string) string { return channel + ChannelIndexTagSuffix }

// ChannelIndex is the signed document, in the order its fields are written.
type ChannelIndex struct {
	Schema  int    `json:"schema"`
	Channel string `json:"channel"`

	// Sequence orders a channel's indexes. A machine accepts only a higher
	// one, or the same one with the same bytes (decision 16).
	Sequence int64 `json:"sequence"`

	IssuedAt  Time `json:"issued_at"`
	ExpiresAt Time `json:"expires_at"`

	Release ChannelIndexRelease `json:"release"`
}

// ChannelIndexRelease names the release the channel points at. Digest is the
// bundle's content digest, never the registry's manifest digest (decision 10).
type ChannelIndexRelease struct {
	Version Version `json:"version"`
	Digest  string  `json:"digest"`
}

// NewChannelIndex is the index a vendor issues at now, valid for validFor.
// The sequence is the issue time in milliseconds (decision 24).
func NewChannelIndex(channel string, version Version, digest string, now time.Time, validFor time.Duration) ChannelIndex {
	return ChannelIndex{
		Schema:    ChannelIndexSchema,
		Channel:   channel,
		Sequence:  now.UnixMilli(),
		IssuedAt:  NewTime(now),
		ExpiresAt: NewTime(now.Add(validFor)),
		Release:   ChannelIndexRelease{Version: version, Digest: digest},
	}
}

// Encode is the bytes that are signed and published. A machine compares these
// bytes, not the fields, so they are produced once and never re-encoded.
func (i ChannelIndex) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return nil, Internal(err, "cannot encode the channel index")
	}
	return append(data, '\n'), nil
}

// Comment is the trusted comment the index is signed with, which minisign
// prints when it verifies one.
func (i ChannelIndex) Comment() string {
	return fmt.Sprintf("morzer channel index %s sequence %d", i.Channel, i.Sequence)
}

// ParseChannelIndex reads an index and refuses one that is not well formed.
// It checks the document, not its signature or its freshness: those are the
// caller's, against its keys and its clock.
func ParseChannelIndex(data []byte) (ChannelIndex, error) {
	var idx ChannelIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, ValidationError(err, "the channel index is not a JSON document")
	}
	switch {
	case idx.Schema != ChannelIndexSchema:
		return idx, ValidationError(nil, "the channel index has schema %d, not %d", idx.Schema, ChannelIndexSchema).
			WithHint("an index of a newer schema is read by a newer manager")
	case strings.TrimSpace(idx.Channel) == "":
		return idx, ValidationError(nil, "the channel index names no channel")
	case idx.Sequence <= 0:
		return idx, ValidationError(nil, "the channel index has no sequence")
	case idx.IssuedAt.IsZero() || idx.ExpiresAt.IsZero():
		return idx, ValidationError(nil, "the channel index has no issue or expiry time")
	case !idx.ExpiresAt.After(idx.IssuedAt.Time):
		return idx, ValidationError(nil, "the channel index expires before it was issued")
	case idx.Release.Version.IsZero():
		return idx, ValidationError(nil, "the channel index names no release version")
	case !contentDigest(idx.Release.Digest):
		return idx, ValidationError(nil, "the channel index's release digest %q is not a sha256 digest", idx.Release.Digest)
	}
	return idx, nil
}

// contentDigest reports whether s is sha256: and 64 lowercase hex digits, the
// form a content digest is written in.
func contentDigest(s string) bool {
	hexPart, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(hexPart) != 64 || strings.ToLower(hexPart) != hexPart {
		return false
	}
	_, err := hex.DecodeString(hexPart)
	return err == nil
}
