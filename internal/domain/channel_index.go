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
	Version Version `yaml:"version" json:"version"`
	Digest  string  `yaml:"digest" json:"digest"`
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

// AcceptedIndex is the index a machine accepted for a channel: what the next
// one is compared with (decision 16), and what the tag is judged against while
// it holds (decisions 10 and 25). An export carries it (decision 31).
type AcceptedIndex struct {
	Sequence int64 `yaml:"sequence" json:"sequence"`

	// Digest is the SHA-256 of the index's bytes as published, which
	// decides decision 16's equal case: the same sequence is accepted again
	// only with the same bytes.
	Digest string `yaml:"digest" json:"digest"`

	Release   ChannelIndexRelease `yaml:"release" json:"release"`
	ExpiresAt Time                `yaml:"expires_at" json:"expires_at"`
}

// Expired reports whether the index no longer holds at now, by this machine's
// clock with no allowance for skew (decision 32).
func (a AcceptedIndex) Expired(now time.Time) bool { return !now.Before(a.ExpiresAt.Time) }

// ChannelIndexes maps a channel, keyed as the channel marks are, to the index
// it accepted.
type ChannelIndexes map[string]AcceptedIndex

// ChannelIndexRecord is what the follower knows of one channel's index: the
// index it accepted, and what the last poll found that `status` and `doctor`
// report (decision 30).
type ChannelIndexRecord struct {
	Accepted *AcceptedIndex `json:"accepted,omitempty"`

	// Problem says why the last poll accepted nothing new: the index it
	// found could not be accepted, or it found none while one is accepted.
	Problem string `json:"problem,omitempty"`

	// Unchecked is a channel that publishes an index this installation
	// pins no key to check (decision 26).
	Unchecked bool `json:"unchecked,omitempty"`

	// Disagrees says the tag still serves a release other than the one the
	// accepted index names.
	Disagrees string `json:"disagrees,omitempty"`

	// ExpiryNotified is the sequence of the accepted index whose expiry was
	// notified, so each index's expiry is notified once (decision 28).
	ExpiryNotified int64 `json:"expiry_notified,omitempty"`
}

// IsZero reports a record with nothing in it.
func (r ChannelIndexRecord) IsZero() bool {
	return r.Accepted == nil && r.Problem == "" && !r.Unchecked && r.Disagrees == "" && r.ExpiryNotified == 0
}

// ChannelIndexRecords maps a channel, keyed as the channel marks are, to its
// record.
type ChannelIndexRecords map[string]ChannelIndexRecord

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
